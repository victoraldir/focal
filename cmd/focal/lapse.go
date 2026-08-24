package focal

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/victoraldir/focal/pkg/domain"
	"github.com/victoraldir/focal/pkg/ffmpeg"
	"github.com/victoraldir/focal/pkg/resolver"
	"github.com/victoraldir/focal/pkg/scanner"
)

// lapseFlags holds the parsed flag values for the lapse command.
type lapseFlags struct {
	input     string
	output    string
	fps       int
	maxHeight int
}

// defaultMaxHeight caps output at 4K (2160p) by default. Full-sensor stills
// (e.g. 6000x3376) otherwise yield 6K video that exceeds every hardware H.264
// decoder's limit and stutters on playback; 2160p decodes in hardware
// everywhere while preserving quality.
const defaultMaxHeight = 2160

// newLapseCmd builds the `focal lapse` subcommand. Construction of the use case
// and its dependencies happens per-invocation in RunE, which keeps the command
// free of package-level state and easy to reason about.
func newLapseCmd() *cobra.Command {
	var flags lapseFlags

	cmd := &cobra.Command{
		Use:   "lapse",
		Short: "Build a timelapse video from a directory of photos",
		Long: `lapse scans a directory of photos, orders them chronologically using EXIF
metadata, and encodes them into a timelapse video with FFmpeg.`,
		Example: "  focal lapse -i ./photos -o timelapse.mp4 -f 24",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLapse(cmd, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.input, "input", "i", "", "directory containing the source photos (required)")
	cmd.Flags().StringVarP(&flags.output, "output", "o", "timelapse.mp4", "output video file path")
	cmd.Flags().IntVarP(&flags.fps, "fps", "f", 30, "output frames per second")
	cmd.Flags().IntVar(&flags.maxHeight, "max-height", defaultMaxHeight, "cap output height in pixels for smooth playback; 0 keeps source resolution")
	_ = cmd.MarkFlagRequired("input")

	return cmd
}

// runLapse is the composition root for a single lapse run: it builds the
// adapters, injects them into the domain use case, wires signal-based
// cancellation, and executes.
func runLapse(cmd *cobra.Command, flags lapseFlags) error {
	log := newConsoleLogger(cmd.ErrOrStderr())

	// Assemble the dependency graph. Every collaborator is an interface from the
	// domain's perspective; only this function knows the concrete types.
	exif := scanner.NewGoExifExtractor()
	imageScanner := scanner.New(exif)
	downloader := resolver.NewHTTPDownloader(nil)
	binaryResolver := resolver.New(downloader, log)
	concat := ffmpeg.NewConcatGenerator()
	engine := ffmpeg.NewEngine(ffmpeg.NewExecRunner())

	timelapser := domain.NewTimelapser(imageScanner, binaryResolver, concat, engine, log)

	// Cancel the pipeline (and any in-flight FFmpeg process) on Ctrl-C or SIGTERM.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	req := domain.LapseRequest{
		InputDir:   flags.input,
		OutputPath: flags.output,
		FPS:        flags.fps,
		MaxHeight:  flags.maxHeight,
	}
	// FFmpeg writes progress to stderr; forward it so long encodes stay visible.
	return timelapser.Create(ctx, req, cmd.ErrOrStderr())
}
