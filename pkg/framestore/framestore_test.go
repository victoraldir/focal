package framestore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/framestore"
)

func TestSave_WritesFileAndStampsMtime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "frames")
	takenAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	path, err := framestore.New(dir).Save("frame_00001.jpg", []byte("jpeg"), takenAt)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if path != filepath.Join(dir, "frame_00001.jpg") {
		t.Errorf("path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "jpeg" {
		t.Fatalf("read back %q, %v", got, err)
	}
	info, _ := os.Stat(path)
	if !info.ModTime().Equal(takenAt) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), takenAt)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected no leftover temp files, got %d entries", len(entries))
	}
}

func TestSave_RejectsPathTraversal(t *testing.T) {
	if _, err := framestore.New(t.TempDir()).Save("../escape.jpg", []byte("x"), time.Now()); err == nil {
		t.Fatal("expected an error for a name containing a path")
	}
}
