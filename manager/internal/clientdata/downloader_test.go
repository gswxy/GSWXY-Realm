package clientdata

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/mirrors"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// buildZip builds an in-memory Data.zip containing the required dirs.
func buildZip(t *testing.T, dirs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, d := range dirs {
		f, err := zw.Create(d + "/README")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("data"))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type testEnv struct {
	m   *Manager
	res *Resource
}

// newEnv builds a Manager whose official URL points at the given server.
func newEnv(t *testing.T, officialURL string, mirrorList []mirrors.ClientDataMirror, zipData []byte) *testEnv {
	t.Helper()
	root, err := os.MkdirTemp("", "gsrm-dl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	paths := platform.Paths{
		AppName: "test", AppDest: filepath.Join(root, "target"),
		Var: filepath.Join(root, "var"), Etc: filepath.Join(root, "etc"),
		Tmp: filepath.Join(root, "tmp"),
	}
	for _, d := range []string{paths.Downloads(), paths.ClientData(), filepath.Join(paths.Downloads(), "manual")} {
		_ = os.MkdirAll(d, 0o755)
	}
	log, err := logging.New(filepath.Join(root, "dl.log"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mirrors.Config{ClientDataMirrors: mirrorList}
	m := NewManager(paths, log, cfg)
	res := &Resource{
		Version: "v20.0", Label: "AC Data v20 enUS",
		Filename:  "Data.zip",
		SizeBytes: int64(len(zipData)),
		SHA256:    sha256Hex(zipData),
		Requires:  []string{"dbc", "maps"},
		URL:       officialURL,
	}
	return &testEnv{m: m, res: res}
}

// waitDone polls until the background job finishes; returns final progress.
func (e *testEnv) waitDone(t *testing.T, timeout time.Duration) Progress {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p := e.m.ProgressSnapshot()
		if !p.Active {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("download job did not finish in time")
	return Progress{}
}

func requireInstalled(t *testing.T, e *testEnv) {
	t.Helper()
	for _, d := range e.res.Requires {
		if _, err := os.Stat(filepath.Join(e.m.paths.ClientData(), d)); err != nil {
			t.Fatalf("required dir %s missing after install: %v", d, err)
		}
	}
}

// serveZip serves zipData with correct Range support (official-like).
func serveZip(t *testing.T, zipData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if strings.HasPrefix(rng, "bytes=") && !strings.Contains(rng, ",") {
			var start, end int64
			if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err == nil {
				if end >= int64(len(zipData)) || end < 0 {
					end = int64(len(zipData)) - 1
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(zipData)))
				w.Header().Set("Content-Type", "application/zip")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(zipData[start : end+1])
				return
			}
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(zipData)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	}))
}

// 正常：官方可用时 auto 直接走官方并完成安装。
func TestDownloadAutoOfficialOK(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	srv := serveZip(t, zipData)
	t.Cleanup(srv.Close)
	e := newEnv(t, srv.URL, nil, zipData)
	if err := e.m.DownloadMode(e.res, "", ModeAuto); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("download failed: %s", p.Error)
	}
	requireInstalled(t, e)
	if p.Source != "官方 GitHub" {
		t.Fatalf("source = %q", p.Source)
	}
}

// 换源：官方 403、镜像1 404、镜像2 可用 → 自动轮换并记录每线原因。
func TestDownloadRotatesOnHTTPErrors(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	good := serveZip(t, zipData)
	t.Cleanup(good.Close)
	mir := []mirrors.ClientDataMirror{
		{Name: "坏镜像404", URL: "http://127.0.0.1:1/m404"}, // 连接失败
		{Name: "好镜像", URL: good.URL + "/x"},
	}
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // 403
	}))
	t.Cleanup(official.Close)

	e := newEnv(t, official.URL, mir, zipData)
	// mirror 模式跳过探测，直接按序尝试线路
	if err := e.m.DownloadMode(e.res, "", ModeMirror); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("mirror rotation failed: %s (attempts=%+v)", p.Error, p.Attempts)
	}
	requireInstalled(t, e)
	if len(p.Attempts) < 2 {
		t.Fatalf("attempts not recorded: %+v", p.Attempts)
	}
	var okFound, failFound bool
	for _, a := range p.Attempts {
		if a.OK {
			okFound = true
		} else if a.Error != "" {
			failFound = true
		}
	}
	if !okFound || !failFound {
		t.Fatalf("attempts must record failure and success: %+v", p.Attempts)
	}
}

