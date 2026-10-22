//go:build !windows

package zen

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// InUse reports whether a running Zen instance holds the profile lock.
func InUse(profileDir string) bool {
	// Linux also uses a "lock" symlink pointing at "<ip>:+<pid>".
	if target, err := os.Readlink(filepath.Join(profileDir, "lock")); err == nil {
		if i := strings.LastIndex(target, "+"); i >= 0 {
			if pid, err := strconv.Atoi(target[i+1:]); err == nil && pid > 0 {
				if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
					return true
				}
			}
		}
	}

	f, err := os.OpenFile(filepath.Join(profileDir, ".parentlock"), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return false
	}
	return lk.Type != syscall.F_UNLCK
}
