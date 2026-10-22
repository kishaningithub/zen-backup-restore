package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcluded(t *testing.T) {
	cases := []struct {
		rel                   string
		excludedByFull        bool
		excludedBySlim        bool
		excludedByNoSensitive bool
	}{
		{"places.sqlite", false, false, false},
		{"places.sqlite-wal", false, false, false},
		{"places.sqlite-shm", true, true, true},
		{"storage/default/moz-extension+++abc/idb/x.sqlite-shm", true, true, true},
		{".parentlock", true, true, true},
		{"compatibility.ini", true, true, true},
		{"cache2/entries/x", true, true, true},
		{"gmp-widevinecdm/4.10/lib.dylib", true, true, true},
		{"storage/temporary/x", true, true, true},
		{"storage/default/https+++x.com^userContextId=6/ls/data.sqlite", false, true, true},
		{"storage/default/moz-extension+++abc/idb/x.sqlite", false, false, false},
		{"zen-sessions-backup/clean.jsonlz4", false, true, false},
		{"sessionstore-backups/recovery.jsonlz4", false, false, false},
		{"sessionstore-backups/upgrade.jsonlz4-20240101", false, true, false},
		{"chrome/userChrome.css", false, false, false},
		{"logins.json", false, false, true},
		{"logins-backup.json", false, false, true},
		{"key4.db", false, false, true},
		{"cookies.sqlite", false, false, true},
		{"cookies.sqlite-wal", false, false, true},
		{"formhistory.sqlite", false, false, true},
		{"autofill-profiles.json", false, false, true},
		{"signedInUser.json", false, false, true},
		{"webappsstore.sqlite", false, false, true},
		{"cert9.db", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.rel, func(t *testing.T) {
			require.Equal(t, c.excludedByFull, Excluded(c.rel, Options{}), "full backup")
			require.Equal(t, c.excludedBySlim, Excluded(c.rel, Options{Slim: true}), "slim backup")
			require.Equal(t, c.excludedByNoSensitive, Excluded(c.rel, Options{ExcludeSensitive: true}), "exclude-sensitive backup")
		})
	}
}

func TestPathRewriterFromWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cross-OS case")
	}
	m := Manifest{OS: "windows", HomeDir: `C:\Users\old`, ProfilePath: `C:\Users\old\AppData\Roaming\zen\Profiles\ab.Default (release)`}
	r := newPathRewriter(m, "/home/new/.zen/xy.Restored", "/home/new")

	cases := map[string]string{
		`C:\Users\old\AppData\Roaming\zen\Profiles\ab.Default (release)\extensions\a@b.xpi`: "/home/new/.zen/xy.Restored/extensions/a@b.xpi",
		`C:\Users\old\Downloads`: "/home/new/Downloads",
		"jar:file:///C:/Users/old/AppData/Roaming/zen/Profiles/ab.Default%20(release)/extensions/%7Bid%7D.xpi!/": "jar:file:///home/new/.zen/xy.Restored/extensions/%7Bid%7D.xpi!/",
	}
	for from, want := range cases {
		got, ok := r.value(from)
		require.True(t, ok, from)
		require.Equal(t, want, got)
	}

	_, ok := r.value("https://example.com")
	require.False(t, ok, "unrelated strings must not be rewritten")
}

func TestRoundTripRewritesPaths(t *testing.T) {
	tmp := t.TempDir()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	src := filepath.Join(tmp, "src", "ab.Old Profile")
	writeFiles(t, src, map[string]string{
		"places.sqlite":         "db",
		"places.sqlite-wal":     "wal",
		"places.sqlite-shm":     "shm",
		".parentlock":           "",
		"compatibility.ini":     "[Compatibility]\nLastVersion=1.0b_1/1\n",
		"cache2/entries/x":      "cache",
		"chrome/userChrome.css": "a{}",
		"prefs.js": `user_pref("browser.download.dir", ` + jsonString(filepath.Join(home, "Downloads")) + ");\n" +
			`user_pref("some.path", ` + jsonString(filepath.Join(src, "x")) + ");\n",
		"extensions.json": `{"addons":[{"path":` + jsonString(filepath.Join(src, "extensions", "a.xpi")) + `}]}`,
	})

	archive := filepath.Join(tmp, "b.tar.gz")
	_, err = Create(src, "Old", "1.0b", archive, Options{}, nil)
	require.NoError(t, err)

	dst := filepath.Join(tmp, "dst", "cd.New")
	writeFiles(t, dst, map[string]string{"stale": "x"})

	res, err := Restore(archive, dst, true, nil)
	require.NoError(t, err)

	require.NotEmpty(t, res.PreviousDir, "previous profile should be kept")
	require.FileExists(t, filepath.Join(res.PreviousDir, "stale"))
	for _, kept := range []string{"places.sqlite", "places.sqlite-wal", "chrome/userChrome.css"} {
		require.FileExists(t, filepath.Join(dst, kept))
	}
	for _, dropped := range []string{"places.sqlite-shm", ".parentlock", "compatibility.ini", "cache2", "stale"} {
		require.NoFileExists(t, filepath.Join(dst, dropped))
		require.NoDirExists(t, filepath.Join(dst, dropped))
	}

	extensions, err := os.ReadFile(filepath.Join(dst, "extensions.json"))
	require.NoError(t, err)
	require.Contains(t, string(extensions), jsonString(filepath.Join(dst, "extensions", "a.xpi")))

	prefs, err := os.ReadFile(filepath.Join(dst, "prefs.js"))
	require.NoError(t, err)
	require.NotContains(t, string(prefs), "Old Profile")
	require.NotContains(t, string(prefs), ".restoring-")
}

func TestRestoreRejectsPathTraversal(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "evil.tar.gz")
	writeTarGz(t, archive, map[string]string{
		ManifestName:            `{"format_version":1}`,
		"profile/../../escaped": "x",
	})

	dst := filepath.Join(tmp, "a", "profile")
	_, err := Restore(archive, dst, false, nil)

	require.ErrorContains(t, err, "unsafe path")
	require.NoFileExists(t, filepath.Join(tmp, "escaped"))
	require.NoDirExists(t, dst, "a failed restore must not create the target")
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

// writeTarGz writes entries in a fixed order: the manifest first, then the rest.
func writeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	write := func(name, content string) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	write(ManifestName, entries[ManifestName])
	for name, content := range entries {
		if name != ManifestName {
			write(name, content)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, f.Close())
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
