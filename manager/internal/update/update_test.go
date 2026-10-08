package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stubGitHub(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	saved := repoAPI
	repoAPI = srv.URL
	t.Cleanup(func() { repoAPI = saved })
}

// stable 通道必须跳过 prerelease（nightly 绝不冒充稳定版推送）。
func TestStableSkipsPrereleases(t *testing.T) {
	stubGitHub(t, `[
	 {"tag_name":"v1.1.0-nightly.7","prerelease":true,"html_url":"u1","body":"n1","assets":[{"name":"x.fpk","size":1,"browser_download_url":"d1"}]},
	 {"tag_name":"v1.0.2","prerelease":false,"html_url":"u2","body":"stable notes","assets":[{"name":"GSWXY-Realm-1.0.2-x86_64.fpk","size":9,"browser_download_url":"d2"}]}
	]`)
	c := New()
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
	]`)
	c := New()
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
	stubGitHub(t, `[{"tag_name":"v1.0.2","prerelease":false,"html_url":"u","body":"","assets":[]}]`)
	c := New()
	if res := c.Check(context.Background(), "1.0.2", "stable"); res.Available {
		t.Fatal("same version flagged as update")
	}
	if res := c.Check(context.Background(), "2.0.0", "stable"); res.Available {
		t.Fatal("older install flagged as update (latest 1.0.2 < current 2.0.0)")
	}
}

// 网络失败只返回 Error，不 panic、不影响调用方。
func TestNetworkFailureIsNonFatal(t *testing.T) {
	saved := repoAPI
	repoAPI = "http://127.0.0.1:1/releases"
	t.Cleanup(func() { repoAPI = saved })
	c := New()
	res := c.Check(context.Background(), "1.0.0", "stable")
	if res.Error == "" {
		t.Fatal("network failure must surface as Error")
	}
	if res.Available {
		t.Fatal("failed check must not claim update available")
	}
}
