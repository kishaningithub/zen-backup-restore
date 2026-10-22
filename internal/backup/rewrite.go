package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/samber/lo"
)

// pathRewriter maps absolute paths from the machine a backup was taken on to
// the machine it is being restored on.
type pathRewriter struct {
	srcSep     string        // path separator on the source machine
	mappings   []pathMapping // most specific first
	dstWindows bool
}

type pathMapping struct {
	from, to string
}

func newPathRewriter(m Manifest, dstProfile, dstHome string) *pathRewriter {
	r := &pathRewriter{
		srcSep:     lo.Ternary(m.OS == "windows", `\`, "/"),
		dstWindows: runtime.GOOS == "windows",
	}
	if m.ProfilePath != "" {
		r.mappings = append(r.mappings, pathMapping{from: m.ProfilePath, to: dstProfile})
	}
	if m.HomeDir != "" && dstHome != "" {
		r.mappings = append(r.mappings, pathMapping{from: m.HomeDir, to: dstHome})
	}
	return r
}

// path rewrites a native absolute path. ok is false if nothing matched.
func (r *pathRewriter) path(s string) (string, bool) {
	for _, m := range r.mappings {
		if s == m.from {
			return m.to, true
		}
		if rest, found := strings.CutPrefix(s, m.from+r.srcSep); found {
			parts := strings.Split(rest, r.srcSep)
			return filepath.Join(append([]string{m.to}, parts...)...), true
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

// rewriteProfile rewrites paths in the prefs and JSON files of a profile.
func (r *pathRewriter) rewriteProfile(dir string) error {
	for _, name := range []string{"prefs.js", "user.js"} {
		if err := r.rewritePrefsFile(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("rewriting %s: %w", name, err)
		}
	}
	jsonFiles, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range jsonFiles {
		if err := r.rewriteJSONFile(f); err != nil {
			return fmt.Errorf("rewriting %s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

// rewriteJSONFile rewrites every string value in a JSON document.
func (r *pathRewriter) rewriteJSONFile(path string) error {
	data, exists, err := readFileIfExists(path)
	if err != nil || !exists {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	doc, changed := r.rewriteJSONValue(doc)
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

// rewriteJSONValue rewrites path strings anywhere inside a decoded JSON value.
func (r *pathRewriter) rewriteJSONValue(v any) (any, bool) {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			nv, c := r.rewriteJSONValue(x)
			t[k], changed = nv, changed || c
		}
	case []any:
		for i, x := range t {
			nv, c := r.rewriteJSONValue(x)
			t[i], changed = nv, changed || c
		}
	case string:
		if nv, ok := r.value(t); ok {
			return nv, true
		}
	}
	return v, changed
}

var jsString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// rewritePrefsFile rewrites path-valued strings in prefs.js / user.js.
func (r *pathRewriter) rewritePrefsFile(path string) error {
	data, exists, err := readFileIfExists(path)
	if err != nil || !exists {
		return err
	}
	unescape := strings.NewReplacer(`\\`, `\`, `\"`, `"`)
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	changed := false
	out := jsString.ReplaceAllFunc(data, func(quoted []byte) []byte {
		raw := unescape.Replace(string(quoted[1 : len(quoted)-1]))
		if nv, ok := r.value(raw); ok {
			changed = true
			return []byte(`"` + escape.Replace(nv) + `"`)
		}
		return quoted
	})
	if !changed {
		return nil
	}
	return os.WriteFile(path, out, 0o600)
}

func readFileIfExists(path string) (data []byte, exists bool, err error) {
	data, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return data, err == nil, err
}
