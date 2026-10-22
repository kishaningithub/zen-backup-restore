//go:build windows

package zen

import (
	"os"
	"path/filepath"
)

// InUse reports whether a running Zen instance holds the profile lock.
// Zen opens parent.lock without sharing, so opening it fails while running.
func InUse(profileDir string) bool {
	p := filepath.Join(profileDir, "parent.lock")
	if _, err := os.Stat(p); err != nil {
		return false
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return true
	}
	f.Close()
	return false
}
