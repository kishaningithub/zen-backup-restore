package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExcluded(t *testing.T) {
	cases := []struct {
		rel        string
		full, slim bool
	}{
		{"places.sqlite", false, false},
		{"places.sqlite-wal", false, false},
		{"places.sqlite-shm", true, true},
		{"storage/default/moz-extension+++abc/idb/x.sqlite-shm", true, true},
		{".parentlock", true, true},
		{"compatibility.ini", true, true},
		{"cache2/entries/x", true, true},
		{"gmp-widevinecdm/4.10/lib.dylib", true, true},
		{"storage/temporary/x", true, true},
		{"storage/default/https+++x.com^userContextId=6/ls/data.sqlite", false, true},
		{"storage/default/moz-extension+++abc/idb/x.sqlite", false, false},
		{"zen-sessions-backup/clean.jsonlz4", false, true},
		{"sessionstore-backups/recovery.jsonlz4", false, false},
		{"sessionstore-backups/upgrade.jsonlz4-20240101", false, true},
		{"chrome/userChrome.css", false, false},
	}
	for _, c := range cases {
		if got := Excluded(c.rel, Options{}); got != c.full {
			t.Errorf("Excluded(%q, full) = %v, want %v", c.rel, got, c.full)
		}
		if got := Excluded(c.rel, Options{Slim: true}); got != c.slim {
			t.Errorf("Excluded(%q, slim) = %v, want %v", c.rel, got, c.slim)
		}
	}
}

func TestPathRewriterFromWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cross-OS case")
	}
	m := Manifest{OS: "windows", HomeDir: `C:\Users\old`, ProfilePath: `C:\Users\old\AppData\Roaming\zen\Profiles\ab.Default (release)`}
	r := newPathRewriter(m, "/home/new/.zen/xy.Restored", "/home/new")

	got, ok := r.value(`C:\Users\old\AppData\Roaming\zen\Profiles\ab.Default (release)\extensions\a@b.xpi`)
	if !ok || got != "/home/new/.zen/xy.Restored/extensions/a@b.xpi" {
		t.Errorf("profile path: %q %v", got, ok)
	}
	got, ok = r.value(`C:\Users\old\Downloads`)
	if !ok || got != "/home/new/Downloads" {
		t.Errorf("home path: %q %v", got, ok)
	}
	got, ok = r.value("jar:file:///C:/Users/old/AppData/Roaming/zen/Profiles/ab.Default%20(release)/extensions/%7Bid%7D.xpi!/")
	if !ok || got != "jar:file:///home/new/.zen/xy.Restored/extensions/%7Bid%7D.xpi!/" {
		t.Errorf("jar url: %q %v", got, ok)
	}
	if _, ok := r.value("https://example.com"); ok {
		t.Error("unrelated string rewritten")
	}
}

func TestRoundTripRewritesPaths(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src", "ab.Old Profile")
	home, _ := os.UserHomeDir()
	files := map[string]string{
		"places.sqlite":         "db",
		"places.sqlite-wal":     "wal",
		"places.sqlite-shm":     "shm",
		".parentlock":           "",
		"compatibility.ini":     "[Compatibility]\nLastVersion=1.0b_1/1\n",
		"cache2/entries/x":      "cache",
		"chrome/userChrome.css": "a{}",
		"prefs.js": `user_pref("browser.download.dir", "` + strings.ReplaceAll(filepath.Join(home, "Downloads"), `\`, `\\`) + `");` + "\n" +
			`user_pref("some.path", "` + strings.ReplaceAll(filepath.Join(src, "x"), `\`, `\\`) + `");` + "\n",
		"extensions.json": `{"addons":[{"path":` + quoteJSON(filepath.Join(src, "extensions", "a.xpi")) + `}]}`,
	}
	for name, body := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}

	archive := filepath.Join(tmp, "b.tar.gz")
	if _, err := Create(src, "Old", "1.0b", archive, Options{}, nil); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, "dst", "cd.New")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "stale"), []byte("x"), 0o644)

	res, err := Restore(archive, dst, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousDir == "" {
		t.Fatal("expected previous profile to be kept")
	}
	if _, err := os.Stat(filepath.Join(res.PreviousDir, "stale")); err != nil {
		t.Error("previous profile not preserved")
	}
	for _, want := range []string{"places.sqlite", "places.sqlite-wal", "chrome/userChrome.css"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(want))); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	for _, gone := range []string{"places.sqlite-shm", ".parentlock", "compatibility.ini", "cache2", "stale"} {
		if _, err := os.Stat(filepath.Join(dst, gone)); err == nil {
			t.Errorf("%s should not be restored", gone)
		}
	}
	ext, _ := os.ReadFile(filepath.Join(dst, "extensions.json"))
	if !strings.Contains(string(ext), quoteJSON(filepath.Join(dst, "extensions", "a.xpi"))) {
		t.Errorf("extensions.json not rewritten: %s", ext)
	}
	prefs, _ := os.ReadFile(filepath.Join(dst, "prefs.js"))
	if strings.Contains(string(prefs), "Old Profile") || strings.Contains(string(prefs), ".restoring-") {
		t.Errorf("prefs.js not rewritten: %s", prefs)
	}
}

func TestRestoreRejectsPathTraversal(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "evil.tar.gz")
	f, _ := os.Create(archive)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	write := func(name, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	write(ManifestName, `{"format_version":1}`)
	write("profile/../../escaped", "x")
	tw.Close()
	gz.Close()
	f.Close()

	dst := filepath.Join(tmp, "a", "profile")
	if _, err := Restore(archive, dst, false, nil); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "escaped")); err == nil {
		t.Fatal("file escaped the target directory")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("target should not exist after a failed restore")
	}
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
