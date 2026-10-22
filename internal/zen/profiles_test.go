package zen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestListAndRegisterProfiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "profiles.ini"), `[Install123]
Default=Profiles/b.Second
Locked=1

[Profile0]
Name=First
IsRelative=1
Path=Profiles/a.First
Default=1

[Profile1]
Name=Second
IsRelative=1
Path=Profiles/b.Second

[General]
StartWithLastProfile=1
Version=2
`)

	profiles, err := ListProfiles(root)
	require.NoError(t, err)
	require.Equal(t, []Profile{
		{Name: "Second", Path: filepath.Join(root, "Profiles", "b.Second"), Default: true},
		{Name: "First", Path: filepath.Join(root, "Profiles", "a.First")},
	}, profiles, "install default should win and sort first")

	dir, err := NewProfileDir(root, "Restored")
	require.NoError(t, err)
	require.NoError(t, RegisterProfile(root, "Restored", dir))

	profiles, err = ListProfiles(root)
	require.NoError(t, err)
	restored, err := FindProfile(profiles, "restored")
	require.NoError(t, err)
	require.Equal(t, dir, restored.Path)
}

// Scoop's zen-browser manifest registers a profile outside the Zen data dir
// (in Scoop's persist folder) with an absolute path, and installs.ini then
// points at that absolute path.
func TestAbsoluteProfilePathAsInstallDefault(t *testing.T) {
	root := t.TempDir()
	scoopProfile := filepath.Join(t.TempDir(), "persist", "zen-browser", "profile")
	writeFile(t, filepath.Join(root, "profiles.ini"), `[Profile0]
Name=Default (release)
IsRelative=1
Path=Profiles/a.Default

[Profile1]
Name=Scoop
IsRelative=0
Path=`+scoopProfile+`
`)
	writeFile(t, filepath.Join(root, "installs.ini"), "[ABC]\nDefault="+scoopProfile+"\nLocked=1\n")

	profiles, err := ListProfiles(root)
	require.NoError(t, err)
	require.Equal(t, Profile{Name: "Scoop", Path: scoopProfile, Default: true}, profiles[0])
}
