// Package update checks GitHub Releases for newer GSWXY Realm versions.
// Read-only: it never downloads or executes anything — installation stays
// with the fnOS App Center.
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

	"github.com/gswxy/gswxy-realm/manager/internal/version"
)

const (
	cacheTTL       = 10 * time.Minute
	requestTimeout = 8 * time.Second
)

// repoAPI is a var so tests can stub the endpoint.
var repoAPI = "https://api.github.com/repos/gswxy/GSWXY-Realm/releases"

// Release is one candidate release (subset of the GitHub API payload).
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Result is what the WebUI renders.
type Result struct {
	Current     string `json:"current"`
	Channel     string `json:"channel"`           // stable | nightly (requested)
	Latest      string `json:"latest,omitempty"`  // latest matching version
	LatestNote  string `json:"latest_note,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	URL         string `json:"url,omitempty"`     // release page
	FPK         *Asset `json:"fpk,omitempty"`     // the x86_64 fpk asset
	SHA256      *Asset `json:"sha256,omitempty"`
	Available   bool   `json:"available"`         // latest > current
	CheckedAt   string `json:"checked_at"`
	Error       string `json:"error,omitempty"`   // network/parse failure (non-fatal)
}

// Checker caches results per channel.
type Checker struct {
	mu    sync.Mutex
	cache map[string]Result
}

func New() *Checker { return &Checker{cache: map[string]Result{}} }

// Check queries GitHub. channel "stable" only considers full releases;
// "nightly" additionally considers prereleases (never pushed as stable).
// Failures return a Result with Error set — checking for updates must not
// break server operation.
func (c *Checker) Check(ctx context.Context, current, channel string) Result {
	c.mu.Lock()
	if r, ok := c.cache[channel]; ok && time.Since(parseTime(r.CheckedAt)) < cacheTTL && r.Error == "" {
		c.mu.Unlock()
		r.Current = current
		r.Available = version.Compare(strings.TrimPrefix(r.Latest, "v"), strings.TrimPrefix(current, "v")) > 0
		return r
	}
	c.mu.Unlock()

	res := Result{Current: current, Channel: channel, CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoAPI+"?per_page=15", nil)
	if err != nil {
		res.Error = err.Error()
		return c.store(channel, res)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		res.Error = "网络请求失败: " + err.Error()
		return c.store(channel, res)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("GitHub API 返回 %d", resp.StatusCode)
		return c.store(channel, res)
	}
	var releases []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases); err != nil {
		res.Error = "解析版本信息失败: " + err.Error()
		return c.store(channel, res)
	}

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
		res.Error = "暂无可用版本信息"
		return c.store(channel, res)
	}
	res.Available = version.Compare(res.Latest, strings.TrimPrefix(current, "v")) > 0
	return c.store(channel, res)
}

func (c *Checker) store(channel string, r Result) Result {
	c.mu.Lock()
	c.cache[channel] = r
	c.mu.Unlock()
	return r
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
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