// 坏哈希绝不缓存：线路返回错误内容 → 删除文件换下一线路，最终内容正确。
func TestDownloadSHAMismatchNotCached(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	bad := serveZip(t, append(buildZip(t, "dbc"), []byte("tampered")...)) // 大小不同
	t.Cleanup(bad.Close)
	good := serveZip(t, zipData)
	t.Cleanup(good.Close)

	e := newEnv(t, "http://127.0.0.1:1/unused-official", []mirrors.ClientDataMirror{
		{Name: "坏内容线路", URL: bad.URL},
		{Name: "好镜像", URL: good.URL},
	}, zipData)
	if err := e.m.DownloadMode(e.res, "", ModeMirror); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("should recover via second source: %s", p.Error)
	}
	requireInstalled(t, e)
	// 错误线路的产物必须已被删除，不得残留
	final := filepath.Join(e.m.paths.Downloads(), e.res.Filename)
	if _, err := os.Stat(final); err == nil {
		if got, _ := checksumFile(final); got != e.res.SHA256 {
			t.Fatal("corrupt file left on disk")
		}
	}
}

// Range 不兼容：服务器忽略 Range 返回 200 → 放弃续传从头接收，成品正确。
func TestDownloadRangeIncompatibleRestarts(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK) // 永远 200 全量
		_, _ = w.Write(zipData)
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t, srv.URL, nil, zipData)
	// 预置同线路的半成品 .part（内容为垃圾），模拟需要续传
	part := filepath.Join(e.m.paths.Downloads(), "Data.zip.part")
	_ = os.WriteFile(part, []byte("half-part-garbage"), 0o640)
	_ = os.WriteFile(part+".src", []byte(srv.URL), 0o600)

	if err := e.m.DownloadMode(e.res, "", ModeOfficial); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("restart-from-scratch failed: %s", p.Error)
	}
	requireInstalled(t, e)
}

// 跨线路续传防护：.part 属于线路 A，切到线路 B 必须丢弃半成品重下。
func TestDownloadCrossSourceResumeRejected(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	srv := serveZip(t, zipData)
	t.Cleanup(srv.Close)
	e := newEnv(t, "http://127.0.0.1:1/official-dead", nil, zipData)
	part := filepath.Join(e.m.paths.Downloads(), "Data.zip.part")
	_ = os.WriteFile(part, []byte("from-source-A"), 0o640)
	_ = os.WriteFile(part+".src", []byte("http://source-a/"), 0o600)

	e.m.Mirrors = mirrors.Config{ClientDataMirrors: []mirrors.ClientDataMirror{{Name: "线路B", URL: srv.URL}}}
	if err := e.m.DownloadMode(e.res, "", ModeMirror); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("cross-source resume handling failed: %s", p.Error)
	}
	requireInstalled(t, e)
}

// HTML 错误页检测：200 但返回网页 → 判错换源。
func TestDownloadHTMLErrorPageRejected(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>404 not found page</body></html>"))
	}))
	t.Cleanup(bad.Close)
	good := serveZip(t, zipData)
	t.Cleanup(good.Close)

	e := newEnv(t, "http://127.0.0.1:1/unused-official", []mirrors.ClientDataMirror{
		{Name: "网页错误页", URL: bad.URL},
		{Name: "好镜像", URL: good.URL},
	}, zipData)
	if err := e.m.DownloadMode(e.res, "", ModeMirror); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("html page must rotate to next source: %s", p.Error)
	}
	requireInstalled(t, e)
	found := false
	for _, a := range p.Attempts {
		if !a.OK && strings.Contains(a.Error, "网页") {
			found = true
		}
	}
	if !found {
		t.Fatalf("html error not reported: %+v", p.Attempts)
	}
}

// 停滞看门狗：发一头字节后挂死 → 超时中断换源成功。
func TestDownloadStallWatchdog(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 发出头 + 首块字节并 Flush，然后永久挂住 —— 客户端会卡在
		// 正文读取阶段，触发"无数据传输"看门狗（而非响应头超时）。
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData[:64])
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)
	good := serveZip(t, zipData)
	t.Cleanup(good.Close)

	savedStall := stallTimeout
	stallTimeout = 700 * time.Millisecond
	t.Cleanup(func() { stallTimeout = savedStall })

	e := newEnv(t, "http://127.0.0.1:1/unused-official", []mirrors.ClientDataMirror{
		{Name: "停滞线路", URL: stalled.URL},
		{Name: "好镜像", URL: good.URL},
	}, zipData)
	if err := e.m.DownloadMode(e.res, "", ModeMirror); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 60*time.Second)
	if p.Error != "" {
		t.Fatalf("stall must rotate: %s", p.Error)
	}
	requireInstalled(t, e)
	stalledNote := false
	for _, a := range p.Attempts {
		if !a.OK && strings.Contains(a.Error, "停滞") {
			stalledNote = true
		}
	}
	if !stalledNote {
		t.Fatalf("stall not recorded: %+v", p.Attempts)
	}
}

