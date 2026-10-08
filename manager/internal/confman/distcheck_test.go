package confman

import (
	"os"
	"strings"
	"testing"
)

func TestRealUpstreamDists(t *testing.T) {
	for _, p := range []string{os.Getenv("WS_DIST"), os.Getenv("PB_DIST")} {
		if p == "" || !fileExists(p) {
			continue
		}
		sc, err := ParseDist(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		t.Logf("%s: %d entries, %d sections: %v", p, len(sc.Entries), len(sc.Sections), sc.Sections[:min(8, len(sc.Sections))])
		if len(sc.Sections) < 3 {
			t.Errorf("%s: too few sections parsed (%d)", p, len(sc.Sections))
		}
		general := 0
		for _, e := range sc.Entries {
			if e.Section == "General" {
				general++
			}
		}
		if general > len(sc.Entries)/2 {
			t.Errorf("%s: %d/%d entries stuck in General", p, general, len(sc.Entries))
		}
		withComment := 0
		for _, e := range sc.Entries {
			if strings.TrimSpace(e.Comment) != "" {
				withComment++
			}
		}
		t.Logf("%s: %d/%d entries have comments", p, withComment, len(sc.Entries))
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
