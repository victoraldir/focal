package sony

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/victoraldir/focal/pkg/domain"
)

// defaultCallTimeout bounds ordinary RPCs. Shutter-related calls get longer
// budgets because a 30" exposure plus in-camera processing takes a while.
const (
	defaultCallTimeout = 10 * time.Second
	captureTimeout     = 90 * time.Second
	readyTimeout       = 15 * time.Second
	maxPhotoBytes      = 64 << 20
)

// Camera is a typed facade over the Camera Remote API. It implements
// domain.Camera and is safe for concurrent use: the API itself tolerates
// parallel calls (the documented pattern is a long-poll getEvent alongside
// commands), so Camera only guards its own cached state.
type Camera struct {
	client *Client
	device Device
	http   *http.Client
	log    domain.Logger

	// PostviewSize is the image size Prepare asks for: "Original" transfers the
	// full-resolution JPEG; "2M" is a ~2 MP preview that transfers much faster.
	// When the body does not offer the requested size, Prepare keeps the
	// current one and logs a warning.
	PostviewSize string

	mu           sync.Mutex
	eventVersion string
	wbCandidates []string
}

// New returns a Camera bound to dev. hc defaults to http.DefaultClient and log
// may be nil.
func New(dev Device, hc *http.Client, log domain.Logger) *Camera {
	if hc == nil {
		hc = http.DefaultClient
	}
	if log == nil {
		log = nopLogger{}
	}
	return &Camera{client: NewClient(dev.BaseURL, hc), device: dev, http: hc, log: log, PostviewSize: "Original"}
}

// Connect resolves a camera — from baseURL when given, otherwise by discovery
// — and returns it ready to Prepare.
func Connect(ctx context.Context, baseURL string, hc *http.Client, log domain.Logger) (*Camera, error) {
	var (
		dev Device
		err error
	)
	if baseURL != "" {
		dev, err = Probe(ctx, hc, baseURL)
		if err != nil {
			return nil, fmt.Errorf("camera at %s did not answer: %w", baseURL, err)
		}
	} else {
		dev, err = Discover(ctx, hc, 3*time.Second)
		if err != nil {
			return nil, err
		}
	}
	return New(dev, hc, log), nil
}

// Device returns what discovery learned about the camera.
func (c *Camera) Device() Device { return c.device }

// Client exposes the underlying JSON-RPC client for raw passthrough calls.
func (c *Camera) Client() *Client { return c.client }

// call invokes a camera-service method at version 1.0, applying the default
// timeout when ctx carries no deadline.
func (c *Camera) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	return c.callTimeout(ctx, defaultCallTimeout, method, params...)
}

func (c *Camera) callTimeout(ctx context.Context, d time.Duration, method string, params ...any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	return c.client.Call(ctx, ServiceCamera, method, params, "1.0")
}

// AvailableAPIs lists the methods the camera accepts in its current state. The
// list changes with mode: most shooting methods only appear after startRecMode.
func (c *Camera) AvailableAPIs(ctx context.Context) ([]string, error) {
	raw, err := c.call(ctx, "getAvailableApiList")
	if err != nil {
		return nil, err
	}
	var res [][]string
	if err := json.Unmarshal(raw, &res); err != nil || len(res) == 0 {
		return nil, fmt.Errorf("unexpected getAvailableApiList result: %s", raw)
	}
	return res[0], nil
}

// Prepare puts the camera in still-shooting remote mode: it enters rec mode if
// needed (bodies like the a5000 boot into a restricted state), switches to
// still shooting, applies PostviewSize, and waits for the body to go idle.
func (c *Camera) Prepare(ctx context.Context) error {
	apis, err := c.AvailableAPIs(ctx)
	if err != nil {
		return fmt.Errorf("querying camera: %w", err)
	}
	if !slices.Contains(apis, "actTakePicture") && slices.Contains(apis, "startRecMode") {
		if _, err := c.call(ctx, "startRecMode"); err != nil {
			return err
		}
		if apis, err = c.waitForAPI(ctx, "actTakePicture"); err != nil {
			return err
		}
	}
	if !slices.Contains(apis, "actTakePicture") {
		return errors.New("camera does not offer remote shooting in its current state")
	}

	if slices.Contains(apis, "setShootMode") {
		if cur, _, err := c.choice(ctx, "getAvailableShootMode"); err == nil && cur != "still" {
			if _, err := c.call(ctx, "setShootMode", "still"); err != nil {
				return err
			}
		}
	}
	if c.PostviewSize != "" && slices.Contains(apis, "setPostviewImageSize") {
		cur, cands, err := c.choice(ctx, "getAvailablePostviewImageSize")
		switch {
		case err != nil:
			c.log.Warnf("could not read postview size: %v", err)
		case cur == c.PostviewSize:
		case slices.Contains(cands, c.PostviewSize):
			if _, err := c.call(ctx, "setPostviewImageSize", c.PostviewSize); err != nil {
				return err
			}
		default:
			c.log.Warnf("camera does not offer %q transfer size (offers %s); keeping %q", c.PostviewSize, strings.Join(cands, ", "), cur)
		}
	}
	return c.waitIdle(ctx)
}

