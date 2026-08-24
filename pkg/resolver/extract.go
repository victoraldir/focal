package resolver

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractFunc extracts a single binary from an archive to destPath. It is a
// function type (rather than a method) so the extraction step can be swapped out
// in tests without a real archive on disk.
//
// wantedPath, when non-empty, selects a specific entry inside the archive;
// empty means "the first regular file".
type extractFunc func(archivePath, wantedPath, destPath string) error

// extractArchive dispatches on the archive extension. Only zip is implemented so
// far because that is what the macOS build source provides; tar.xz handling for
// Linux builds slots in here without touching callers.
func extractArchive(archivePath, wantedPath, destPath string) error {
	switch {
	case strings.HasSuffix(archivePath, ".zip"):
		return extractZip(archivePath, wantedPath, destPath)
	default:
		return fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
	}
}

// extractZip copies the selected entry out of a zip archive to destPath. When
// wantedPath is empty the first regular (non-directory) file is used.
func extractZip(archivePath, wantedPath, destPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer zr.Close()

	var entry *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if wantedPath == "" || filepath.Clean(f.Name) == filepath.Clean(wantedPath) || filepath.Base(f.Name) == wantedPath {
			entry = f
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("no matching binary found in archive (wanted %q)", wantedPath)
	}

	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("opening archive entry: %w", err)
	}
	defer rc.Close()

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("writing extracted binary: %w", err)
	}
	return nil
}
