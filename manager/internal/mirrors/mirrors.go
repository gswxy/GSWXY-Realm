// Package mirrors loads the centralized mirror configuration shipped in
// the payload (mirrors.json). All China-network accelerators live here so
// they can be updated in one place; every line is verified at runtime
// (connectivity + content), dead ones are skipped automatically.
package mirrors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ClientDataMirror is one download line for the client data archive.
type ClientDataMirror struct {
	Name string `json:"name"`
	URL  string `json:"url"` // {version}/{filename} placeholders
}

// FPKMirror describes how to build a China-friendly download URL for the
// release FPK (verified at runtime before being offered in the UI).
type FPKMirror struct {
	Name         string `json:"name"`
	URL          string `json:"url"` // {tag}/{filename} placeholders
	verifiedNote string
}

// Config is the whole mirrors.json document.
type Config struct {
	ClientDataMirrors []ClientDataMirror `json:"client_data_mirrors"`
	APIMirrors        []string           `json:"github_api_mirrors"`
	FPKMirror         *FPKMirror         `json:"fpk_mirror"`
}

// Default is used when the payload file is missing (source builds).
func Default() Config {
	return Config{
		APIMirrors: []string{"https://gh-proxy.com/https://api.github.com"},
	}
}

// Load reads <appdest>/mirrors.json, falling back to defaults.
func Load(appDest string) Config {
	raw, err := os.ReadFile(filepath.Join(appDest, "mirrors.json"))
	if err != nil {
		return Default()
	}
	var c Config
	if json.Unmarshal(raw, &c) != nil {
		return Default()
	}
	// 只保留 https 线路：不接受降级或来路不明的协议。
	c.ClientDataMirrors = filterHTTPS(c.ClientDataMirrors)
	c.APIMirrors = httpsOnly(c.APIMirrors)
	if c.FPKMirror != nil && !strings.HasPrefix(c.FPKMirror.URL, "https://") {
		c.FPKMirror = nil
	}
	return c
}

func filterHTTPS(in []ClientDataMirror) []ClientDataMirror {
	out := make([]ClientDataMirror, 0, len(in))
	for _, m := range in {
		if strings.HasPrefix(m.URL, "https://") {
			out = append(out, m)
		}
	}
	return out
}

func httpsOnly(in []string) []string {
	var out []string
	for _, u := range in {
		if strings.HasPrefix(u, "https://") {
			out = append(out, u)
		}
	}
	return out
}

// Fill expands the {version}/{filename} placeholders of a client-data mirror.
func (m ClientDataMirror) Fill(version, filename string) string {
	r := strings.ReplaceAll(m.URL, "{version}", version)
	return strings.ReplaceAll(r, "{filename}", filename)
}

// Fill expands the {tag}/{filename} placeholders of the FPK mirror.
func (f FPKMirror) Fill(tag, filename string) string {
	r := strings.ReplaceAll(f.URL, "{tag}", tag)
	return strings.ReplaceAll(r, "{filename}", filename)
}
