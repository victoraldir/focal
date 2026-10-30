package focal

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/victoraldir/focal/pkg/camera/sony"
	"github.com/victoraldir/focal/pkg/camera/sony/sonysim"
	"github.com/victoraldir/focal/pkg/domain"
)

// cameraFlags are shared by every command that talks to a camera.
type cameraFlags struct {
	endpoint string
	simulate bool
}

func (f *cameraFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.endpoint, "camera", "", "camera API URL, e.g. http://192.168.122.1:8080/sony (default: auto-discover)")
	cmd.Flags().BoolVar(&f.simulate, "simulate", false, "use a built-in simulated camera instead of real hardware")
}

// cameraHTTPClient is used for every camera request. Per-call contexts carry
// the timeouts; the transport only bounds connection setup so an absent camera
// fails fast instead of hanging on a dead Wi-Fi link.
var cameraHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		ResponseHeaderTimeout: 0, // live view and long exposures stream for a long time
		MaxIdleConnsPerHost:   4,
	},
}

// resolveEndpoint starts the simulator when requested and returns the camera
// URL to use ("" means auto-discover) plus a cleanup func.
func (f *cameraFlags) resolveEndpoint(log domain.Logger) (string, func(), error) {
	if !f.simulate {
		return f.endpoint, func() {}, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("starting simulator: %w", err)
	}
	srv := &http.Server{Handler: sonysim.New(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	url := "http://" + ln.Addr().String() + "/sony"
	log.Infof("Using simulated camera at %s", url)
	return url, func() { _ = srv.Close() }, nil
}

// connectCamera resolves the camera, logging what was found.
func connectCamera(ctx context.Context, endpoint string, log domain.Logger) (*sony.Camera, error) {
	if endpoint == "" {
		log.Infof("Looking for a camera on the network…")
	}
	cam, err := sony.Connect(ctx, endpoint, cameraHTTPClient, log)
	if err != nil {
		return nil, err
	}
	name := cam.Device().FriendlyName
	if name == "" {
		name = "camera"
	}
	log.Infof("Connected to %s at %s", name, cam.Device().BaseURL)
	return cam, nil
}

// parsePostviewSize maps the --size flag to the API's postview size names.
func parsePostviewSize(v string) (string, error) {
	switch strings.ToLower(v) {
	case "original", "full":
		return "Original", nil
	case "2m", "preview":
		return "2M", nil
	case "", "keep":
		return "", nil
	}
	return "", fmt.Errorf(`--size must be "original", "2m" or "keep", got %q`, v)
}
