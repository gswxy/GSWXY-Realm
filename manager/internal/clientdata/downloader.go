// Package clientdata downloads and verifies AzerothCore Client Data
// (dbc/maps/vmaps/mmaps/Cameras) with a three-tier source strategy:
// official release -> public proxy pool -> manual import.
package clientdata

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// Resource describes one downloadable client data release (resources.json).
type Resource struct {
	Version      string   `json:"version"`
	Label        string   `json:"label,omitempty"`
	URL          string   `json:"url"`
	Filename     string   `json:"filename"`
	SizeBytes    int64    `json:"size_bytes,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	Requires     []string `json:"requires"`
	CompatibleTo string   `json:"compatible_core_revision,omitempty"`
	Mirrors      []string `json:"mirrors,omitempty"`
}

// Manager owns download state and verification.
type Manager struct {
	paths platform.Paths
	log   *logging.Logger
	// OnInstalled is invoked after a verified install so the caller can
	// persist the version into its own state store.
	OnInstalled func(version string)

	mu      sync.Mutex
	active  bool
	cancel  context.CancelFunc
	progress Progress
}

// Progress mirrors the download job for the API.
type Progress struct {
	Active     bool    `json:"active"`
	Source     string  `json:"source"`
	URL        string  `json:"url"`
	BytesDone  int64   `json:"bytes_done"`
	BytesTotal int64   `json:"bytes_total"`
	SpeedBps   float64 `json:"speed_bps"`
	Error      string  `json:"error,omitempty"`
}

func NewManager(p platform.Paths, log *logging.Logger) *Manager {
	return &Manager{paths: p, log: log}
}

// LoadResource reads resources.json shipped with the payload.
func (m *Manager) LoadResource() (*Resource, error) {
	raw, err := os.ReadFile(m.paths.ResourcesJSON())
	if err != nil {
		return nil, err
	}
	var r Resource
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// InstalledVersion returns the recorded installed data version ("" = none).
func (m *Manager) InstalledVersion() string {
	raw, err := os.ReadFile(m.paths.ClientData() + ".json")
	if err != nil {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Version
}

// RequiredDirs checks the unpacked data completeness.
func (m *Manager) RequiredDirs(res *Resource) []string {
	missing := []string{}
	for _, d := range res.Requires {
		if _, err := os.Stat(filepath.Join(m.paths.ClientData(), d)); err != nil {
			missing = append(missing, d)
		}
	}
	return missing
}

// Sources builds the ordered URL list: official first, mirrors as fallback.
func (m *Manager) Sources(res *Resource) []string {
	urls := []string{res.URL}
	urls = append(urls, res.Mirrors...)
	return urls
}

// Download runs a full download job in the background.
func (m *Manager) Download(res *Resource, manualFile string) error {
	m.mu.Lock()
	if m.active {
		m.mu.Unlock()
		return fmt.Errorf("已有下载任务在进行中")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.active = true
	m.progress = Progress{Active: true}
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.active = false
			m.progress.Active = false
			m.mu.Unlock()
			cancel()
		}()

		var archive string
		var err error
		if manualFile != "" {
			archive, err = m.importManual(res, manualFile)
			m.setSource("manual", manualFile)
		} else {
			archive, err = m.downloadChain(ctx, res)
		}
		if err != nil {
			m.fail(err)
			return
		}
		if err := m.verifyAndUnpack(res, archive); err != nil {
			m.fail(err)
			return
		}
		m.succeed(res)
	}()
	return nil
}

func (m *Manager) downloadChain(ctx context.Context, res *Resource) (string, error) {
	var lastErr error
	for _, url := range m.Sources(res) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		name := sourceName(url)
		m.setSource(name, url)
		m.log.Info("downloading client data from %s", name)
		path := filepath.Join(m.paths.Downloads(), res.Filename)
		if err := m.downloadOne(ctx, url, res, path); err != nil {
			m.log.Warn("source %s failed: %v", name, err)
			lastErr = err
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("所有下载源均失败，最后一个错误: %w", lastErr)
}

// downloadOne streams to <path>.part with HTTP Range resume. A complete
// local file (exact size + valid sha256 when pinned) short-circuits the
// network entirely — pre-seeded caches and retries just work.
func (m *Manager) downloadOne(ctx context.Context, url string, res *Resource, path string) error {
	if st, err := os.Stat(path); err == nil {
		sizeOK := res.SizeBytes == 0 || st.Size() == res.SizeBytes
		if sizeOK {
			if res.SHA256 != "" {
				if sum, err := checksumFile(path); err == nil && sum == res.SHA256 {
					m.log.Info("reusing complete local file %s", path)
					return nil
				}
			} else {
				m.log.Info("reusing existing local file %s (size match)", path)
				return nil
			}
		}
	}
	part := path + ".part"
	_ = os.MkdirAll(m.paths.Downloads(), 0o755)

	var offset int64
	if st, err := os.Stat(part); err == nil {
		// Refuse to resume beyond the expected total.
		if res.SizeBytes > 0 && st.Size() > res.SizeBytes {
			_ = os.Remove(part)
		} else {
			offset = st.Size()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := &http.Client{Timeout: 0} // streaming; per-read deadlines below
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		// resuming
	case resp.StatusCode == http.StatusOK:
		offset = 0
		_ = os.Remove(part)
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}

	total := resp.ContentLength + offset
	start := time.Now()
	var done int64 = offset
	buf := make([]byte, 256<<10)
	t0 := time.Now()
	var doneAtT0 int64 = offset

	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if time.Since(t0) > time.Second {
				m.setProgress(done, total, float64(done-doneAtT0)/time.Since(t0).Seconds())
				t0 = time.Now()
				doneAtT0 = done
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return rerr // resumable: .part is kept
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	m.setProgress(done, total, 0)

	// Size check before rename.
	if res.SizeBytes > 0 && done != res.SizeBytes {
		return fmt.Errorf("下载不完整: %d / %d 字节", done, res.SizeBytes)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	_ = start
	return os.Rename(part, path)
}

func (m *Manager) importManual(res *Resource, src string) (string, error) {
	// Validate before accepting: size must match if known, sha256 if known.
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if res.SizeBytes > 0 && st.Size() != res.SizeBytes {
		return "", fmt.Errorf("文件大小不符: %d (期望 %d)", st.Size(), res.SizeBytes)
	}
	dst := filepath.Join(m.paths.Downloads(), res.Filename)
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (m *Manager) verifyAndUnpack(res *Resource, archive string) error {
	// 1. checksum (when pinned) — otherwise integrity via zip CRC.
	if res.SHA256 != "" {
		sum, err := checksumFile(archive)
		if err != nil {
			return err
		}
		if sum != res.SHA256 {
			return fmt.Errorf("SHA-256 不匹配: %s", sum)
		}
	}

	// 2. disk space pre-check: unpacked data is roughly 2x the archive.
	if st, err := os.Stat(archive); err == nil {
		if err := checkDiskSpace(m.paths.ClientData(), st.Size()*3); err != nil {
			return err
		}
	}

	// 3. open zip and validate member paths (zip-slip guard).
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("打开压缩包失败: %w", err)
	}
	defer zr.Close()

	root := m.paths.ClientData()
	for _, zf := range zr.File {
		name := filepath.FromSlash(zf.Name)
		if strings.ContainsAny(name, "\\") {
			return fmt.Errorf("压缩包内路径非法: %s", zf.Name)
		}
		clean := filepath.Clean(filepath.Join(root, name))
		if !platform.IsUnder(root, clean) {
			return fmt.Errorf("压缩包内路径越界: %s", zf.Name)
		}
	}

	// 4. unpack.
	for _, zf := range zr.File {
		if err := extractMember(root, zf); err != nil {
			return err
		}
	}

	// 5. post-unpack directory verification.
	if missing := m.RequiredDirs(res); len(missing) > 0 {
		return fmt.Errorf("解压后缺少目录: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (m *Manager) succeed(res *Resource) {
	rec := map[string]any{
		"version":     res.Version,
		"verified_at": time.Now().Format(time.RFC3339),
	}
	raw, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(m.paths.ClientData()+".json", raw, 0o644)
	_ = os.Remove(filepath.Join(m.paths.Downloads(), res.Filename)) // free space
	m.mu.Lock()
	m.progress.Error = ""
	m.mu.Unlock()
	if m.OnInstalled != nil {
		m.OnInstalled(res.Version)
	}
	m.log.Info("client data %s installed and verified", res.Version)
}

func (m *Manager) fail(err error) {
	m.mu.Lock()
	m.progress.Error = err.Error()
	m.mu.Unlock()
	m.log.Error("client data download failed: %v", err)
}

func (m *Manager) setSource(name, url string) {
	m.mu.Lock()
	m.progress.Source = name
	m.progress.URL = url
	m.mu.Unlock()
}

func (m *Manager) setProgress(done, total int64, bps float64) {
	m.mu.Lock()
	m.progress.BytesDone = done
	m.progress.BytesTotal = total
	m.progress.SpeedBps = bps
	m.mu.Unlock()
}

// Cancel aborts the active download (if any).
func (m *Manager) Cancel() {
	m.mu.Lock()
	c := m.cancel
	m.mu.Unlock()
	if c != nil {
		c()
	}
}

// ManualCandidates lists Data.zip files the user dropped into
// downloads/manual via the fnOS file manager (NAS-local import path).
func (m *Manager) ManualCandidates(res *Resource) []string {
	dir := filepath.Join(m.paths.Downloads(), "manual")
	var out []string
	for _, name := range []string{res.Filename, "Data.zip"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			out = append(out, p)
		}
	}
	return out
}

// ScanManualDir validates and returns the local Data.zip candidate; the
// subsequent Download(res, path) run performs size/sha verification before
// unpacking.
func (m *Manager) ScanManualDir(res *Resource) (string, error) {
	cands := m.ManualCandidates(res)
	if len(cands) == 0 {
		return "", fmt.Errorf("未在 %s 找到 Data.zip（可从 fnOS 文件管理器把官方 Data.zip 放入该目录后重试）",
			filepath.Join(m.paths.Downloads(), "manual"))
	}
	for _, p := range cands {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if res.SizeBytes > 0 && st.Size() != res.SizeBytes {
			return "", fmt.Errorf("文件大小不符: %s 为 %d 字节（期望 %d），请重新下载完整文件",
				filepath.Base(p), st.Size(), res.SizeBytes)
		}
		return p, nil
	}
	return "", fmt.Errorf("候选文件不可读")
}

// ProgressSnapshot returns the current progress (value copy).
func (m *Manager) ProgressSnapshot() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress
}

func sourceName(url string) string {
	switch {
	case strings.Contains(url, "github.com/wowgaming"):
		return "官方 GitHub Release"
	case strings.Contains(url, "github.com"):
		return "公共 GitHub 代理"
	default:
		return "镜像源"
	}
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func extractMember(root string, zf *zip.File) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	path := filepath.Join(root, filepath.FromSlash(zf.Name))
	if zf.FileInfo().IsDir() {
		return os.MkdirAll(path, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}
