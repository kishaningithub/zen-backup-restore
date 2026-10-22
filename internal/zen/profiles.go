// Package zen knows where Zen Browser keeps its profiles and how to read
// and update profiles.ini.
package zen

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"
)

// Profile is a single Zen profile entry.
type Profile struct {
	Name    string // display name from profiles.ini (or dir name)
	Path    string // absolute path to the profile directory
	Default bool
}

// CandidateRoots returns the directories where Zen may store profiles.ini
// for the current OS, in priority order.
func CandidateRoots() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "zen"),
			filepath.Join(home, "Library", "Application Support", "Zen Browser"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{
			filepath.Join(appData, "zen"),
			filepath.Join(appData, "Zen Browser"),
		}
	default:
		var roots []string
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			roots = append(roots, filepath.Join(xdg, "zen"))
		}
		return append(roots,
			filepath.Join(home, ".zen"),
			filepath.Join(home, ".config", "zen"),
			filepath.Join(home, ".var", "app", "app.zen_browser.zen", ".zen"),
			filepath.Join(home, ".var", "app", "app.zen_browser.zen", "zen"),
			filepath.Join(home, ".var", "app", "io.github.zen_browser.zen", ".zen"),
			filepath.Join(home, "snap", "zen-browser", "common", ".zen"),
			filepath.Join(home, ".local", "share", "zen"),
		)
	}
}

// FindRoot returns the first candidate root that contains a profiles.ini.
func FindRoot() (string, error) {
	root, ok := lo.Find(CandidateRoots(), func(r string) bool {
		return fileExists(filepath.Join(r, "profiles.ini"))
	})
	if ok {
		return root, nil
	}
	return "", fmt.Errorf("could not find Zen's profiles.ini in any of:\n  %s\nuse --zen-dir to point at it",
		strings.Join(CandidateRoots(), "\n  "))
}

type iniSection struct {
	name string
	kv   map[string]string
}

func parseINI(path string) ([]iniSection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []iniSection
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			out = append(out, iniSection{name: line[1 : len(line)-1], kv: map[string]string{}})
		case len(out) > 0:
			if k, v, ok := strings.Cut(line, "="); ok {
				out[len(out)-1].kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out, sc.Err()
}

func resolve(root, p, isRelative string) string {
	// installs.ini has no IsRelative flag and uses absolute paths for
	// profiles outside the data dir (e.g. Scoop's persist folder).
	if isRelative == "1" && !filepath.IsAbs(p) {
		return filepath.Join(root, filepath.FromSlash(p))
	}
	return filepath.Clean(p)
}

// ListProfiles reads profiles.ini (and installs.ini for the default) under root.
func ListProfiles(root string) ([]Profile, error) {
	secs, err := parseINI(filepath.Join(root, "profiles.ini"))
	if err != nil {
		return nil, err
	}
	// The per-install default (installs.ini / [Install...] sections) wins
	// over the legacy Default=1 flag.
	installDefault := ""
	install, ok := lo.Find(secs, func(s iniSection) bool {
		return strings.HasPrefix(s.name, "Install") && s.kv["Default"] != ""
	})
	if !ok {
		isecs, _ := parseINI(filepath.Join(root, "installs.ini"))
		install, ok = lo.Find(isecs, func(s iniSection) bool { return s.kv["Default"] != "" })
	}
	if ok {
		installDefault = resolve(root, install.kv["Default"], "1")
	}

	profiles := lo.FilterMap(secs, func(s iniSection, _ int) (Profile, bool) {
		if !strings.HasPrefix(s.name, "Profile") || s.kv["Path"] == "" {
			return Profile{}, false
		}
		path := resolve(root, s.kv["Path"], s.kv["IsRelative"])
		return Profile{
			Name:    lo.Ternary(s.kv["Name"] != "", s.kv["Name"], filepath.Base(path)),
			Path:    path,
			Default: lo.Ternary(installDefault != "", path == installDefault, s.kv["Default"] == "1"),
		}, true
	})
	// Default profile first; the rest keep their profiles.ini order.
	defaults, others := lo.FilterReject(profiles, func(p Profile, _ int) bool { return p.Default })
	return append(defaults, others...), nil
}

// FindProfile matches query against profile name, directory name or path.
func FindProfile(profiles []Profile, query string) (Profile, error) {
	if abs, err := filepath.Abs(query); err == nil && dirExists(abs) {
		if p, ok := lo.Find(profiles, func(p Profile) bool { return p.Path == abs }); ok {
			return p, nil
		}
		return Profile{Name: filepath.Base(abs), Path: abs}, nil
	}
	matches := lo.Filter(profiles, func(p Profile, _ int) bool {
		return strings.EqualFold(p.Name, query) || filepath.Base(p.Path) == query
	})
	switch len(matches) {
	case 0:
		return Profile{}, fmt.Errorf("no profile matching %q", query)
	case 1:
		return matches[0], nil
	default:
		return Profile{}, fmt.Errorf("%q is ambiguous; pass the profile directory path instead", query)
	}
}

// NewProfileDir returns a fresh, unused directory path under root/Profiles
// following Firefox's "<salt>.<name>" convention.
func NewProfileDir(root, name string) (string, error) {
	charset := slices.Concat(lo.LowerCaseLettersCharset, lo.NumbersCharset)
	for range 10 {
		dir := filepath.Join(root, "Profiles", lo.RandomString(8, charset)+"."+name)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return dir, nil
		}
	}
	return "", errors.New("could not pick a unique profile directory")
}

// RegisterProfile appends a [ProfileN] section to profiles.ini.
func RegisterProfile(root, name, dir string) error {
	iniPath := filepath.Join(root, "profiles.ini")
	secs, err := parseINI(iniPath)
	if err != nil {
		return err
	}
	indexes := lo.FilterMap(secs, func(s iniSection, _ int) (int, bool) {
		n, err := strconv.Atoi(strings.TrimPrefix(s.name, "Profile"))
		return n, err == nil && strings.HasPrefix(s.name, "Profile")
	})
	next := lo.Ternary(len(indexes) == 0, 0, lo.Max(indexes)+1)

	path, isRelative := dir, "0"
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		path, isRelative = filepath.ToSlash(rel), "1"
	}

	data, err := os.ReadFile(iniPath)
	if err != nil {
		return err
	}
	// Match the file's existing line endings (CRLF on Windows).
	content := string(data)
	nl := lo.Ternary(strings.Contains(content, "\r\n"), "\r\n", "\n")
	lines := []string{"", fmt.Sprintf("[Profile%d]", next), "Name=" + name, "IsRelative=" + isRelative, "Path=" + path, ""}
	if content != "" && !strings.HasSuffix(content, "\n") {
		lines = append([]string{""}, lines...)
	}
	section := strings.Join(lines, nl)
	f, err := os.OpenFile(iniPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(section); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Version returns the Zen version that last used the profile, if known.
func Version(profileDir string) string {
	secs, err := parseINI(filepath.Join(profileDir, "compatibility.ini"))
	if err != nil {
		return ""
	}
	section, _ := lo.Find(secs, func(s iniSection) bool { return s.kv["LastVersion"] != "" })
	// LastVersion looks like "1.23.4b_20261020090730/20261020090730".
	version, _, _ := strings.Cut(section.kv["LastVersion"], "_")
	return version
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
