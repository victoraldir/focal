package domain

import (
	"context"
	"fmt"
	"io"
)

// aspectRatioTolerance is the maximum spread in width/height ratio the sequence
// may exhibit before focal warns the user. 0.01 tolerates minor rounding (e.g.
// 3:2 crops that differ by a pixel) while still catching portrait/landscape mixes.
const aspectRatioTolerance = 0.01

// Timelapser is the core use case. It orchestrates the collaborators required to
// turn a directory of photos into a video, depending only on domain interfaces
// so every collaborator can be injected and independently tested.
type Timelapser struct {
	Scanner  ImageScanner
	Resolver BinaryResolver
	Concat   ConcatBuilder
	Encoder  Encoder
	Log      Logger
}

// NewTimelapser wires the use case from its collaborators. Keeping construction
// explicit (rather than reaching for globals) is what makes the dependency
// injection in cmd/focal and the tests straightforward.
func NewTimelapser(scanner ImageScanner, resolver BinaryResolver, concat ConcatBuilder, encoder Encoder, log Logger) *Timelapser {
	return &Timelapser{
		Scanner:  scanner,
		Resolver: resolver,
		Concat:   concat,
		Encoder:  encoder,
		Log:      log,
	}
}

// Create runs the full timelapse pipeline: scan and order the images, warn on
// inconsistent aspect ratios, resolve FFmpeg, generate the concat file, and
// encode. FFmpeg's stderr is streamed to progress so long encodes remain visible.
func (t *Timelapser) Create(ctx context.Context, req LapseRequest, progress io.Writer) error {
	if err := req.Validate(); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}

	images, err := t.Scanner.Scan(ctx, req.InputDir)
	if err != nil {
		return fmt.Errorf("scanning images: %w", err)
	}
	if len(images) == 0 {
		return fmt.Errorf("no images found in %q", req.InputDir)
	}
	t.Log.Infof("Found %d image(s) in %s", len(images), req.InputDir)

	if varies, min, max := AspectRatioVaries(images, aspectRatioTolerance); varies {
		t.Log.Warnf(
			"aspect ratios vary across the sequence (%.3f–%.3f); the output may be padded or letterboxed",
			min, max,
		)
	}

	// Resolve output sizing: cap oversized sources so the result stays within
	// hardware-decoder limits and plays back smoothly.
	scaleHeight := req.TargetHeight(maxImageHeight(images))
	if scaleHeight > 0 {
		t.Log.Infof("Scaling output to %dp for smooth playback (source exceeds the %dp cap)", scaleHeight, req.MaxHeight)
	}

	ffmpegPath, err := t.Resolver.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("resolving ffmpeg: %w", err)
	}

	concatPath, cleanup, err := t.Concat.Build(images, req.FPS)
	if err != nil {
		return fmt.Errorf("building concat file: %w", err)
	}
	defer func() {
		if cleanup == nil {
			return
		}
		if cErr := cleanup(); cErr != nil {
			t.Log.Warnf("failed to remove temporary file: %v", cErr)
		}
	}()

	t.Log.Infof("Encoding %d frames at %d fps -> %s", len(images), req.FPS, req.OutputPath)
	encReq := EncodeRequest{
		FFmpegPath:     ffmpegPath,
		ConcatFilePath: concatPath,
		OutputPath:     req.OutputPath,
		FPS:            req.FPS,
		TotalFrames:    len(images),
		ScaleHeight:    scaleHeight,
	}
	if err := t.Encoder.Encode(ctx, encReq, progress); err != nil {
		return fmt.Errorf("encoding video: %w", err)
	}

	t.Log.Infof("Timelapse written to %s", req.OutputPath)
	return nil
}
