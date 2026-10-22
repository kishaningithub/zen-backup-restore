package zen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAndRegisterProfiles(t *testing.T) {
	root := t.TempDir()
	ini := "[Install123]\nDefault=Profiles/b.Second\nLocked=1\n\n" +
		"[Profile0]\nName=First\nIsRelative=1\nPath=Profiles/a.First\nDefault=1\n\n" +
		"[Profile1]\nName=Second\nIsRelative=1\nPath=Profiles/b.Second\n\n" +
		"[General]\nStartWithLastProfile=1\nVersion=2\n"
	os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0o644)

	ps, err := ListProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Name != "Second" || !ps[0].Default || ps[1].Default {
		t.Fatalf("install default should win and sort first: %+v", ps)
	}

	dir, err := NewProfileDir(root, "Restored")
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterProfile(root, "Restored", dir); err != nil {
		t.Fatal(err)
	}
	ps, _ = ListProfiles(root)
	p, err := FindProfile(ps, "restored")
	if err != nil || p.Path != dir {
		t.Fatalf("registered profile not found: %+v %v", p, err)
	}
}
