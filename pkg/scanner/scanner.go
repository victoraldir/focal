// Package scanner discovers image files in a directory, reads their pixel
// dimensions from the file header, resolves a capture timestamp (EXIF first,
// modification time as a fallback), and returns the sequence sorted
// chronologically. It implements domain.ImageScanner.
package scanner

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// Registered for their image.DecodeConfig side effects so headers of these
	// formats can be read without decoding full pixel data.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/victoraldir/focal/pkg/domain"
)

// supportedExtensions is the set of file extensions the scanner will consider.
// Values are lower-case and include the leading dot.
var supportedExtensions = map[string]struct{}{
	".jpg":  {},
	".jpeg": {},
	".png":  {},
	".gif":  {},
	".tif":  {},
	".tiff": {},
}

// FileScanner implements domain.ImageScanner over the local filesystem.
type FileScanner struct {
	exif domain.ExifExtractor
}

// New constructs a FileScanner with the given EXIF extractor injected, keeping
// metadata reading decoupled and mockable.
func New(exif domain.ExifExtractor) *FileScanner {
	return &FileScanner{exif: exif}
}

// Scan reads dir (non-recursively), collecting supported image files with their
// dimensions and timestamps, then returns them ordered oldest-first. Files that
// cannot be decoded as images are skipped rather than failing the whole scan.
func (s *FileScanner) Scan(ctx context.Context, dir string) ([]domain.Image, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", dir, err)
	}

	var images []domain.Image
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if _, ok := supportedExtensions[ext]; !ok {
			continue
		}

		path, err := filepath.Abs(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("resolving path for %q: %w", entry.Name(), err)
		}

		img, ok := s.inspect(path)
		if !ok {
			// Not a decodable image (e.g. a .png that is actually text); skip it.
			continue
		}
		images = append(images, img)
	}

	// Stable chronological sort. Ties (identical timestamps, common when EXIF is
	// absent and files share a mtime) fall back to path for deterministic output.
	sort.SliceStable(images, func(i, j int) bool {
		if images[i].Timestamp.Equal(images[j].Timestamp) {
			return images[i].Path < images[j].Path
		}
		return images[i].Timestamp.Before(images[j].Timestamp)
	})

	return images, nil
}

// inspect reads the header dimensions and resolves the timestamp for a single
// file. It returns false when the file is not a decodable image.
func (s *FileScanner) inspect(path string) (domain.Image, bool) {
	f, err := os.Open(path)
	if err != nil {
		return domain.Image{}, false
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return domain.Image{}, false
	}

	ts, fromEXIF := s.resolveTimestamp(path)
	return domain.Image{
		Path:              path,
		Width:             cfg.Width,
		Height:            cfg.Height,
		Timestamp:         ts,
		TimestampFromEXIF: fromEXIF,
	}, true
}

// resolveTimestamp returns the EXIF DateTimeOriginal when present, otherwise the
// file modification time. If both are unavailable it returns the zero time.
func (s *FileScanner) resolveTimestamp(path string) (time.Time, bool) {
	if s.exif != nil {
		if t, ok := s.exif.Timestamp(path); ok {
			return t, true
		}
	}
	if info, err := os.Stat(path); err == nil {
		return info.ModTime(), false
	}
	return time.Time{}, false
}
