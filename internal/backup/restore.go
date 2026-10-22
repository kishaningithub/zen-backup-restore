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
	tr, closeArchive, err := openArchive(archivePath)
	if err != nil {
		return m, err
	}
	defer func() { _ = closeArchive() }()
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
	tr, closeArchive, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = closeArchive() }()

	tracker := progressTracker{report: progress}
	defer tracker.flush()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		if !strings.HasPrefix(hdr.Name, ProfilePrefix) {
			continue
		}
		rel := path.Clean(strings.TrimPrefix(hdr.Name, ProfilePrefix))
		if !isSafeRelPath(rel) {
			return fmt.Errorf("refusing unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := extractFile(tr, hdr, target); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			tracker.add(hdr.Size)
		default:
			// Links and special files are never written by Create; ignore.
		}
	}
}

// isSafeRelPath reports whether a cleaned archive path stays inside the
// extraction directory.
func isSafeRelPath(rel string) bool {
	return rel != "." && rel != ".." &&
		!path.IsAbs(rel) &&
		!strings.HasPrefix(rel, "../") &&
		!strings.Contains(rel, `\`)
}

func extractFile(r io.Reader, hdr *tar.Header, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	mode := hdr.FileInfo().Mode().Perm() | 0o600
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(target, hdr.ModTime, hdr.ModTime)
}

// regeneratedFiles are removed after extraction. They hold locks or
// location-specific caches that Zen rebuilds on the next start.
var regeneratedFiles = []string{"compatibility.ini", "addonStartup.json.lz4", "lock", ".parentlock", "parent.lock"}

// fixup makes the profile extracted into dir usable at finalDir.
func fixup(dir, finalDir string, m Manifest) error {
	for _, name := range regeneratedFiles {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	return newPathRewriter(m, finalDir, home).rewriteProfile(dir)
}

// RestoreResult reports what Restore did.
type RestoreResult struct {
	Manifest Manifest
	// PreviousDir is where the old profile was moved ("" if none was kept).
	PreviousDir string
}

// Restore replaces (or creates) targetDir with the archive's profile.
// The new profile is fully prepared next to the target first, so a failed
// restore never leaves a half-written profile behind. If keepPrevious is
// set, an existing profile is renamed to "<target>.pre-restore-<time>".
func Restore(archivePath, targetDir string, keepPrevious bool, progress Progress) (RestoreResult, error) {
	m, err := ReadManifest(archivePath)
	if err != nil {
		return RestoreResult{}, err
	}
	result := RestoreResult{Manifest: m}

	targetDir = filepath.Clean(targetDir)
	stamp := time.Now().Format("20060102-150405")
	staging := targetDir + ".restoring-" + stamp
	if err := prepareProfile(archivePath, staging, targetDir, m, progress); err != nil {
		_ = os.RemoveAll(staging)
		return result, err
	}

	previous, err := moveAsideIfExists(targetDir, targetDir+".pre-restore-"+stamp)
	if err != nil {
		_ = os.RemoveAll(staging)
		return result, fmt.Errorf("moving existing profile aside: %w", err)
	}
	if err := os.Rename(staging, targetDir); err != nil {
		if previous != "" {
			_ = os.Rename(previous, targetDir)
		}
		return result, fmt.Errorf("activating restored profile: %w", err)
	}

	if previous != "" && !keepPrevious {
		return result, os.RemoveAll(previous)
	}
	result.PreviousDir = previous
	return result, nil
}

// prepareProfile extracts the archive into staging and fixes it up so it can
// be moved to targetDir.
func prepareProfile(archivePath, staging, targetDir string, m Manifest, progress Progress) error {
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o700); err != nil {
		return err
	}
	if err := extract(archivePath, staging, progress); err != nil {
		return err
	}
	return fixup(staging, targetDir, m)
}

// moveAsideIfExists renames dir to newPath and returns newPath, or returns ""
// if dir does not exist.
func moveAsideIfExists(dir, newPath string) (string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", nil
	}
	if err := os.Rename(dir, newPath); err != nil {
		return "", err
	}
	return newPath, nil
}
