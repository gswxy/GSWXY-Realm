// Package confman implements the configuration centre:
// .conf.dist parsing, the three-layer model (upstream default / GSWXY
// recommended / user override) and generation of the final run configs.
package confman

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Entry is one parsed setting from a .conf.dist file.
type Entry struct {
	Key      string `json:"key"`
	Default  string `json:"default"`
	Type     string `json:"type"` // bool|int|float|string
	Section  string `json:"section"`
	Comment  string `json:"comment"`
	Restart  bool   `json:"restart"`
	Source   string `json:"source"` // conf file it came from
}

// Schema is the parsed representation of one .conf.dist.
type Schema struct {
	File       string             `json:"file"`
	Sections   []string           `json:"sections"`
	Entries    []*Entry           `json:"entries"`
	byKey      map[string]*Entry
}

var (
	reSetting = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9._]*)\s*=\s*(.*?)\s*$`)
	// reSubHead matches section title lines, both styles upstream ships:
	//   "#   SERVER SYSTEM SETTINGS"      (worldserver/authserver)
	//   "# GENERAL SETTINGS           #"  (playerbots box headers)
	reSubHead = regexp.MustCompile(`^#\s+([A-Z][A-Z0-9 .&/-]{2,60}?)\s*#*\s*$`)
)

// ParseDist parses an AzerothCore-style .conf.dist file.
//
// Section headers are the visual banners upstream ships:
//
//	####################################
//	#    SERVER SYSTEM SETTINGS
//	####################################
//
// i.e. an indented ALL-CAPS title line adjacent to a #### border. The
// SECTION INDEX blocks at the top of each file contain the same shape of
// lines WITHOUT border adjacency — those are a table of contents and must
// not be treated as headers.
func ParseDist(path string) (*Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")

	sc := &Schema{File: filepath.Base(path), byKey: map[string]*Entry{}}
	var comment []string
	section := "General"

	isBorder := func(s string) bool {
		t := strings.TrimSpace(s)
		return strings.HasPrefix(t, "##") && strings.Count(t, "#") >= 8
	}
	// isPadLine matches the decorative filler inside box headers
	// ("#                          #") and the lone "#" separators.
	isPadLine := func(s string) bool {
		t := strings.TrimSpace(s)
		return t == "#" || (strings.HasPrefix(t, "#") && strings.HasSuffix(t, "#") &&
			strings.Trim(t, "# \t") == "")
	}
	// underBorder reports whether the title line at i sits under a border,
	// allowing up to 3 decorative pad lines in between (box headers).
	underBorder := func(i int) bool {
		pads := 0
		for j := i - 1; j >= 0; j-- {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				continue
			}
			if isBorder(lines[j]) {
				return true
			}
			if isPadLine(lines[j]) && pads < 3 {
				pads++
				continue
			}
			return false
		}
		return false
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || trimmed == "#":
			continue
		case isBorder(line):
			// Borders only frame; they never reset the pending comment
			// block (a key's description ends right after a border).
			continue
		case isPadLine(line):
			continue
		case strings.HasPrefix(trimmed, "#"):
			// Real headers sit under a border (directly or through pad
			// lines); the SECTION INDEX table-of-contents lines do not.
			if m := reSubHead.FindStringSubmatch(line); m != nil && underBorder(i) {
				section = strings.TrimSpace(m[1])
				comment = comment[:0]
				continue
			}
			comment = append(comment, strings.TrimPrefix(trimmed, "#"))
		default:
			if m := reSetting.FindStringSubmatch(line); m != nil && !strings.HasPrefix(trimmed, "#") {
				key := strings.TrimSpace(m[1])
				if _, dup := sc.byKey[key]; dup {
					// Duplicated key: first declaration wins (AC behaviour).
					comment = comment[:0]
					continue
				}
				e := &Entry{
					Key:     key,
					Default: unquote(strings.TrimSpace(m[2])),
					Section: section,
					Comment: strings.TrimSpace(strings.Join(comment, "\n")),
					Source:  sc.File,
				}
				e.Type = inferType(e.Default)
				sc.Entries = append(sc.Entries, e)
				sc.byKey[key] = e
			}
			comment = comment[:0]
		}
	}
	seen := map[string]bool{}
	for _, e := range sc.Entries {
		if !seen[e.Section] {
			seen[e.Section] = true
			sc.Sections = append(sc.Sections, e.Section)
		}
	}
	return sc, nil
}

func unquote(v string) string {
	v = strings.TrimSuffix(strings.TrimPrefix(v, "\""), "\"")
	return v
}

func inferType(v string) string {
	switch v {
	case "true", "false", "True", "False", "yes", "no", "Yes", "No", "YES", "NO":
		return "bool"
	}
	// NOTE: "0"/"1" stay int — many numeric settings default to 1 (rates,
	// counts); treating them as bool would reject valid values like "5".
	if isInt(v) {
		return "int"
	}
	if isFloat(v) {
		return "float"
	}
	return "string"
}

func isInt(v string) bool {
	if v == "" {
		return false
	}
	i := 0
	if v[0] == '-' {
		i = 1
	}
	if i >= len(v) {
		return false
	}
	for ; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

func isFloat(v string) bool {
	if v == "" || !strings.Contains(v, ".") {
		return false
	}
	_, err := strconvParseFloat(v)
	return err == nil
}

func strconvParseFloat(v string) (float64, error) {
	var f float64
	var sign float64 = 1
	i := 0
	if v[0] == '-' {
		sign = -1
		i = 1
	}
	for ; i < len(v); i++ {
		if v[i] == '.' {
			continue
		}
		if v[i] < '0' || v[i] > '9' {
			return f, errParse
		}
		f = f*10 + float64(v[i]-'0')
	}
	return f * sign, nil
}

type parseErr string

func (e parseErr) Error() string { return string(e) }

const errParse = parseErr("not a float")

// SortedKeys returns entries sorted by section then key (for the UI).
func (s *Schema) SortedKeys() []*Entry {
	out := append([]*Entry(nil), s.Entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Get finds an entry by key.
func (s *Schema) Get(key string) (*Entry, bool) {
	e, ok := s.byKey[key]
	return e, ok
}
