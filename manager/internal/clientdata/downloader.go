// Package clientdata downloads and verifies the AzerothCore Client Data
// (enUS DBC + maps/vmaps/mmaps/Cameras) with a multi-line source strategy:
// official GitHub → verified public accelerators → manual import.
//
// 数据包必须是官方 enUS 版（wowgaming/client-data，从英文客户端提取）。
// 锁定的 SHA-256 即 enUS 数据包的完整性依据：哈希吻合 = 官方原包；
// 不匹配一律拒绝，不伪造"校验通过"。
package clientdata

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/mirrors"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// 时间参数（测试可覆盖）。
var (
	probeTimeout  = 6 * time.Second  // 线路探测超时
	stallTimeout  = 30 * time.Second // 无数据传输超时
	dialTimeout   = 10 * time.Second
	headerTimeout = 20 * time.Second
	zipMagic      = []byte{'P', 'K'}
	contentRangeRe = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)
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
}

// Manager owns download state and verification.
type Manager struct {
	paths   platform.Paths
	log     *logging.Logger
	Mirrors mirrors.Config
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
	Active     bool       `json:"active"`
	Source     string     `json:"source"`
	URL        string     `json:"url"`
	BytesDone  int64      `json:"bytes_done"`
	BytesTotal int64      `json:"bytes_total"`
	SpeedBps   float64    `json:"speed_bps"`
	Error      string     `json:"error,omitempty"`
	Mode       string     `json:"mode,omitempty"`               // auto|official|mirror|manual
	Attempts   []Attempt  `json:"attempts,omitempty"`           // 已尝试线路及失败原因
}