// waitForAPI polls until method shows up in the available-API list.
func (c *Camera) waitForAPI(ctx context.Context, method string) ([]string, error) {
	deadline := time.Now().Add(readyTimeout)
	for {
		apis, err := c.AvailableAPIs(ctx)
		if err == nil && slices.Contains(apis, method) {
			return apis, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("camera did not make %s available within %s", method, readyTimeout)
		}
		if err := sleepCtx(ctx, 300*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// waitIdle polls the event feed until the body reports IDLE. Bodies that do not
// answer getEvent are assumed ready.
func (c *Camera) waitIdle(ctx context.Context) error {
	deadline := time.Now().Add(readyTimeout)
	for {
		st, err := c.Status(ctx)
		if err != nil || st.CameraStatus == "" || st.CameraStatus == "IDLE" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("camera still %q after %s", st.CameraStatus, readyTimeout)
		}
		if err := sleepCtx(ctx, 300*time.Millisecond); err != nil {
			return err
		}
	}
}

// TakePicture fires the shutter and returns the URL of the resulting postview
// image. It follows the API's protocol for long exposures (40403 → poll
// awaitTakePicture) and briefly retries while the body reports "not ready".
func (c *Camera) TakePicture(ctx context.Context) (string, error) {
	raw, err := c.callTimeout(ctx, captureTimeout, "actTakePicture")
	for attempt := 0; IsAPIError(err, ErrCodeCameraNotReady) && attempt < 10; attempt++ {
		if serr := sleepCtx(ctx, 300*time.Millisecond); serr != nil {
			return "", serr
		}
		raw, err = c.callTimeout(ctx, captureTimeout, "actTakePicture")
	}
	for IsAPIError(err, ErrCodeStillCapturing) {
		raw, err = c.callTimeout(ctx, captureTimeout, "awaitTakePicture")
	}
	if err != nil {
		return "", err
	}
	var res [][]string
	if err := json.Unmarshal(raw, &res); err != nil || len(res) == 0 || len(res[0]) == 0 {
		return "", fmt.Errorf("unexpected actTakePicture result: %s", raw)
	}
	return res[0][0], nil
}

// Fetch downloads an image the camera serves (postview, live view snapshot).
func (c *Camera) Fetch(ctx context.Context, url string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, captureTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading image: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes))
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	return data, nil
}

// Capture implements domain.Camera: shoot, then download the postview image.
func (c *Camera) Capture(ctx context.Context) (domain.Photo, error) {
	url, err := c.TakePicture(ctx)
	if err != nil {
		return domain.Photo{}, err
	}
	data, err := c.Fetch(ctx, url)
	if err != nil {
		return domain.Photo{}, err
	}
	ext := strings.ToLower(path.Ext(strings.SplitN(url, "?", 2)[0]))
	if ext == "" || ext == ".jpeg" {
		ext = ".jpg"
	}
	return domain.Photo{Data: data, Ext: ext}, nil
}

// StartLiveview starts the live-view stream and returns its URL.
func (c *Camera) StartLiveview(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "startLiveview")
	if err != nil {
		return "", err
	}
	var res []string
	if err := json.Unmarshal(raw, &res); err != nil || len(res) == 0 {
		return "", fmt.Errorf("unexpected startLiveview result: %s", raw)
	}
	return res[0], nil
}

// StopLiveview stops the live-view stream.
func (c *Camera) StopLiveview(ctx context.Context) error {
	_, err := c.call(ctx, "stopLiveview")
	return err
}

// OpenLiveview starts live view and opens its packet stream. The caller must
// Close the returned reader; cancelling ctx also ends the stream.
func (c *Camera) OpenLiveview(ctx context.Context) (*LiveviewReader, error) {
	url, err := c.StartLiveview(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opening live view: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("opening live view: unexpected status %s", resp.Status)
	}
	return NewLiveviewReader(resp.Body), nil
}

