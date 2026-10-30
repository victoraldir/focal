// Package framestore persists captured frames to a local directory. It
// implements domain.FrameStore.
package framestore

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DirStore writes frames into a single directory.
type DirStore struct {
	dir string
}

// New returns a store rooted at dir. The directory is created lazily on the
// first Save so a session that never captures leaves nothing behind.
func New(dir string) *DirStore {
	return &DirStore{dir: dir}
}

// Save writes data to dir/name atomically (temp file + rename) so an
// interrupted write never leaves a truncated frame for the lapse scanner, then
// sets the file's modification time to takenAt. The scanner falls back to
// mtime when a frame has no EXIF timestamp, so this keeps ordering correct.
func (s *DirStore) Save(name string, data []byte, takenAt time.Time) (string, error) {
	if name != filepath.Base(name) {
		return "", fmt.Errorf("invalid frame name %q", name)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("creating output directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".focal-frame-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("writing frame: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("closing frame: %w", err)
	}

	dest := filepath.Join(s.dir, name)
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("moving frame into place: %w", err)
	}
	if !takenAt.IsZero() {
		_ = os.Chtimes(dest, takenAt, takenAt)
	}
	return dest, nil
}
