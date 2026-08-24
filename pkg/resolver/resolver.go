// Package resolver locates an executable FFmpeg binary for the current platform.
// It first checks the system PATH; failing that, on supported platforms it
// downloads a static build into the user cache directory and reuses it on
// subsequent runs. It implements domain.BinaryResolver.
//
// The package is structured so support for new operating systems is added by
// registering an entry in the platform build table (see builds.go) — the
// resolution flow in this file never needs to change.
package resolver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/victoraldir/focal/pkg/domain"
)

// binaryName is the FFmpeg executable name looked up on PATH and written to the
// cache. Windows support will override this with "ffmpeg.exe" via the build table.
const binaryName = "ffmpeg"

// Resolver implements domain.BinaryResolver. It composes a PATH lookup, a cache
// directory, a downloader, and an archive extractor — each injected so the
// resolution policy can be tested without touching the network.
type Resolver struct {
	// lookPath finds an executable on PATH; injectable for tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
	// cacheDir returns the base cache directory; defaults to os.UserCacheDir.
	cacheDir func() (string, error)
	// goos/goarch identify the target platform; default to the build's runtime
	// values but are overridable so platform selection is testable.
	goos   string
	goarch string

	downloader domain.BinaryDownloader
	extract    extractFunc
	log        domain.Logger

	// builds maps "<goos>/<goarch>" to the static-build descriptor to fetch.
	builds map[string]buildTarget
}

// Option customises a Resolver at construction time.
type Option func(*Resolver)

// WithLookPath overrides the PATH lookup function (used in tests).
func WithLookPath(fn func(string) (string, error)) Option {
	return func(r *Resolver) { r.lookPath = fn }
}

// WithCacheDir overrides the cache-directory provider (used in tests).
func WithCacheDir(fn func() (string, error)) Option {
	return func(r *Resolver) { r.cacheDir = fn }
}

// WithPlatform overrides the detected OS/architecture (used in tests).
func WithPlatform(goos, goarch string) Option {
	return func(r *Resolver) { r.goos, r.goarch = goos, goarch }
}

// WithBuilds overrides the platform build table (used in tests to avoid real URLs).
func WithBuilds(builds map[string]buildTarget) Option {
	return func(r *Resolver) { r.builds = builds }
}

// New constructs a Resolver. The downloader is injected (satisfying
// domain.BinaryDownloader) so the network transfer is decoupled from the
// platform-detection and extraction logic.
func New(downloader domain.BinaryDownloader, log domain.Logger, opts ...Option) *Resolver {
	r := &Resolver{
		lookPath:   exec.LookPath,
		cacheDir:   os.UserCacheDir,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		downloader: downloader,
		extract:    extractArchive,
		log:        log,
		builds:     defaultBuilds,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve returns a path to a runnable FFmpeg binary, obtaining one as needed.
// Resolution order: PATH, then the cache, then a fresh download.
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	if path, err := r.lookPath(binaryName); err == nil {
		r.log.Infof("Using ffmpeg from PATH: %s", path)
		return path, nil
	}

	cachedPath, err := r.cachedBinaryPath()
	if err != nil {
		return "", err
	}
	if isExecutable(cachedPath) {
		r.log.Infof("Using cached ffmpeg: %s", cachedPath)
		return cachedPath, nil
	}

	return r.fetch(ctx, cachedPath)
}

// fetch downloads and extracts the platform's static build into the cache,
// returning the path to the extracted, executable binary.
func (r *Resolver) fetch(ctx context.Context, destBinary string) (string, error) {
	target, ok := r.builds[r.platformKey()]
	if !ok {
		return "", fmt.Errorf(
			"ffmpeg is not installed and automatic download is not yet supported for %s; please install ffmpeg and ensure it is on your PATH",
			r.platformKey(),
		)
	}

	binDir := filepath.Dir(destBinary)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir: %w", err)
	}

	archivePath := filepath.Join(binDir, target.archiveName())
	r.log.Infof("ffmpeg not found; downloading static build for %s", r.platformKey())
	if err := r.downloader.Download(ctx, target.URL, archivePath); err != nil {
		return "", fmt.Errorf("downloading ffmpeg: %w", err)
	}
	defer os.Remove(archivePath)

	if err := r.extract(archivePath, target.BinaryPathInArchive, destBinary); err != nil {
		return "", fmt.Errorf("extracting ffmpeg: %w", err)
	}
	if err := os.Chmod(destBinary, 0o755); err != nil {
		return "", fmt.Errorf("making ffmpeg executable: %w", err)
	}

	r.log.Infof("Installed ffmpeg to %s", destBinary)
	return destBinary, nil
}

// cachedBinaryPath returns the expected on-disk path of the managed binary:
// <UserCacheDir>/focal/bin/ffmpeg.
func (r *Resolver) cachedBinaryPath() (string, error) {
	base, err := r.cacheDir()
	if err != nil {
		return "", fmt.Errorf("locating user cache dir: %w", err)
	}
	return filepath.Join(base, "focal", "bin", binaryName), nil
}

// platformKey is the "<goos>/<goarch>" lookup key into the build table.
func (r *Resolver) platformKey() string {
	return r.goos + "/" + r.goarch
}

// isExecutable reports whether path exists and has an owner-execute bit set.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o100 != 0
}
