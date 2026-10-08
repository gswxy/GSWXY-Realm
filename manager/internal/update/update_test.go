package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gswxy/gswxy-realm/manager/internal/mirrors"
)

func stubGitHub(t *testing.T, body string, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	saved := repoAPIBase
	repoAPIBase = srv.URL + "/repos/gswxy/GSWXY-Realm/releases"
	t.Cleanup(func() { repoAPIBase = saved })
	return srv
}

// stable 通道必须跳过 prerelease（nightly 绝不冒充稳定版推送）。
func TestStableSkipsPrereleases(t *testing.T) {
	stubGitHub(t, `[
	 {"tag_name":"v1.1.0-nightly.7","prerelease":true,"html_url":"u1","body":"n1","assets":[{"name":"x.fpk","size":1,"browser_download_url":"d1"}]},
	 {"tag_name":"v1.0.2","prerelease":false,"html_url":"u2","body":"stable notes","assets":[{"name":"GSWXY-Realm-1.0.2-x86_64.fpk","size":9,"browser_download_url":"d2"}]}
	]`, 0)
	c := New(mirrors.Config{})
	res := c.Check(context.Background(), "1.0.1", "stable")
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.Latest != "1.0.2" {
		t.Fatalf("stable latest = %q, want 1.0.2", res.Latest)
	}
	if !res.Available {
		t.Fatal("1.0.2 > 1.0.1 must be available")
	}
	if res.FPK == nil || !strings.HasSuffix(res.FPK.Name, "-x86_64.fpk") {
		t.Fatalf("fpk asset not picked: %+v", res.FPK)
	}
}

func TestNightlyChannelConsidersPrerelease(t *testing.T) {
	stubGitHub(t, `[
	 {"tag_name":"v1.1.0-nightly.7","prerelease":true,"html_url":"u1","body":"n","assets":[]}
	]`, 0)
	c := New(mirrors.Config{})
	res := c.Check(context.Background(), "1.1.0-nightly.5", "nightly")
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.Latest != "1.1.0-nightly.7" {
		t.Fatalf("nightly latest = %q", res.Latest)
	}
}

// 版本相同或更旧时不得提示"有更新"。
func TestNoFalsePositive(t *testing.T) {
	stubGitHub(t, `[{"tag_name":"v1.0.2","prerelease":false,"html_url":"u","body":"","assets":[]}]`, 0)
	c := New(mirrors.Config{})
	if res := c.Check(context.Background(), "1.0.2", "stable"); res.Available {
		t.Fatal("same version flagged as update")
	}
	if res := c.Check(context.Background(), "2.0.0", "stable"); res.Available {
		t.Fatal("older install flagged as update")
	}
}

// 网络失败只返回 Error（不 panic、不影响调用方），且绝不显示为已是最新。
func TestNetworkFailureIsNonFatal(t *testing.T) {
	saved := repoAPIBase
	repoAPIBase = "http://127.0.0.1:1/releases"
	t.Cleanup(func() { repoAPIBase = saved })
	c := New(mirrors.Config{})
	res := c.Check(context.Background(), "1.0.0", "stable")
	if res.Error == "" {
		t.Fatal("network failure must surface as Error")
	}
	if res.Available {
		t.Fatal("failed check must not claim update available")
	}
	// 负缓存：紧随其后的第二次检查不应再次发起网络请求（同结果返回）
	res2 := c.Check(context.Background(), "1.0.0", "stable")
	if res2.Error == "" || res2.Available {
		t.Fatalf("negative cache not applied: %+v", res2)
	}
}

// 官方 API 不通时回退到配置的镜像线路，成功后如实标注来源。
func TestMirrorFallbackWhenOfficialDown(t *testing.T) {
	// 官方 = 不可达端口
	saved := repoAPIBase
	repoAPIBase = "http://127.0.0.1:1/repos/gswxy/GSWXY-Realm/releases"
	t.Cleanup(func() { repoAPIBase = saved })

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.1.0","prerelease":false,"html_url":"mu","body":"via mirror",
		  "assets":[{"name":"GSWXY-Realm-1.1.0-x86_64.fpk","size":123,"browser_download_url":"https://github.com/gswxy/GSWXY-Realm/releases/download/v1.1.0/GSWXY-Realm-1.1.0-x86_64.fpk"}]}]`)
	}))
	t.Cleanup(mirror.Close)

	cfg := mirrors.Config{APIMirrors: []string{mirror.URL + "/api.github.com"}}
	c := New(cfg)
	res := c.Check(context.Background(), "1.0.0", "stable")
	if res.Error != "" {
		t.Fatalf("mirror fallback failed: %s", res.Error)
	}
	if res.Latest != "1.1.0" || !res.Available {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Via, "镜像") {
		t.Fatalf("via = %q, want mirror label", res.Via)
	}
}

// 国内 FPK 镜像：仅当 HEAD 实测存在且大小与官方资产一致时才提供。
func TestFPKMirrorVerifiedOnly(t *testing.T) {
	const size = int64(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			fmt.Fprint(w, fmt.Sprintf(`[{"tag_name":"v1.1.0","prerelease":false,"html_url":"u","body":"","assets":[{"name":"GSWXY-Realm-1.1.0-x86_64.fpk","size":%d,"browser_download_url":"d"}]}]`, size))
		default: // 镜像 HEAD 校验路径
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
				w.WriteHeader(200)
				return
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	saved := repoAPIBase
	repoAPIBase = srv.URL + "/releases"
	t.Cleanup(func() { repoAPIBase = saved })

	cfg := mirrors.Config{FPKMirror: &mirrors.FPKMirror{Name: "测试镜像", URL: srv.URL + "/dl/{tag}/{filename}"}}
	c := New(cfg)
	res := c.Check(context.Background(), "1.0.0", "stable")
	if res.FPKMirror == nil || !res.FPKMirror.Verified || res.FPKMirror.Size != size {
		t.Fatalf("verified mirror expected: %+v", res.FPKMirror)
	}

	// 镜像不可用（404）时不得返回任何未验证按钮
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			fmt.Fprint(w, fmt.Sprintf(`[{"tag_name":"v1.1.0","prerelease":false,"html_url":"u","body":"","assets":[{"name":"GSWXY-Realm-1.1.0-x86_64.fpk","size":%d,"browser_download_url":"d"}]}]`, size))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv2.Close)
	repoAPIBase = srv2.URL + "/releases"
	cfg2 := mirrors.Config{FPKMirror: &mirrors.FPKMirror{Name: "坏镜像", URL: srv2.URL + "/dl/{tag}/{filename}"}}
	res2 := New(cfg2).Check(context.Background(), "1.0.0", "stable")
	if res2.FPKMirror != nil {
		t.Fatalf("unverified mirror must not be offered: %+v", res2.FPKMirror)
	}
	if res2.FPK == nil {
		t.Fatal("official asset must remain available")
	}
}
