package backup

import (
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samber/lo"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// SQLite databases larger than this are compacted in slim backups.
const minCompactBytes = 10 * 1024 * 1024

// Files SQLite keeps next to a database while changes are not yet merged
// into it. A compacted copy already contains those changes.
var sqliteJournalSuffixes = []string{"-wal", "-journal"}

// compactedFileInfo reports the size of the compacted copy while keeping
// the original file's name, mode and modification time.
type compactedFileInfo struct {
	fs.FileInfo
	size int64
}

func (c compactedFileInfo) Size() int64 { return c.size }

// compactDatabases swaps large SQLite databases in entries for vacuumed
// copies written under tempDir and drops their journal files, which the
// copies already include. The profile itself is never modified; a database
// that cannot be vacuumed is backed up as is.
func compactDatabases(profileDir, tempDir string, entries []profileEntry) []profileEntry {
	compacted := map[string]bool{}
	entries = lo.Map(entries, func(e profileEntry, i int) profileEntry {
		if !isLargeDatabase(e) {
			return e
		}
		src := filepath.Join(profileDir, filepath.FromSlash(e.rel))
		copyPath, size, err := vacuumedCopy(src, filepath.Join(tempDir, strconv.Itoa(i)))
		if err != nil {
			return e
		}
		compacted[e.rel] = true
		return profileEntry{rel: e.rel, info: compactedFileInfo{e.info, size}, src: copyPath}
	})
	return lo.Reject(entries, func(e profileEntry, _ int) bool {
		return lo.SomeBy(sqliteJournalSuffixes, func(suffix string) bool {
			db, isJournal := strings.CutSuffix(e.rel, suffix)
			return isJournal && compacted[db]
		})
	})
}

func isLargeDatabase(e profileEntry) bool {
	return !e.info.IsDir() && path.Ext(e.rel) == ".sqlite" && e.info.Size() > minCompactBytes
}

// vacuumedCopy copies the database at src and its journal files into dir,
// vacuums the copy and returns its path and size.
func vacuumedCopy(src, dir string) (string, int64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := copyToFile(src, dst); err != nil {
		return "", 0, err
	}
	for _, suffix := range sqliteJournalSuffixes {
		if err := copyToFile(src+suffix, dst+suffix); err != nil && !os.IsNotExist(err) {
			return "", 0, err
		}
	}
	if err := vacuum(dst); err != nil {
		return "", 0, err
	}
	for _, suffix := range sqliteJournalSuffixes {
		if info, err := os.Stat(dst + suffix); err == nil && info.Size() > 0 {
			return "", 0, fmt.Errorf("%s was not merged into the database", filepath.Base(dst+suffix))
		}
	}
	info, err := os.Stat(dst)
	if err != nil {
		return "", 0, err
	}
	return dst, info.Size(), nil
}

// vacuum rebuilds the database to drop free pages and merges any
// write-ahead log into it, so the file is self-contained.
func vacuum(dbPath string) (err error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer func() { err = lo.CoalesceOrEmpty(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("VACUUM"); err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func copyToFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
