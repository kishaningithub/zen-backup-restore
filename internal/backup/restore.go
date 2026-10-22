package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func openArchive(archivePath string) (*tar.Reader, func() error, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s is not a gzip archive: %w", archivePath, err)
	}
	return tar.NewReader(gz), func() error { return errors.Join(gz.Close(), f.Close()) }, nil
}

// ReadManifest reads only the manifest from an archive.
func ReadManifest(archivePath string) (Manifest, error) {
	var m Manifest
	tr, closeFn, err := openArchive(archivePath)
	if err != nil {
		return m, err
	}
	defer func() { _ = closeFn() }()
	hdr, err := tr.Next()
	if err != nil {
		return m, fmt.Errorf("reading archive: %w", err)
	}
	if hdr.Name != ManifestName {
		return m, fmt.Errorf("%s was not created by this tool (no %s)", archivePath, ManifestName)
	}
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("invalid manifest: %w", err)
	}
	if m.FormatVersion > FormatVersion {
		return m, fmt.Errorf("backup format v%d is newer than this tool supports (v%d)", m.FormatVersion, FormatVersion)
	}
	return m, nil
}

// extract unpacks the profile/ entries of the archive into destDir.
func extract(archivePath, destDir string, progress Progress) error {
	tr, closeFn, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = closeFn() }()
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	var files int
	var bytes int64
	last := time.Now()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		if !strings.HasPrefix(hdr.Name, ProfilePrefix) {
			continue
		}
		rel := path.Clean(strings.TrimPrefix(hdr.Name, ProfilePrefix))
		if rel == "." {
			continue
		}
		if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, `\`) {
			return fmt.Errorf("refusing unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := hdr.FileInfo().Mode().Perm() | 0o600
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return fmt.Errorf("%s: %w", rel, err)
			}
			if err := out.Close(); err != nil {
				return err
			}
			_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			files++
			bytes += hdr.Size
			if progress != nil && time.Since(last) > 200*time.Millisecond {
				progress(files, bytes)
				last = time.Now()
			}
		default:
			// Links and special files are never written by Create; ignore.
		}
	}
	if progress != nil {
		progress(files, bytes)
	}
	return nil
}

// fixup makes the profile extracted into dir usable at finalDir.
func fixup(dir, finalDir string, m Manifest) error {
	for _, name := range []string{"compatibility.ini", "addonStartup.json.lz4", "lock", ".parentlock", "parent.lock"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	rw := newPathRewriter(m, finalDir, home)
	for _, name := range []string{"prefs.js", "user.js"} {
		if err := rw.rewritePrefsFile(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("rewriting %s: %w", name, err)
		}
	}
	jsonFiles, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range jsonFiles {
		if err := rw.rewriteJSONFile(f); err != nil {
			return fmt.Errorf("rewriting %s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

// RestoreResult reports what Restore did.
type RestoreResult struct {
	Manifest Manifest
	// PreviousDir is where the old profile was moved ("" if none was kept).
	PreviousDir string
}

// Restore replaces (or creates) targetDir with the archive's profile.
// The new profile is fully extracted next to the target first, so a failed
// restore never leaves a half-written profile behind. If keepPrevious is
// set, an existing profile is renamed to "<target>.pre-restore-<time>".
func Restore(archivePath, targetDir string, keepPrevious bool, progress Progress) (RestoreResult, error) {
	var res RestoreResult
	m, err := ReadManifest(archivePath)
	if err != nil {
		return res, err
	}
	res.Manifest = m

	targetDir = filepath.Clean(targetDir)
	stamp := time.Now().Format("20060102-150405")
	staging := targetDir + ".restoring-" + stamp
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o700); err != nil {
		return res, err
	}
	if err := extract(archivePath, staging, progress); err != nil {
		_ = os.RemoveAll(staging)
		return res, err
	}
	if err := fixup(staging, targetDir, m); err != nil {
		_ = os.RemoveAll(staging)
		return res, err
	}

	if _, err := os.Stat(targetDir); err == nil {
		previous := targetDir + ".pre-restore-" + stamp
		if err := os.Rename(targetDir, previous); err != nil {
			_ = os.RemoveAll(staging)
			return res, fmt.Errorf("moving existing profile aside: %w", err)
		}
		if keepPrevious {
			res.PreviousDir = previous
		} else {
			defer func() { _ = os.RemoveAll(previous) }()
		}
	}
	if err := os.Rename(staging, targetDir); err != nil {
		if res.PreviousDir != "" {
			_ = os.Rename(res.PreviousDir, targetDir)
		}
		return res, fmt.Errorf("activating restored profile: %w", err)
	}
	return res, nil
}
