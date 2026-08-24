package ffmpeg

import (
	"context"
	"fmt"
	"io"

	"github.com/victoraldir/focal/pkg/domain"
)

// padFilter forces both output dimensions to the nearest even number. Many
// encoders (notably H.264 with yuv420p) require width and height divisible by 2;
// padding rather than scaling avoids distorting the source frames.
const padFilter = "pad=ceil(iw/2)*2:ceil(ih/2)*2"

// Engine implements domain.Encoder. It builds the FFmpeg argument vector for a
// concat-demuxer encode and delegates process execution to an injected
// domain.FFmpegRunner, so argument construction can be tested without spawning
// FFmpeg.
type Engine struct {
	runner domain.FFmpegRunner
}

// NewEngine constructs an Engine backed by the given runner.
func NewEngine(runner domain.FFmpegRunner) *Engine {
	return &Engine{runner: runner}
}

// Encode assembles the FFmpeg command for req and runs it, streaming FFmpeg's
// stderr (which carries progress) to the provided writer.
func (e *Engine) Encode(ctx context.Context, req domain.EncodeRequest, stderr io.Writer) error {
	if req.FFmpegPath == "" {
		return fmt.Errorf("ffmpeg path is empty")
	}
	args := BuildArgs(req)
	if err := e.runner.Run(ctx, req.FFmpegPath, args, stderr); err != nil {
		return fmt.Errorf("ffmpeg run failed: %w", err)
	}
	return nil
}

// BuildArgs returns the ordered FFmpeg argument vector for a concat-demuxer
// encode. It is exported and pure so tests can assert the exact command shape.
//
//	-y                      overwrite the output without prompting
//	-f concat -safe 0       read the concat demuxer, allowing absolute paths
//	-i <concat file>        the generated frame list
//	-vf <pad filter>        enforce even dimensions
//	-r <fps>                output frame rate
//	-pix_fmt yuv420p        broadly compatible pixel format
//	<output>                destination file
func BuildArgs(req domain.EncodeRequest) []string {
	return []string{
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", req.ConcatFilePath,
		"-vf", padFilter,
		"-r", fmt.Sprintf("%d", req.FPS),
		"-pix_fmt", "yuv420p",
		req.OutputPath,
	}
}
