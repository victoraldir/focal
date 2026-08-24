package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"strings"

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

// Encode assembles the FFmpeg command for req and runs it. FFmpeg's
// machine-readable progress (from "-progress pipe:1") is parsed into a progress
// bar rendered to out; its stderr is captured so that, on failure, the tail of
// FFmpeg's own diagnostics is surfaced in the returned error rather than lost.
func (e *Engine) Encode(ctx context.Context, req domain.EncodeRequest, out io.Writer) error {
	if req.FFmpegPath == "" {
		return fmt.Errorf("ffmpeg path is empty")
	}

	renderer := newProgressRenderer(out, req.TotalFrames)
	var errTail tailBuffer

	args := BuildArgs(req)
	runErr := e.runner.Run(ctx, req.FFmpegPath, args, renderer, &errTail)
	renderer.Finish()

	if runErr != nil {
		if tail := strings.TrimSpace(errTail.String()); tail != "" {
			return fmt.Errorf("ffmpeg run failed: %w\n%s", runErr, tail)
		}
		return fmt.Errorf("ffmpeg run failed: %w", runErr)
	}
	return nil
}

// BuildArgs returns the ordered FFmpeg argument vector for a concat-demuxer
// encode. It is exported and pure so tests can assert the exact command shape.
//
//	-y                      overwrite the output without prompting
//	-hide_banner            drop the build/config banner from stderr
//	-nostats                suppress the default stats line (we render our own)
//	-progress pipe:1        emit machine-readable progress on stdout
//	-f concat -safe 0       read the concat demuxer, allowing absolute paths
//	-i <concat file>        the generated frame list
//	-vf <filter>            optional downscale + even-dimension pad
//	-r <fps>                output frame rate
//	-fps_mode cfr           force constant frame rate (no dup/drop jitter)
//	-frames:v <n>           write exactly one frame per source image
//	-pix_fmt yuv420p        broadly compatible pixel format
//	<output>                destination file
func BuildArgs(req domain.EncodeRequest) []string {
	args := []string{
		"-y",
		"-hide_banner",
		"-nostats",
		"-progress", "pipe:1",
		"-f", "concat",
		"-safe", "0",
		"-i", req.ConcatFilePath,
		"-vf", videoFilter(req.ScaleHeight),
		"-r", fmt.Sprintf("%d", req.FPS),
		"-fps_mode", "cfr",
	}
	// Bound the output to exactly one frame per source image, trimming the extra
	// frames the concat demuxer's repeated last entry would otherwise emit.
	if req.TotalFrames > 0 {
		args = append(args, "-frames:v", fmt.Sprintf("%d", req.TotalFrames))
	}
	args = append(args, "-pix_fmt", "yuv420p", req.OutputPath)
	return args
}

// videoFilter builds the -vf graph. It always enforces even dimensions; when
// scaleHeight is positive it first downscales to that height, letting the width
// fall out to the nearest even number (-2) so the aspect ratio is preserved.
func videoFilter(scaleHeight int) string {
	if scaleHeight > 0 {
		return fmt.Sprintf("scale=-2:%d,%s", scaleHeight, padFilter)
	}
	return padFilter
}
