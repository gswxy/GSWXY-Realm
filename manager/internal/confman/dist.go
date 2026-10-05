// Package confman implements the configuration centre:
// .conf.dist parsing, the three-layer model (upstream default / GSWXY
// recommended / user override) and generation of the final run configs.
package confman

import (
	"bufio"
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
	reSection = regexp.MustCompile(`^#+\s*([A-Za-z][A-Za-z0-9 _/&.-]{2,60})\s#*\s*$`)
	reSubHead = regexp.MustCompile(`^#\s+[A-Z][A-Z0-9 .&/-]{2,60}:?\s*$`)
)

// ParseDist parses an AzerothCore-style .conf.dist file.
func ParseDist(path string) (*Schema, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := &Schema{File: filepath.Base(path), byKey: map[string]*Entry{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var comment []string
	section := "General"
	// Track previous non-comment line to decide whether a comment block
	// belongs to the setting that follows (standard AC layout).
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || trimmed == "#":
			continue
		case strings.HasPrefix(trimmed, "#"):
			// Section headers: solid # border lines then a title.
			if m := reSection.FindStringSubmatch(strings.TrimLeft(line, "# \t")); m != nil &&
				strings.Count(strings.TrimSpace(line), "#") > 3 {
				sec := strings.TrimSpace(m[1])
				if looksLikeSection(sec) {
					section = sec
					continue
				}
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

// looksLikeSection filters comment prose that pattern-matches a header.
func looksLikeSection(s string) bool {
	if strings.Contains(s, "=") || strings.Contains(s, ".") && !strings.Contains(s, " ") {
		return false
	}
	upper := 0
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r == ' ' || r == '.' || r == '_' || r == '-' {
			upper++
		}
	}
	return upper*10 > len([]rune(s))*7 // mostly uppercase/punct
}

func unquote(v string) string {
	v = strings.TrimSuffix(strings.TrimPrefix(v, "\""), "\"")
	return v
}

func inferType(v string) string {
	switch v {
	case "0", "1", "true", "false", "True", "False", "yes", "no", "Yes", "No":
		return "bool"
	}
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
