package focal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/victoraldir/focal/pkg/domain"
	"github.com/victoraldir/focal/pkg/framestore"
)

// shootFlags holds the parsed flag values for the shoot command.
type shootFlags struct {
	camera    cameraFlags
	output    string
	interval  time.Duration
	count     int
	duration  time.Duration
	size      string
	lapse     string
	fps       int
	maxHeight int
}

// newShootCmd builds `focal shoot`: interval capture from a Wi-Fi-connected
// Sony camera, optionally followed by rendering the frames into a video.
func newShootCmd() *cobra.Command {
	var flags shootFlags

	cmd := &cobra.Command{
		Use:   "shoot",
		Short: "Capture a timelapse sequence from a Wi-Fi connected Sony camera",
		Long: `shoot fires the camera's shutter on a fixed interval over Wi-Fi and saves every
frame to a directory, ready for "focal lapse". Pass --lapse to render the video
as soon as capture ends.

Works with Sony bodies that run the built-in Smart Remote app (a5000, a6000,
NEX-5T/6, RX100 III/IV, QX10/100 and similar). Before running:
  1. On the camera: Menu → Application List → Smart Remote Embedded.
  2. Join the Wi-Fi network the camera displays (DIRECT-xxxx) from this computer.

Press Ctrl-C to stop early. Frames captured so far are kept, and --lapse still
renders them.`,
		Example: `  focal shoot -t 5s -n 300 -o ./sunset
  focal shoot -t 10s -d 1h --lapse sunset.mp4
  focal shoot -t 2s --size 2m            # faster transfers, ~2 MP frames
  focal shoot --simulate -t 1s -n 20     # try it without a camera`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runShoot(cmd, flags)
		},
	}

	flags.camera.register(cmd)
	cmd.Flags().StringVarP(&flags.output, "output", "o", "", "directory for captured frames (default: ./shoot-YYYYMMDD-HHMMSS)")
	cmd.Flags().DurationVarP(&flags.interval, "interval", "t", 5*time.Second, "time between shots, e.g. 2s, 30s, 1m")
	cmd.Flags().IntVarP(&flags.count, "count", "n", 0, "number of frames to capture (0 = until --duration or Ctrl-C)")
	cmd.Flags().DurationVarP(&flags.duration, "duration", "d", 0, "how long to shoot, e.g. 45m, 2h (0 = until --count or Ctrl-C)")
	cmd.Flags().StringVar(&flags.size, "size", "original", `transfer size: "original" (full resolution), "2m" (~2 MP, faster), or "keep"`)
	cmd.Flags().StringVar(&flags.lapse, "lapse", "", "render the frames to this video file when capture ends")
	cmd.Flags().IntVarP(&flags.fps, "fps", "f", 30, "frames per second for --lapse")
	cmd.Flags().IntVar(&flags.maxHeight, "max-height", defaultMaxHeight, "cap --lapse output height in pixels; 0 keeps source resolution")

	return cmd
}

func runShoot(cmd *cobra.Command, flags shootFlags) error {
	log := newConsoleLogger(cmd.ErrOrStderr())
	out := cmd.ErrOrStderr()

	size, err := parsePostviewSize(flags.size)
	if err != nil {
		return err
	}
	if flags.output == "" {
		flags.output = "shoot-" + time.Now().Format("20060102-150405")
	}
	req := domain.ShootRequest{OutputDir: flags.output, Interval: flags.interval, Count: flags.count, Duration: flags.duration}
	if err := req.Validate(); err != nil {
		return err
	}
	if flags.lapse != "" {
		if err := (domain.LapseRequest{InputDir: flags.output, OutputPath: flags.lapse, FPS: flags.fps, MaxHeight: flags.maxHeight}).Validate(); err != nil {
			return err
		}
	}

	// The first Ctrl-C ends capture gracefully; rendering below gets its own
	// signal context so a second Ctrl-C can still abort the encode.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint, cleanup, err := flags.camera.resolveEndpoint(log)
	if err != nil {
		return err
	}
	defer cleanup()
	cam, err := connectCamera(ctx, endpoint, log)
	if err != nil {
		return err
	}
	cam.PostviewSize = size

	log.Infof("%s → %s  (Ctrl-C to stop)", describePlan(req), flags.output)
	shooter := domain.NewShooter(cam, framestore.New(flags.output), log)
	summary, err := shooter.Run(ctx, req, func(ev domain.ShotEvent) { printShot(out, ev) })
	stop()
	if err != nil && summary.Captured == 0 {
		return err
	}
	// A session that captured frames and then gave up still renders below;
	// err is returned at the end so the exit status reflects the failure.

	verb := "Captured"
	if summary.Stopped {
		verb = "Stopped after capturing"
	}
	log.Infof("%s %d frame(s) in %s → %s", verb, summary.Captured, summary.Elapsed.Round(time.Second), flags.output)
	if summary.Failed > 0 {
		log.Warnf("%d shot(s) failed", summary.Failed)
	}
	if summary.Captured == 0 {
		return nil
	}

	if flags.lapse == "" {
		log.Infof("Render it with: focal lapse -i %s -o timelapse.mp4", shellQuote(flags.output))
		return err
	}
	renderCtx, stopRender := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopRender()
	lapseReq := domain.LapseRequest{InputDir: flags.output, OutputPath: flags.lapse, FPS: flags.fps, MaxHeight: flags.maxHeight}
	if rerr := newTimelapser(log).Create(renderCtx, lapseReq, out); rerr != nil {
		return rerr
	}
	return err
}

// describePlan renders a one-line summary of the session.
func describePlan(req domain.ShootRequest) string {
	total := req.TotalFrames()
	if total == 0 {
		return fmt.Sprintf("Shooting every %s until stopped", req.Interval)
	}
	length := time.Duration(total) * req.Interval
	return fmt.Sprintf("Shooting %d frames every %s (~%s)", total, req.Interval, length.Round(time.Second))
}

// printShot writes one progress line per frame.
func printShot(w io.Writer, ev domain.ShotEvent) {
	counter := fmt.Sprintf("[%d]", ev.Index)
	if ev.Total > 0 {
		width := len(fmt.Sprint(ev.Total))
		counter = fmt.Sprintf("[%*d/%d]", width, ev.Index, ev.Total)
	}
	if ev.Err != nil {
		fmt.Fprintf(w, "⚠ %s %v\n", counter, ev.Err)
		return
	}
	line := fmt.Sprintf("📸 %s %s  %s", counter, filepath.Base(ev.Path), humanBytes(ev.Bytes))
	if !ev.Next.IsZero() {
		if wait := time.Until(ev.Next); wait > 0 {
			line += fmt.Sprintf("  · next in %.1fs", wait.Seconds())
		} else {
			line += "  · behind schedule"
		}
	}
	fmt.Fprintln(w, line)
}

func humanBytes(n int) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func shellQuote(s string) string {
	for _, r := range s {
		if r == ' ' || r == '\'' || r == '"' || r == '$' {
			return "'" + s + "'"
		}
	}
	return s
}

// renderFunc adapts the lapse pipeline to the studio's RenderFunc.
func renderFunc(log domain.Logger) func(ctx context.Context, in, out string, fps, maxHeight int, progress io.Writer) error {
	return func(ctx context.Context, in, out string, fps, maxHeight int, progress io.Writer) error {
		req := domain.LapseRequest{InputDir: in, OutputPath: out, FPS: fps, MaxHeight: maxHeight}
		return newTimelapser(log).Create(ctx, req, progress)
	}
}
