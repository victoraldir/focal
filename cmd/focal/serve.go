package focal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/victoraldir/focal/pkg/camera/sony"
	"github.com/victoraldir/focal/pkg/server"
)

type serveFlags struct {
	camera   cameraFlags
	addr     string
	media    string
	size     string
	noBrowse bool
}

// newServeCmd builds `focal serve`: the browser studio for live view, remote
// shooting, timelapse capture, and interactive API docs.
func newServeCmd() *cobra.Command {
	var flags serveFlags

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Open a browser studio to see and control the camera",
		Long: `serve starts a local web studio for a Wi-Fi connected Sony camera:

  /        live view, shutter, zoom, focus, exposure settings, timelapse
           capture and rendering, and a library of everything captured
  /docs    interactive API documentation (Swagger UI) for the REST API,
           including a passthrough to the camera's own Remote API

Everything is embedded, so it works while this computer is joined to the
camera's Wi-Fi network (which has no internet). The camera is discovered on
first use, so the studio can be started before joining that network.`,
		Example: `  focal serve
  focal serve --media ~/Pictures/a5000 --addr :8080
  focal serve --simulate                 # explore the UI without a camera`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, flags)
		},
	}

	flags.camera.register(cmd)
	cmd.Flags().StringVar(&flags.addr, "addr", "127.0.0.1:8770", "address to listen on (use 0.0.0.0:8770 to reach the studio from a phone)")
	cmd.Flags().StringVar(&flags.media, "media", "focal-media", "directory for photos, timelapse frames and rendered videos")
	cmd.Flags().StringVar(&flags.size, "size", "original", `transfer size for captures: "original", "2m", or "keep"`)
	cmd.Flags().BoolVar(&flags.noBrowse, "no-browser", false, "do not open the studio in a browser")

	return cmd
}

func runServe(cmd *cobra.Command, flags serveFlags) error {
	log := newConsoleLogger(cmd.ErrOrStderr())

	size, err := parsePostviewSize(flags.size)
	if err != nil {
		return err
	}
	media, err := server.EnsureMediaDir(flags.media)
	if err != nil {
		return fmt.Errorf("preparing media directory: %w", err)
	}
	endpoint, cleanup, err := flags.camera.resolveEndpoint(log)
	if err != nil {
		return err
	}
	defer cleanup()

	srv := server.New(server.Config{
		MediaDir: media,
		Connect: func(ctx context.Context) (*sony.Camera, error) {
			cam, err := sony.Connect(ctx, endpoint, cameraHTTPClient, log)
			if err != nil {
				return nil, err
			}
			cam.PostviewSize = size
			return cam, nil
		},
		Render: renderFunc(log),
		Log:    log,
	})

	ln, err := net.Listen("tcp", flags.addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", flags.addr, err)
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	url := studioURL(ln.Addr())
	log.Infof("focal studio running at %s", url)
	log.Infof("API docs at %s/docs · media in %s", url, media)
	log.Infof("Press Ctrl-C to stop")
	if !flags.noBrowse {
		openBrowser(url)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Live-view streams never end on their own; Close after the grace period.
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	_ = httpSrv.Close()
	log.Infof("Stopped")
	return nil
}

// studioURL turns the listener address into a browsable URL, mapping wildcard
// hosts to localhost.
func studioURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}
