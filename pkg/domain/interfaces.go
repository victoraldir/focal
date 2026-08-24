package domain

import (
	"context"
	"io"
	"time"
)

// ImageScanner discovers image files in a directory, reads their headers and
// timestamps, and returns them sorted chronologically. Decoupling this behind
// an interface keeps disk I/O and image decoding out of the core use case so it
// can be exercised with in-memory test doubles.
type ImageScanner interface {
	Scan(ctx context.Context, dir string) ([]Image, error)
}

// ExifExtractor resolves the capture time of a single image. Implementations
// read the EXIF DateTimeOriginal tag; the boolean return reports whether a value
// was found so callers can fall back to filesystem metadata.
type ExifExtractor interface {
	Timestamp(path string) (t time.Time, found bool)
}

// BinaryResolver returns the path to an executable FFmpeg binary, obtaining one
// (from PATH, a cache, or a download) as needed. The core use case depends only
// on this interface, so adding Windows or Linux resolution later requires no
// change to the domain.
type BinaryResolver interface {
	Resolve(ctx context.Context) (ffmpegPath string, err error)
}

// BinaryDownloader fetches a remote artifact to a local path. It is split out
// from BinaryResolver so the network transfer can be mocked independently of the
// platform-detection and extraction logic.
type BinaryDownloader interface {
	Download(ctx context.Context, url, destPath string) error
}

// EncodeRequest is the fully-resolved instruction handed to an Encoder. All
// paths are absolute and the FFmpeg binary has already been located.
type EncodeRequest struct {
	FFmpegPath     string
	ConcatFilePath string
	OutputPath     string
	FPS            int
	// TotalFrames is the number of source images. It lets the encoder render a
	// determinate progress bar (frames done / total) rather than a spinner, and
	// bounds the output to exactly this many frames.
	TotalFrames int
	// ScaleHeight, when greater than zero, is the height the output is scaled to
	// (preserving aspect ratio, width auto-adjusted to an even number). Zero
	// keeps the source resolution.
	ScaleHeight int
}

// Encoder turns a prepared concat file into an output video. It owns FFmpeg
// argument construction and delegates process execution to an FFmpegRunner.
type Encoder interface {
	Encode(ctx context.Context, req EncodeRequest, stderr io.Writer) error
}

// FFmpegRunner is the low-level process-execution seam. Implementations run the
// given command, connecting its standard output and standard error to the
// provided writers. FFmpeg's machine-readable "-progress" stream is written to
// stdout while human-readable diagnostics go to stderr, so the encoder wires a
// progress renderer to the former and an error buffer to the latter. Tests
// substitute a runner that records arguments instead of spawning a process.
type FFmpegRunner interface {
	Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error
}

// ConcatBuilder writes an FFmpeg concat-demuxer file describing the ordered
// sequence and per-frame durations. It returns the file path and a cleanup
// function the caller must invoke to remove the temporary file.
type ConcatBuilder interface {
	Build(images []Image, fps int) (path string, cleanup func() error, err error)
}

// Logger is a minimal structured-ish logging seam so the domain can surface
// progress and warnings without binding to a concrete logging library.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}
