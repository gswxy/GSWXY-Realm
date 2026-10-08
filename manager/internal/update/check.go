// Package update checks GitHub Releases for newer GSWXY Realm versions.
// Read-only: it never downloads or executes anything — installation stays
// with the fnOS App Center.
//
// 国内网络下 api.github.com 可能不可达：先直连，失败后按序尝试
// mirrors.json 配置的 API 代理线路；全部失败时如实报错（绝不把
// "检查失败"显示成"已是最新"），并做短负缓存避免反复冲击。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/mirrors"
	"github.com/gswxy/gswxy-realm/manager/internal/version"
)

const (
	repoAPI        = "https://api.github.com/repos/gswxy/GSWXY-Realm/releases"
	cacheTTL       = 10 * time.Minute // 成功缓存
	negCacheTTL    = 2 * time.Minute  // 失败负缓存（网络错误不视为"最新"）
	requestTimeout = 8 * time.Second
)

// repoAPIBase is a var so tests can stub the endpoint.
var repoAPIBase = repoAPI

// Release is one candidate release (subset of the GitHub API payload).
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"published_at"`
	HTMLURL     string  `json:"html_url"`
	Body        string  `json:"body"`
	Assets      []Asset `json:"assets"`
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// MirrorAsset is a China-friendly URL for an asset, offered ONLY after a
// live HEAD check confirms it exists with the exact same size.
type MirrorAsset struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Verified bool   `json:"verified"`
}

// Result is what the WebUI renders.
type Result struct {
	Current     string       `json:"current"`
	Channel     string       `json:"channel"`           // stable | nightly (requested)
	Latest      string       `json:"latest,omitempty"`  // latest matching version
	LatestNote  string       `json:"latest_note,omitempty"`
	PublishedAt string       `json:"published_at,omitempty"`
	URL         string       `json:"url,omitempty"` // release page
	FPK         *Asset       `json:"fpk,omitempty"` // the x86_64 fpk asset
	SHA256      *Asset       `json:"sha256,omitempty"`
	FPKMirror   *MirrorAsset `json:"fpk_mirror,omitempty"` // 国内线路（实测可用才返回）
	Via         string       `json:"via,omitempty"`        // 数据来源（官方/镜像名）
	Available   bool         `json:"available"` // latest > current
	CheckedAt   string       `json:"checked_at"`
	Error       string       `json:"error,omitempty"` // network/parse failure (non-fatal)
}

type cacheEntry struct {
	res     Result
	at      time.Time
	failed  bool
}

// Checker caches results per channel.
type Checker struct {
	mu    sync.Mutex
	cfg   mirrors.Config
	cache map[string]cacheEntry
}

func New(cfg mirrors.Config) *Checker {
	return &Checker{cfg: cfg, cache: map[string]cacheEntry{}}
}

// apiEndpoints returns the ordered API URL list: official first, then the
// configured accelerator lines (they proxy the full api.github.com path).
func (c *Checker) apiEndpoints() []string {
	eps := []string{repoAPIBase + "?per_page=15"}
	for _, m := range c.cfg.APIMirrors {
		base := strings.TrimSuffix(m, "/")
		base = strings.TrimSuffix(base, "/repos/gswxy/GSWXY-Realm/releases")
		if strings.Contains(base, "api.github.com") && !strings.HasSuffix(base, "api.github.com") {
			continue
		}
		if strings.HasSuffix(base, "api.github.com") {
			eps = append(eps, base+"/repos/gswxy/GSWXY-Realm/releases?per_page=15")
		}
	}
	return eps
}

// Check queries GitHub (direct, then configured accelerators). channel
// "stable" only considers full releases; "nightly" additionally considers
// prereleases (never pushed as stable). Failures return a Result with
// Error set — checking for updates must not break server operation, and
// must never be reported as "already up to date".
func (c *Checker) Check(ctx context.Context, current, channel string) Result {
	c.mu.Lock()
	if e, ok := c.cache[channel]; ok {
		fresh := time.Since(e.at) < (map[bool]time.Duration{true: negCacheTTL, false: cacheTTL})[e.failed]
		if fresh {
			c.mu.Unlock()
			res := e.res
			res.Current = current
			res.Available = res.Latest != "" &&
				version.Compare(strings.TrimPrefix(res.Latest, "v"), strings.TrimPrefix(current, "v")) > 0
			return res
		}
	}
	c.mu.Unlock()

	res := Result{Current: current, Channel: channel, CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	var releases []Release
	var lastErr error
	var via string
	for _, ep := range c.apiEndpoints() {
		rs, err := c.fetch(ctx, ep)
		if err == nil {
			releases, via = rs, sourceLabel(ep)
			break
		}
		lastErr = err
	}
	if releases == nil {
		res.Error = "GitHub 与镜像线路均不可达: " + lastErr.Error()
		res.Error += "（不影响服务器运行；国内网络可稍后重试或到 Releases 页面手动查看）"
		return c.store(channel, res, true)
	}
	res.Via = via

	for _, r := range releases {
		if channel == "stable" && r.Prerelease {
			continue // nightly 绝不冒充稳定版
		}
		res.Latest = strings.TrimPrefix(r.TagName, "v")
		res.LatestNote = firstParagraphs(r.Body)
		res.PublishedAt = r.PublishedAt
		res.URL = r.HTMLURL
		for _, a := range r.Assets {
			switch {
			case strings.HasSuffix(a.Name, "-x86_64.fpk"):
				fp := a
				res.FPK = &fp
			case strings.HasSuffix(a.Name, ".fpk.sha256"):
				s := a
				res.SHA256 = &s
			}
		}
		break
	}
	if res.Latest == "" {
		res.Error = "暂无可用版本信息（当前通道没有已发布的 Release）"
		return c.store(channel, res, false)
	}
	res.Available = version.Compare(res.Latest, strings.TrimPrefix(current, "v")) > 0

	// 国内下载线路：仅当配置了镜像且实测 HEAD 可用、大小与官方资产
	// 完全一致时才提供 —— 不显示任何未经验证的按钮。
	if res.FPK != nil && c.cfg.FPKMirror != nil {
		tag := "v" + res.Latest
		murl := c.cfg.FPKMirror.Fill(tag, res.FPK.Name)
		if ok, size := c.headCheck(ctx, murl); ok && size == res.FPK.Size {
			res.FPKMirror = &MirrorAsset{
				Name: c.cfg.FPKMirror.Name, URL: murl, Size: size, Verified: true,
			}
		} else {
			res.FPKMirror = nil
		}
	}
	return c.store(channel, res, false)
}

func sourceLabel(ep string) string {
	if strings.Contains(ep, "api.github.com") && !strings.Contains(strings.Split(ep, "://")[1], "/https") {
		if strings.HasPrefix(ep, repoAPIBase) {
			return "GitHub 官方 API"
		}
	}
	for _, m := range mirrors.Default().APIMirrors {
		if strings.Contains(ep, m) {
			return "镜像 API 线路"
		}
	}
	return "镜像 API 线路"
}

func (c *Checker) fetch(ctx context.Context, url string) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var releases []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// headCheck verifies a mirror URL really serves the file with the exact
// expected size (follows redirects; some proxies answer HEAD with 200 and
// no length — fall back to a 1-byte ranged GET to read Content-Range).
func (c *Checker) headCheck(ctx context.Context, url string) (bool, int64) {
	client := &http.Client{Timeout: requestTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, 0
	}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
			return true, resp.ContentLength
		}
	}
	// fallback: ranged GET for Content-Range total
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0
	}
	req2.Header.Set("Range", "bytes=0-0")
	resp2, err := client.Do(req2)
	if err != nil {
		return false, 0
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp2.Body, 1))
	if resp2.StatusCode != http.StatusPartialContent {
		return false, 0
	}
	cr := resp2.Header.Get("Content-Range")
	if i := strings.LastIndex(cr, "/"); i >= 0 {
		var size int64
		if _, err := fmt.Sscanf(cr[i+1:], "%d", &size); err == nil && size > 0 {
			return true, size
		}
	}
	return false, 0
}

func (c *Checker) store(channel string, r Result, failed bool) Result {
	c.mu.Lock()
	c.cache[channel] = cacheEntry{res: r, at: time.Now(), failed: failed}
	c.mu.Unlock()
	return r
}

// firstParagraphs trims release notes to a sane display size (markdown
// body kept as-is; the UI renders it as plain text).
func firstParagraphs(body string) string {
	body = strings.TrimSpace(body)
	if len(body) > 4000 {
		return body[:4000] + "\n…"
	}
	return body
}
