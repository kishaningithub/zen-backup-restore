package backup

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// pathRewriter maps absolute paths from the machine a backup was taken on to
// the machine it is being restored on.
type pathRewriter struct {
	srcSep, dstSep string
	pairs          [][2]string // {old, new}, most specific first
	dstWindows     bool
}

func newPathRewriter(m Manifest, dstProfile, dstHome string) *pathRewriter {
	r := &pathRewriter{srcSep: "/", dstSep: string(filepath.Separator), dstWindows: runtime.GOOS == "windows"}
	if m.OS == "windows" {
		r.srcSep = `\`
	}
	if m.ProfilePath != "" {
		r.pairs = append(r.pairs, [2]string{m.ProfilePath, dstProfile})
	}
	if m.HomeDir != "" && dstHome != "" {
		r.pairs = append(r.pairs, [2]string{m.HomeDir, dstHome})
	}
	return r
}

// path rewrites a native absolute path. ok is false if nothing matched.
func (r *pathRewriter) path(s string) (string, bool) {
	for _, p := range r.pairs {
		old, nu := p[0], p[1]
		if s == old {
			return nu, true
		}
		if strings.HasPrefix(s, old+r.srcSep) {
			parts := strings.Split(s[len(old)+len(r.srcSep):], r.srcSep)
			return filepath.Join(append([]string{nu}, parts...)...), true
		}
	}
	return s, false
}

// fileURL rewrites file:// and jar:file:// URLs that point into a mapped path.
func (r *pathRewriter) fileURL(s string) (string, bool) {
	prefix := ""
	rest := s
	if strings.HasPrefix(rest, "jar:") {
		prefix, rest = "jar:", rest[4:]
	}
	if !strings.HasPrefix(rest, "file://") {
		return s, false
	}
	suffix := ""
	if i := strings.Index(rest, "!/"); i >= 0 {
		rest, suffix = rest[:i], rest[i:]
	}
	u, err := url.Parse(rest)
	if err != nil {
		return s, false
	}
	p := u.Path
	if r.srcSep == `\` {
		p = strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", `\`)
	}
	np, ok := r.path(p)
	if !ok {
		return s, false
	}
	np = filepath.ToSlash(np)
	if r.dstWindows {
		np = "/" + np
	}
	return prefix + (&url.URL{Scheme: "file", Path: np}).String() + suffix, true
}

func (r *pathRewriter) value(s string) (string, bool) {
	if v, ok := r.path(s); ok {
		return v, true
	}
	return r.fileURL(s)
}

// rewriteJSONFile rewrites every string value in a JSON document.
func (r *pathRewriter) rewriteJSONFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	changed := false
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				t[k] = walk(x)
			}
		case []any:
			for i, x := range t {
				t[i] = walk(x)
			}
		case string:
			if nv, ok := r.value(t); ok {
				changed = true
				return nv
			}
		}
		return v
	}
	doc = walk(doc)
	if !changed {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return os.WriteFile(path, bytes.TrimRight(buf.Bytes(), "\n"), 0o600)
}

var jsString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// rewritePrefsFile rewrites path-valued strings in prefs.js / user.js.
func (r *pathRewriter) rewritePrefsFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	unesc := strings.NewReplacer(`\\`, `\`, `\"`, `"`)
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	changed := false
	out := jsString.ReplaceAllFunc(data, func(m []byte) []byte {
		raw := unesc.Replace(string(m[1 : len(m)-1]))
		if nv, ok := r.value(raw); ok {
			changed = true
			return []byte(`"` + esc.Replace(nv) + `"`)
		}
		return m
	})
	if !changed {
		return nil
	}
	return os.WriteFile(path, out, 0o600)
}
