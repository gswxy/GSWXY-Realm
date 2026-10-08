package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"v1.2.3", "1.2.3", 0},
		// semver: prerelease < release of the same version
		{"1.0.0-nightly", "1.0.0", -1},
		{"1.0.0", "1.0.0-nightly", 1},
		{"1.0.0-nightly.20261008", "1.0.0-nightly", 1},
		// unparseable falls back sensibly
		{"0.0.0-dev", "0.0.0-dev", 0},
		{"1.0.0", "garbage", 1},
		{"garbage", "1.0.0", -1},
		{"10.0.0", "9.0.0", 1},
		{"1.10.0", "1.9.0", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// nightly 不得被当作比 stable 更新的"稳定版"推送：1.0.2-nightly < 1.0.2。
func TestNightlyNeverBeatsStable(t *testing.T) {
	if Compare("1.0.2-nightly.5", "1.0.2") >= 0 {
		t.Fatal("prerelease must compare older than its release")
	}
}
