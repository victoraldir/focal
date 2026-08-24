package domain

import (
	"fmt"
	"math"
	"time"
)

// Image represents a single source photograph in a timelapse sequence together
// with the metadata focal needs to order and encode it.
type Image struct {
	// Path is the absolute path to the image file on disk.
	Path string
	// Width and Height are the pixel dimensions read from the image header.
	Width  int
	Height int
	// Timestamp is the moment the photo was taken. It is sourced from the EXIF
	// DateTimeOriginal tag when available, and falls back to the file's
	// modification time otherwise.
	Timestamp time.Time
	// TimestampFromEXIF reports whether Timestamp came from EXIF (true) or from
	// the filesystem modification time (false).
	TimestampFromEXIF bool
}

// AspectRatio returns the width/height ratio of the image. It returns 0 when
// the height is unknown to avoid a division by zero.
func (i Image) AspectRatio() float64 {
	if i.Height == 0 {
		return 0
	}
	return float64(i.Width) / float64(i.Height)
}

// LapseRequest captures everything needed to build a single timelapse. It is a
// plain value object with no behaviour so it can cross layer boundaries freely.
type LapseRequest struct {
	// InputDir is the directory containing the source photographs.
	InputDir string
	// OutputPath is the destination video file (e.g. "timelapse.mp4").
	OutputPath string
	// FPS is the target output frame rate.
	FPS int
}

// Validate returns an error if the request is missing required fields or holds
// values that FFmpeg could not act on.
func (r LapseRequest) Validate() error {
	if r.InputDir == "" {
		return fmt.Errorf("input directory is required")
	}
	if r.OutputPath == "" {
		return fmt.Errorf("output path is required")
	}
	if r.FPS <= 0 {
		return fmt.Errorf("fps must be a positive integer, got %d", r.FPS)
	}
	return nil
}

// AspectRatioVaries reports whether the aspect ratios across the sequence differ
// by more than tolerance. FFmpeg can encode mixed aspect ratios, but the result
// is usually undesirable (letterboxing or stretching), so callers surface this
// as a warning. It returns the smallest and largest ratios seen so the caller
// can build a helpful message.
func AspectRatioVaries(images []Image, tolerance float64) (varies bool, min, max float64) {
	first := true
	for _, img := range images {
		ar := img.AspectRatio()
		if ar == 0 {
			continue
		}
		if first {
			min, max = ar, ar
			first = false
			continue
		}
		min = math.Min(min, ar)
		max = math.Max(max, ar)
	}
	if first {
		return false, 0, 0
	}
	return (max - min) > tolerance, min, max
}
