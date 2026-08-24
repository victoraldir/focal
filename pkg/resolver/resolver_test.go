package resolver

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type noopLogger struct{}

func (noopLogger) Infof(string, ...any) {}
func (noopLogger) Warnf(string, ...any) {}

// fakeDownloader satisfies domain.BinaryDownloader by copying a pre-made archive
// into place, so download behaviour is tested without any network access.
type fakeDownloader struct {
	src    string // path to an existing archive to copy
	called bool
	err    error
}

func (f *fakeDownloader) Download(_ context.Context, _ string, destPath string) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	data, err := os.ReadFile(f.src)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o644)
}

func TestResolve_PrefersPATH(t *testing.T) {
	r := New(&fakeDownloader{}, noopLogger{},
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/ffmpeg", nil }),
	)
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got != "/usr/local/bin/ffmpeg" {
		t.Errorf("got %q, want PATH binary", got)
	}
}

func TestResolve_UsesCachedBinary(t *testing.T) {
	cache := t.TempDir()
	binPath := filepath.Join(cache, "focal", "bin", "ffmpeg")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	dl := &fakeDownloader{}
	r := New(dl, noopLogger{},
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
		WithCacheDir(func() (string, error) { return cache, nil }),
		WithPlatform("darwin", "arm64"),
	)
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got != binPath {
		t.Errorf("got %q, want cached binary %q", got, binPath)
	}
	if dl.called {
		t.Error("should not download when a cached binary exists")
	}
}

func TestResolve_DownloadsAndExtracts(t *testing.T) {
	cache := t.TempDir()
	archive := makeZip(t, "ffmpeg", []byte("FAKE-FFMPEG-BINARY"))

	dl := &fakeDownloader{src: archive}
	r := New(dl, noopLogger{},
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
		WithCacheDir(func() (string, error) { return cache, nil }),
		WithPlatform("darwin", "arm64"),
		WithBuilds(map[string]buildTarget{
			"darwin/arm64": {URL: "https://example.test/ffmpeg.zip"},
		}),
	)

	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !dl.called {
		t.Error("expected a download to occur")
	}
	if !isExecutable(got) {
		t.Errorf("resolved binary %q is not executable", got)
	}
	data, _ := os.ReadFile(got)
	if string(data) != "FAKE-FFMPEG-BINARY" {
		t.Errorf("extracted binary content = %q, want the archived bytes", data)
	}
}

func TestResolve_UnsupportedPlatform(t *testing.T) {
	r := New(&fakeDownloader{}, noopLogger{},
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
		WithCacheDir(func() (string, error) { return t.TempDir(), nil }),
		WithPlatform("plan9", "mips"),
		WithBuilds(map[string]buildTarget{"darwin/arm64": {URL: "x"}}),
	)
	_, err := r.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}

func TestResolve_DownloadError(t *testing.T) {
	r := New(&fakeDownloader{err: errors.New("network down")}, noopLogger{},
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
		WithCacheDir(func() (string, error) { return t.TempDir(), nil }),
		WithPlatform("darwin", "arm64"),
		WithBuilds(map[string]buildTarget{"darwin/arm64": {URL: "https://example.test/ffmpeg.zip"}}),
	)
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected download error to propagate")
	}
}

// makeZip writes a single-entry zip archive to a temp file and returns its path.
func makeZip(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
