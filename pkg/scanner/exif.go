package scanner

import (
	"os"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// GoExifExtractor implements domain.ExifExtractor using the rwcarlsen/goexif
// library. It reads only the DateTimeOriginal tag; any failure (unreadable file,
// missing tag, unparseable value) is reported as "not found" so the caller can
// fall back to filesystem metadata rather than aborting the whole run.
type GoExifExtractor struct{}

// NewGoExifExtractor returns a ready-to-use EXIF extractor.
func NewGoExifExtractor() *GoExifExtractor { return &GoExifExtractor{} }

// Timestamp returns the DateTimeOriginal capture time for the image at path.
// The boolean is false when no usable timestamp could be read.
func (GoExifExtractor) Timestamp(path string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return time.Time{}, false
	}

	// DateTime resolves DateTimeOriginal (falling back to DateTime) and applies
	// any timezone offset tags the file carries.
	t, err := x.DateTime()
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