// 206 Content-Range 总长与预期不符（疑似不同文件）→ 拒绝该线路。
// 该路径只在续传时触发：预置同线路半成品，令其走 Range 请求。
func TestDownloadContentRangeTotalMismatch(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	wrongTotal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if rng == "" {
			// 无 Range（探测/全新下载）路径不触发本用例，交给长度检查
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipData)
			return
		}
		var start int64
		fmt.Sscanf(rng, "bytes=%d-", &start)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, int64(len(zipData))-1, int64(len(zipData))+999))
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(zipData[start:])
	}))
	t.Cleanup(wrongTotal.Close)
	good := serveZip(t, zipData)
	t.Cleanup(good.Close)

	e := newEnv(t, "http://127.0.0.1:1/unused-official", []mirrors.ClientDataMirror{
		{Name: "总长不符线路", URL: wrongTotal.URL},
		{Name: "好镜像", URL: good.URL},
	}, zipData)
	// 预置"总长不符线路"的半成品，强制下一次请求带 Range
	part := filepath.Join(e.m.paths.Downloads(), "Data.zip.part")
	_ = os.WriteFile(part, zipData[:32], 0o640)
	_ = os.WriteFile(part+".src", []byte(wrongTotal.URL), 0o600)

	if err := e.m.DownloadMode(e.res, "", ModeMirror); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("mismatch must rotate: %s", p.Error)
	}
	requireInstalled(t, e)
	mismatch := false
	for _, a := range p.Attempts {
		if !a.OK && strings.Contains(a.Error, "不同文件") {
			mismatch = true
		}
	}
	if !mismatch {
		t.Fatalf("content-range mismatch not recorded: %+v", p.Attempts)
	}
}

// 全线路不可用：错误信息必须引导本地导入，而不是死循环或崩溃。
func TestDownloadAllSourcesFail(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	e := newEnv(t, "http://127.0.0.1:1/dead", []mirrors.ClientDataMirror{
		{Name: "死镜像", URL: "http://127.0.0.1:1/m"},
	}, zipData)
	if err := e.m.DownloadMode(e.res, "", ModeAuto); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 60*time.Second)
	if p.Error == "" {
		t.Fatal("all-dead must surface an error")
	}
	if !strings.Contains(p.Error, "本地导入") {
		t.Fatalf("error must guide to manual import: %s", p.Error)
	}
}

// 本地导入走同一校验：哈希不对 → 拒绝且不缓存；正确 → 安装。
func TestManualImportSameIntegrityRules(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	e := newEnv(t, "http://127.0.0.1:1/x", nil, zipData)

	bad := filepath.Join(e.m.paths.Downloads(), "manual", "Data.zip")
	_ = os.WriteFile(bad, append(zipData, []byte("x")...), 0o644)
	if err := e.m.DownloadMode(e.res, bad, ModeManual); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error == "" || !strings.Contains(p.Error, "enUS") {
		t.Fatalf("bad manual file must be rejected with enUS guidance: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(e.m.paths.Downloads(), e.res.Filename)); err == nil {
		t.Fatal("rejected manual file must not be cached as the download")
	}

	// 正确文件
	_ = os.WriteFile(bad, zipData, 0o644)
	if err := e.m.DownloadMode(e.res, bad, ModeManual); err != nil {
		t.Fatal(err)
	}
	p = e.waitDone(t, 30*time.Second)
	if p.Error != "" {
		t.Fatalf("good manual import failed: %s", p.Error)
	}
	requireInstalled(t, e)
}

// 探测选线：官方探测失败、镜像成功 → auto 模式跳过官方先走镜像。
func TestProbeOrdersWorkingMirrorFirst(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	good := serveZip(t, zipData)
	t.Cleanup(good.Close)
	e := newEnv(t, "http://127.0.0.1:1/official-dead",
		[]mirrors.ClientDataMirror{{Name: "好镜像", URL: good.URL}}, zipData)

	if err := e.m.DownloadMode(e.res, "", ModeAuto); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 60*time.Second)
	if p.Error != "" {
		t.Fatalf("auto with dead official failed: %s", p.Error)
	}
	requireInstalled(t, e)
	if p.Source != "好镜像" {
		t.Fatalf("final source = %q, want 好镜像", p.Source)
	}
}

// 镜像内容被篡改（同大小不同哈希）→ 校验失败拒绝安装。
func TestMirrorTamperedSameSize(t *testing.T) {
	zipData := buildZip(t, "dbc", "maps")
	tampered := append([]byte{}, zipData...)
	tampered[len(tampered)-1] ^= 0xFF
	srv := serveZip(t, tampered)
	t.Cleanup(srv.Close)
	e := newEnv(t, srv.URL, nil, zipData)
	if err := e.m.DownloadMode(e.res, "", ModeOfficial); err != nil {
		t.Fatal(err)
	}
	p := e.waitDone(t, 30*time.Second)
	if p.Error == "" || !strings.Contains(p.Error, "全部下载线路失败") {
		t.Fatalf("tampered file must fail the job: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(e.m.paths.ClientData(), "dbc")); err == nil {
		t.Fatal("tampered archive must never be unpacked")
	}
}

var _ = context.Background