// Attempt records one source try (kept in progress so the UI can show
// exactly why a line was skipped).
type Attempt struct {
	Source string `json:"source"`
	OK     bool   `json:"ok,omitempty"`
	Error  string `json:"error,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// NewManager builds a downloader with the payload's mirror config.
func NewManager(p platform.Paths, log *logging.Logger, cfg mirrors.Config) *Manager {
	return &Manager{paths: p, log: log, Mirrors: cfg}
}

// LoadResource reads resources.json shipped with the payload.
func (m *Manager) LoadResource() (*Resource, error) {
	raw, err := os.ReadFile(m.paths.ResourcesJSON())
	if err != nil {
		return nil, err
	}
	var r Resource
	if err := jsonUnmarshal(raw, &r); err != nil {
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
	_ = jsonUnmarshal(raw, &v)
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

// candidate is one download line.
type candidate struct {
	name string
	url  string
}

// SourceModes accepted by Download.
const (
	ModeAuto     = "auto"     // 官方优先，探测失败自动走镜像（国内推荐）
	ModeOfficial = "official" // 仅官方 GitHub
	ModeMirror   = "mirror"   // 仅镜像线路
	ModeManual   = "manual"   // 本地导入
)

// candidates builds the ordered URL list for a mode. "mirror" uses every
// configured mirror; "mirror:<name>" pins ONE specific line (the UI sends
// this when the user picks a line explicitly).
func (m *Manager) candidates(res *Resource, mode string) []candidate {
	var out []candidate
	if mode == ModeOfficial {
		return []candidate{{name: "官方 GitHub", url: res.URL}}
	}
	if strings.HasPrefix(mode, "mirror:") {
		want := strings.TrimPrefix(mode, "mirror:")
		for _, mm := range m.Mirrors.ClientDataMirrors {
			if mm.Name == want {
				return []candidate{{name: mm.Name, url: mm.Fill(res.Version, res.Filename)}}
			}
		}
		return nil // 用户选定的线路已不在当前配置中
	}
	if mode == ModeMirror {
		for _, mm := range m.Mirrors.ClientDataMirrors {
			out = append(out, candidate{name: mm.Name, url: mm.Fill(res.Version, res.Filename)})
		}
		return out
	}
	// auto：官方 + 镜像，顺序由 probeSources 决定
	out = append(out, candidate{name: "官方 GitHub", url: res.URL})
	for _, mm := range m.Mirrors.ClientDataMirrors {
		out = append(out, candidate{name: mm.Name, url: mm.Fill(res.Version, res.Filename)})
	}
	return out
}

// probeResult is one line's connectivity probe.
type probeResult struct {
	c    candidate
	ok   bool
	rtt  time.Duration
	note string
}

// probeSources concurrently measures every line with a real ranged GET:
// 只有真实拿到 ZIP 头部字节才算通（HTML 错误页 / 超时 / 5xx 均算不通），
// 不凭域名猜测速度。官方线在此阶段就会让位给更快的可用线路，
// 国内网络不会长时间卡死在官方源。
func (m *Manager) probeSources(ctx context.Context, cands []candidate) []probeResult {
	var wg sync.WaitGroup
	results := make([]probeResult, len(cands))
	client := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout: dialTimeout,
			DisableKeepAlives:   true,
		},
	}
	for i, c := range cands {
		wg.Add(1)
		go func(i int, c candidate) {
			defer wg.Done()
			start := time.Now()
			pr := probeResult{c: c}
			defer func() { results[i] = pr }()

			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(pctx, http.MethodGet, c.url, nil)
			if err != nil {
				pr.note = err.Error()
				return
			}
			req.Header.Set("Range", "bytes=0-1023")
			resp, err := client.Do(req)
			if err != nil {
				pr.note = "探测失败: " + err.Error()
				return
			}
			defer resp.Body.Close()
			head := make([]byte, 1024)
			n, _ := io.ReadFull(io.LimitReader(resp.Body, 1024), head)
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				pr.note = fmt.Sprintf("HTTP %d", resp.StatusCode)
				return
			}
			if n < 4 || !bytesEqual(head[:2], zipMagic) {
				pr.note = "返回内容不是 ZIP 文件"
				return
			}
			if resp.StatusCode == http.StatusPartialContent && resSizeMismatchCR(resp.Header.Get("Content-Range")) {
				pr.note = "Content-Range 与预期文件不符"
				return
			}
			pr.ok = true
			pr.rtt = time.Since(start)
		}(i, c)
	}
	wg.Wait()
	return results
}

// resSizeMismatchCR reports whether a probe's Content-Range total looks
// wrong; without an expected size it always passes (deep checks happen in
// the real download).
func resSizeMismatchCR(contentRange string) bool {
	cr := contentRangeRe.FindStringSubmatch(contentRange)
	return contentRange != "" && cr == nil // header present but malformed
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// orderForAuto sorts probe results: reachable first (by rtt), unreachable
// last (as fallbacks, still tried in original order).
func orderForAuto(cands []candidate, probes []probeResult) []candidate {
	var fast, slow []candidate
	for _, p := range probes {
		if p.ok {
			fast = append(fast, p.c)
		} else {
			slow = append(slow, p.c)
		}
	}
	// stable rtt sort for fast
	for i := 1; i < len(fast); i++ {
		for j := i; j > 0; j-- {
			a, b := idxOf(probes, fast[j]), idxOf(probes, fast[j-1])
			if probes[a].rtt < probes[b].rtt {
				fast[j], fast[j-1] = fast[j-1], fast[j]
			} else {
				break
			}
		}
	}
	return append(fast, slow...)
}

func idxOf(probes []probeResult, c candidate) int {
	for i := range probes {
		if probes[i].c.url == c.url {
			return i
		}
	}
	return -1
}

// Download starts a full download job in the background. mode is one of
// ModeAuto/ModeOfficial/ModeMirror (manualFile implies ModeManual).
func (m *Manager) Download(res *Resource, manualFile string) error { return m.DownloadMode(res, manualFile, ModeAuto) }

func (m *Manager) DownloadMode(res *Resource, manualFile string, mode string) error {
	if manualFile != "" {
		mode = ModeManual
	}
	valid := mode == ModeAuto || mode == ModeOfficial || mode == ModeMirror ||
		strings.HasPrefix(mode, "mirror:") // 指定具体镜像线路
	if !valid {
		mode = ModeAuto
	}
	m.mu.Lock()
	if m.active {
		m.mu.Unlock()
		return fmt.Errorf("已有下载任务在进行中")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.active = true
	m.progress = Progress{Active: true, Mode: mode}
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.active = false
			m.progress.Active = false
			m.mu.Unlock()
			cancel()
		}()

		if manualFile != "" {
			m.runManual(res, manualFile)
			return
		}
		m.runDownload(ctx, res, mode)
	}()
	return nil
}

// runDownload: probe (auto) → try each line: download + SHA-256 verify;
// 损坏文件立即删除，绝不把校验失败的文件留给下一轮“复用”。
func (m *Manager) runDownload(ctx context.Context, res *Resource, mode string) {
	cands := m.candidates(res, mode)
	if len(cands) == 0 {
		m.fail(fmt.Errorf("没有可用的下载线路（镜像未配置）"))
		return
	}
	if mode == ModeAuto && len(cands) > 1 {
		m.setPhase("探测下载线路", cands[0].url)
		probes := m.probeSources(ctx, cands)
		for _, p := range probes {
			m.addAttempt(p.c.name, p.ok, p.note, 0)
		}
		cands = orderForAuto(cands, probes)
		m.log.Info("source order after probe: %v", names(cands))
	}

	var lastErr error
	for _, c := range cands {
		select {
		case <-ctx.Done():
			m.fail(ctx.Err())
			return
		default:
		}
		m.setSource(c.name, c.url)
		m.log.Info("downloading client data from %s", c.name)
		path := filepath.Join(m.paths.Downloads(), res.Filename)
		n, err := m.downloadOne(ctx, c.url, res, path)
		if err != nil {
			m.log.Warn("source %s failed: %v", c.name, err)
			m.addAttempt(c.name, false, err.Error(), n)
			if errors.Is(err, context.Canceled) {
				// 用户主动取消：保留同线路半成品，下次可续传
				m.fail(fmt.Errorf("已取消下载（已下载的 %.1f MB 可在重试时续传）", float64(n)/1048576))
				return
			}
			m.cleanPartial(path)
			lastErr = err
			continue
		}
		// 完整性：大小 + SHA-256（锁定哈希 = 官方 enUS 包依据）
		if err := m.verifyFile(res, path); err != nil {
			m.addAttempt(c.name, false, "完整性校验失败: "+err.Error(), n)
			_ = os.Remove(path) // 坏文件绝不缓存
			m.cleanPartial(path)
			m.log.Warn("source %s integrity: %v", c.name, err)
			lastErr = err
			continue
		}
		m.addAttempt(c.name, true, "", n)
		if err := m.unpackVerified(res, path); err != nil {
			m.fail(err)
			return
		}
		m.succeed(res)
		return
	}
	m.fail(fmt.Errorf("全部下载线路失败（已尝试 %d 条）。建议：1) 稍后重试；2) 在本机或NAS下载官方 Data.zip 后用「本地导入」。最后错误: %w", len(cands), lastErr))
}

func names(cands []candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.name)
	}
	return out
}

func (m *Manager) runManual(res *Resource, src string) {
	m.setSource("本地导入", src)
	st, err := os.Stat(src)
	if err != nil {
		m.fail(err)
		return
	}
	if res.SizeBytes > 0 && st.Size() != res.SizeBytes {
		m.fail(fmt.Errorf("文件大小不符: %d (期望 %d)。请下载与 Core 版本匹配的官方 enUS Data.zip（中文客户端的 Data 目录不能替代服务端数据包）", st.Size(), res.SizeBytes))
		return
	}
	dst := filepath.Join(m.paths.Downloads(), res.Filename)
	if err := copyFile(src, dst); err != nil {
		m.fail(err)
		return
	}
	if err := m.verifyFile(res, dst); err != nil {
		_ = os.Remove(dst)
		m.fail(fmt.Errorf("完整性校验失败: %v。请确认是官方发布的 enUS 服务端数据包（wowgaming/client-data）", err))
		return
	}
	if err := m.unpackVerified(res, dst); err != nil {
		m.fail(err)
		return
	}
	m.succeed(res)
}

// verifyFile checks size + pinned SHA-256 (when set).
func (m *Manager) verifyFile(res *Resource, path string) error {
	if res.SizeBytes > 0 {
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		if st.Size() != res.SizeBytes {
			return fmt.Errorf("大小不符: %d / %d 字节", st.Size(), res.SizeBytes)
		}
	}
	if res.SHA256 != "" {
		sum, err := checksumFile(path)
		if err != nil {
			return err
		}
		if sum != res.SHA256 {
			return fmt.Errorf("SHA-256 不匹配: %s", sum)
		}
	}
	return nil
}

// cleanPartial removes the .part file AND its source sidecar.
func (m *Manager) cleanPartial(path string) {
	_ = os.Remove(path + ".part")
	_ = os.Remove(path + ".part.src")
}

// downloadOne streams to <path>.part with resume ONLY when the partial
// file was produced by the same URL — 跨线路续传无法确认内容一致时从头
// 下载，避免不同来源数据错误拼接。206 的 Content-Range 全长必须与
// 预期一致，200 忽略 Range 时自动放弃续传从头接收。
func (m *Manager) downloadOne(ctx context.Context, url string, res *Resource, path string) (int64, error) {
	part := path + ".part"
	srcSidecar := path + ".part.src"
	_ = os.MkdirAll(m.paths.Downloads(), 0o755)

	var offset int64
	if st, err := os.Stat(part); err == nil {
		prevURL, _ := os.ReadFile(srcSidecar)
		if string(prevURL) == url {
			if res.SizeBytes > 0 && st.Size() > res.SizeBytes {
				m.cleanPartial(path)
			} else {
				offset = st.Size()
			}
		} else {
			// 不同线路的半成品不可拼接
			m.cleanPartial(path)
			m.log.Info("discarding .part from a different source (cannot verify consistency)")
		}
	}

	// 请求绑定 dctx：外层取消（用户/任务结束）与停滞看门狗都能真正
	// 中断正文读取（net/http 在 context 取消时关闭连接，解除 Read 阻塞）。
	dctx, dcancel := context.WithCancel(ctx)
	defer dcancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return offset, err
	}
	defer resp.Body.Close()

	// HTML 错误页检测放最前：不少代理对失效资源返回 200 + 网页。
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return 0, fmt.Errorf("线路返回了网页而非数据文件（HTTP %d）", resp.StatusCode)
	}

	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		cr := contentRangeRe.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if cr == nil {
			m.cleanPartial(path)
			return 0, fmt.Errorf("206 缺少合法 Content-Range")
		}
		start, _ := strconv.ParseInt(cr[1], 10, 64)
		total, _ := strconv.ParseInt(cr[3], 10, 64)
		if start != offset {
			m.cleanPartial(path)
			return 0, fmt.Errorf("Content-Range 起点不符（服务器不支持按请求续传）")
		}
		if res.SizeBytes > 0 && total != res.SizeBytes {
			m.cleanPartial(path)
			return 0, fmt.Errorf("Content-Range 总长 %d 与预期 %d 不符（疑似不同文件）", total, res.SizeBytes)
		}
	case resp.StatusCode == http.StatusOK:
		if offset > 0 {
			// 服务器忽略 Range：放弃续传，从头接收，避免拼接错位
			m.cleanPartial(path)
			offset = 0
			m.log.Info("server ignored Range; restarting from scratch")
		}
	default:
		return offset, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 长度核对（无 Range 响应的 Content-Length 必须等于预期）。
	if res.SizeBytes > 0 && resp.ContentLength > 0 && resp.StatusCode == http.StatusOK &&
		resp.ContentLength != res.SizeBytes {
		return 0, fmt.Errorf("文件长度 %d 与预期 %d 不符", resp.ContentLength, res.SizeBytes)
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return offset, err
	}
	defer f.Close() // 幂等：显式 Close 后再 Close 仅返回错误
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	_ = os.WriteFile(srcSidecar, []byte(url), 0o600)

	// 停滞看门狗：超过 stallTimeout 无任何字节到达则取消请求本身。
	// 清理必须"先 cancel 再等退出"，拆成两个 defer 会因 LIFO 顺序互相
	// 等待造成死锁（每次下载多挂 stallTimeout）。
	wctx, wcancel := context.WithCancel(context.Background())
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		tick := time.NewTicker(stallTimeout / 6)
		defer tick.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-tick.C:
				if time.Since(time.Unix(0, lastRead.Load())) > stallTimeout {
					m.log.Warn("stall watchdog fired (%s)", url)
					dcancel() // 中断 HTTP 请求（解除正文读取阻塞）
					wcancel()
					return
				}
			}
		}
	}()
	defer func() { wcancel(); <-watchDone }()

	total := resp.ContentLength + offset
	if resp.ContentLength < 0 {
		total = 0
	}
	var done int64 = offset
	buf := make([]byte, 256<<10)
	t0 := time.Now()
	var doneAtT0 int64 = offset
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			lastRead.Store(time.Now().UnixNano())
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
			if wctx.Err() != nil {
				return done, fmt.Errorf("连接停滞超过 %s（无数据传输）", stallTimeout)
			}
			return done, rerr // resumable: .part/.part.src kept
		}
		select {
		case <-wctx.Done():
			return done, fmt.Errorf("连接停滞超过 %s（无数据传输）", stallTimeout)
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
	}
	m.setProgress(done, total, 0)

	if res.SizeBytes > 0 && done != res.SizeBytes {
		return done, fmt.Errorf("下载不完整: %d / %d 字节", done, res.SizeBytes)
	}
	if err := f.Sync(); err != nil {
		return done, err
	}
	// Windows 不允许重命名仍打开的文件：先关句柄再落位。
	if err := f.Close(); err != nil {
		return done, err
	}
	return done, os.Rename(part, path)
}

func (m *Manager) httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout:   dialTimeout,
			ResponseHeaderTimeout: headerTimeout,
			DisableKeepAlives:     true,
		},
	}
}

// unpackVerified opens the archive, validates member paths, disk space,
// extracts and verifies required directories.
func (m *Manager) unpackVerified(res *Resource, archive string) error {
	if st, err := os.Stat(archive); err == nil {
		if err := checkDiskSpace(m.paths.ClientData(), st.Size()*3); err != nil {
			return err
		}
	}

	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("打开压缩包失败: %w", err)
	}
	defer zr.Close()

	root := m.paths.ClientData()
	for _, zf := range zr.File {
		// 检测必须基于原始（正斜杠）成员名：FromSlash 在 Windows 会引入
		// 反斜杠，先转换再检测会误杀所有合法条目。
		if strings.Contains(zf.Name, "\\") || strings.Contains(zf.Name, ":") {
			return fmt.Errorf("压缩包内路径非法: %s", zf.Name)
		}
		clean := filepath.Clean(filepath.Join(root, filepath.FromSlash(zf.Name)))
		if !platform.IsUnder(root, clean) {
			return fmt.Errorf("压缩包内路径越界: %s", zf.Name)
		}
	}

	for _, zf := range zr.File {
		if err := extractMember(root, zf); err != nil {
			return err
		}
	}

	if missing := m.RequiredDirs(res); len(missing) > 0 {
		return fmt.Errorf("解压后缺少目录: %s（请使用官方 enUS 数据包）", strings.Join(missing, ", "))
	}
	return nil
}

func (m *Manager) succeed(res *Resource) {
	rec := map[string]any{
		"version":     res.Version,
		"verified_at": time.Now().Format(time.RFC3339),
	}
	raw, _ := jsonMarshal(rec)
	_ = os.WriteFile(m.paths.ClientData()+".json", raw, 0o644)
	_ = os.Remove(filepath.Join(m.paths.Downloads(), res.Filename)) // free space
	m.mu.Lock()
	m.progress.Error = ""
	m.mu.Unlock()
	if m.OnInstalled != nil {
		m.OnInstalled(res.Version)
	}
	m.log.Info("client data %s installed and verified (official enUS pack)", res.Version)
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

func (m *Manager) setPhase(phase, url string) {
	m.setSource(phase, url)
}

func (m *Manager) addAttempt(source string, ok bool, errMsg string, bytes int64) {
	m.mu.Lock()
	m.progress.Attempts = append(m.progress.Attempts, Attempt{Source: source, OK: ok, Error: errMsg, Bytes: bytes})
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

// SourceInfo is one selectable download line for the UI.
type SourceInfo struct {
	ID   string `json:"id"`   // official|mirror:<name>
	Name string `json:"name"`
	URL  string `json:"url"`
}

// AvailableSources lists official + mirror lines for the data page.
func (m *Manager) AvailableSources(res *Resource) []SourceInfo {
	out := []SourceInfo{{ID: ModeOfficial, Name: "官方 GitHub", URL: res.URL}}
	for _, mm := range m.Mirrors.ClientDataMirrors {
		out = append(out, SourceInfo{ID: "mirror:" + mm.Name, Name: mm.Name, URL: mm.Fill(res.Version, res.Filename)})
	}
	return out
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
// subsequent Download run applies the SAME integrity rules as network
// downloads (size + pinned SHA-256 = official enUS pack guarantee).
func (m *Manager) ScanManualDir(res *Resource) (string, error) {
	cands := m.ManualCandidates(res)
	if len(cands) == 0 {
		return "", fmt.Errorf("未在 %s 找到 Data.zip。请放入官方 enUS 服务端数据包（wowgaming/client-data 发布的 Data.zip）；中文客户端的 Data/zhCN 目录不能替代",
			filepath.Join(m.paths.Downloads(), "manual"))
	}
	for _, p := range cands {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if res.SizeBytes > 0 && st.Size() != res.SizeBytes {
			return "", fmt.Errorf("文件大小不符: %s 为 %d 字节（期望 %d）。请下载与 Core 版本匹配的官方 enUS Data.zip",
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

// jsonUnmarshal / jsonMarshal are tiny wrappers keeping import lists tidy.
func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
func jsonMarshal(v any) ([]byte, error)     { return json.Marshal(v) }

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
