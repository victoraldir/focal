package scanner_test

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/domain"
	"github.com/victoraldir/focal/pkg/scanner"
)

// stubExif returns canned timestamps keyed by file path so chronological
// ordering can be tested deterministically without embedding real EXIF data.
type stubExif struct {
	times map[string]time.Time
}

func (s stubExif) Timestamp(path string) (time.Time, bool) {
	if s.times == nil {
		return time.Time{}, false
	}
	t, ok := s.times[filepath.Base(path)]
	return t, ok
}

// writeJPEG writes a solid-colour JPEG of the given size and returns its path.
func writeJPEG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScan_ReadsDimensions(t *testing.T) {
	dir := t.TempDir()
	writeJPEG(t, dir, "a.jpg", 640, 480)

	s := scanner.New(stubExif{})
	images, err := s.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].Width != 640 || images[0].Height != 480 {
		t.Errorf("dimensions = %dx%d, want 640x480", images[0].Width, images[0].Height)
	}
}

func TestScan_SortsByEXIFTimestamp(t *testing.T) {
	dir := t.TempDir()
	writeJPEG(t, dir, "z_last.jpg", 100, 100)
	writeJPEG(t, dir, "a_first.jpg", 100, 100)
	writeJPEG(t, dir, "m_middle.jpg", 100, 100)

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	s := scanner.New(stubExif{times: map[string]time.Time{
		"a_first.jpg":  base.Add(3 * time.Hour),
		"m_middle.jpg": base.Add(2 * time.Hour),
		"z_last.jpg":   base.Add(1 * time.Hour), // earliest by EXIF despite the name
	}})

	images, err := s.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	want := []string{"z_last.jpg", "m_middle.jpg", "a_first.jpg"}
	for i, w := range want {
		if got := filepath.Base(images[i].Path); got != w {
			t.Errorf("position %d = %s, want %s (EXIF ordering)", i, got, w)
		}
	}
	if !images[0].TimestampFromEXIF {
		t.Error("expected TimestampFromEXIF=true when EXIF is present")
	}
}

func TestScan_FallsBackToModTime(t *testing.T) {
	dir := t.TempDir()
	path := writeJPEG(t, dir, "a.jpg", 100, 100)

	modTime := time.Date(2019, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}

	s := scanner.New(stubExif{}) // no EXIF available
	images, err := s.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if images[0].TimestampFromEXIF {
		t.Error("expected fallback to modtime (TimestampFromEXIF=false)")
	}
	if !images[0].Timestamp.Equal(modTime) {
		t.Errorf("timestamp = %v, want modtime %v", images[0].Timestamp, modTime)
	}
}

func TestScan_IgnoresNonImages(t *testing.T) {
	dir := t.TempDir()
	writeJPEG(t, dir, "real.jpg", 100, 100)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake.jpg"), []byte("not a jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := scanner.New(stubExif{})
	images, err := s.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(images) != 1 {
		t.Errorf("expected only the valid image, got %d", len(images))
	}
}

func TestScan_DirectoryError(t *testing.T) {
	s := scanner.New(stubExif{})
	if _, err := s.Scan(context.Background(), filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing directory")
	}
}

// Guard: FileScanner must satisfy the domain interface.
var _ domain.ImageScanner = (*scanner.FileScanner)(nil)