// Zoom drives the power zoom. direction is "in" or "out"; movement is "start",
// "stop", or "1shot".
func (c *Camera) Zoom(ctx context.Context, direction, movement string) error {
	_, err := c.call(ctx, "actZoom", direction, movement)
	return err
}

// HalfPress focuses as if the shutter button were half-pressed.
func (c *Camera) HalfPress(ctx context.Context) error {
	_, err := c.callTimeout(ctx, 20*time.Second, "actHalfPressShutter")
	return err
}

// CancelHalfPress releases a half-press.
func (c *Camera) CancelHalfPress(ctx context.Context) error {
	_, err := c.call(ctx, "cancelHalfPressShutter")
	return err
}

// TouchAF focuses at (x, y), given as percentages of the live-view frame.
func (c *Camera) TouchAF(ctx context.Context, x, y float64) error {
	_, err := c.callTimeout(ctx, 20*time.Second, "setTouchAFPosition", x, y)
	return err
}

// Setting names accepted by Set. They match the keys of Status.
const (
	SettingShootMode            = "shootMode"
	SettingExposureMode         = "exposureMode"
	SettingFNumber              = "fNumber"
	SettingShutterSpeed         = "shutterSpeed"
	SettingISO                  = "isoSpeedRate"
	SettingExposureCompensation = "exposureCompensation"
	SettingWhiteBalance         = "whiteBalance"
	SettingFocusMode            = "focusMode"
	SettingFlashMode            = "flashMode"
	SettingSelfTimer            = "selfTimer"
	SettingPostviewImageSize    = "postviewImageSize"
)

// settingSetters maps a setting to its API method and a function building the
// method's params from the string value supplied by the user.
var settingSetters = map[string]struct {
	method string
	params func(v string) ([]any, error)
}{
	SettingShootMode:            {"setShootMode", stringParam},
	SettingExposureMode:         {"setExposureMode", stringParam},
	SettingFNumber:              {"setFNumber", stringParam},
	SettingShutterSpeed:         {"setShutterSpeed", stringParam},
	SettingISO:                  {"setIsoSpeedRate", stringParam},
	SettingExposureCompensation: {"setExposureCompensation", intParam},
	SettingWhiteBalance: {"setWhiteBalance", func(v string) ([]any, error) {
		return []any{v, false, -1}, nil
	}},
	SettingFocusMode:         {"setFocusMode", stringParam},
	SettingFlashMode:         {"setFlashMode", stringParam},
	SettingSelfTimer:         {"setSelfTimer", intParam},
	SettingPostviewImageSize: {"setPostviewImageSize", stringParam},
}

// Settings lists the names accepted by Set, sorted.
func Settings() []string {
	names := make([]string, 0, len(settingSetters))
	for name := range settingSetters {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func stringParam(v string) ([]any, error) { return []any{v}, nil }

func intParam(v string) ([]any, error) {
	var n int
	if _, err := fmt.Sscan(v, &n); err != nil {
		return nil, fmt.Errorf("expected an integer, got %q", v)
	}
	return []any{n}, nil
}

// ErrUnknownSetting is returned by Set for names not listed by Settings.
var ErrUnknownSetting = errors.New("unknown setting")

// Set changes one shooting setting. Values are the camera's own strings (e.g.
// shutterSpeed "1/125", fNumber "5.6", isoSpeedRate "AUTO"); integer settings
// (exposureCompensation as a step index, selfTimer in seconds) are passed as
// decimal strings.
func (c *Camera) Set(ctx context.Context, name, value string) error {
	s, ok := settingSetters[name]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownSetting, name)
	}
	params, err := s.params(value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	_, err = c.call(ctx, s.method, params...)
	return err
}

// choice calls a getAvailableX method shaped as [current, [candidates...]].
func (c *Camera) choice(ctx context.Context, method string) (string, []string, error) {
	raw, err := c.call(ctx, method)
	if err != nil {
		return "", nil, err
	}
	var res []json.RawMessage
	if err := json.Unmarshal(raw, &res); err != nil || len(res) == 0 {
		return "", nil, fmt.Errorf("unexpected %s result: %s", method, raw)
	}
	cur := scalarString(res[0])
	var cands []string
	if len(res) > 1 {
		var list []json.RawMessage
		_ = json.Unmarshal(res[1], &list)
		for _, item := range list {
			cands = append(cands, scalarString(item))
		}
	}
	return cur, cands, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type nopLogger struct{}

func (nopLogger) Infof(string, ...any) {}
func (nopLogger) Warnf(string, ...any) {}
