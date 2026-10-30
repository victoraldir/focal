package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/camera/sony"
	"github.com/victoraldir/focal/pkg/camera/sony/sonysim"
	"github.com/victoraldir/focal/pkg/server"
)

type harness struct {
	srv      *httptest.Server
	media    string
	rendered chan string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sim := sonysim.New()
	sim.CaptureDelay = 0
	camSrv := httptest.NewServer(sim)
	t.Cleanup(camSrv.Close)

	h := &harness{media: t.TempDir(), rendered: make(chan string, 1)}
	s := server.New(server.Config{
		MediaDir: h.media,
		Connect: func(ctx context.Context) (*sony.Camera, error) {
			cam, err := sony.Connect(ctx, camSrv.URL+"/sony", camSrv.Client(), nil)
			if err == nil {
				cam.PostviewSize = "2M" // keep test captures small
			}
			return cam, err
		},
		Render: func(_ context.Context, in, out string, fps, _ int, _ io.Writer) error {
			h.rendered <- in
			return os.WriteFile(out, []byte("mp4"), 0o644)
		},
	})
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
	return v
}

func TestStaticPages(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string]string{
		"/":                             "focal studio",
		"/docs":                         "SwaggerUIBundle",
		"/openapi.yaml":                 "openapi: 3.0.3",
		"/assets/app.js":                "focal studio",
		"/swagger/swagger-ui-bundle.js": "SwaggerUIBundle",
	} {
		resp, body := h.do(t, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("GET %s: status %d, contains %q = %v", path, resp.StatusCode, want, strings.Contains(string(body), want))
		}
	}
	if resp, _ := h.do(t, http.MethodGet, "/api/nope", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown API path: status %d, want 404", resp.StatusCode)
	}
}

func TestStatusConnectsLazilyAndReportsSettings(t *testing.T) {
	h := newHarness(t)

	_, body := h.do(t, http.MethodGet, "/api/camera", nil)
	if info := decode[server.CameraInfo](t, body); info.Connected {
		t.Fatal("GET /api/camera must not trigger discovery")
	}

	resp, body := h.do(t, http.MethodGet, "/api/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	st := decode[sony.Status](t, body)
	if st.CameraStatus != "IDLE" || st.ExposureMode == nil || st.Battery == nil {
		t.Errorf("unexpected status: %s", body)
	}

	_, body = h.do(t, http.MethodGet, "/api/camera", nil)
	if info := decode[server.CameraInfo](t, body); !info.Connected || info.Device == nil {
		t.Errorf("expected connected camera after first use: %s", body)
	}
}

func TestSettings(t *testing.T) {
	h := newHarness(t)

	if resp, body := h.do(t, http.MethodPut, "/api/settings/exposureMode", map[string]any{"value": "Aperture"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set mode: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(t, http.MethodPut, "/api/settings/fNumber", map[string]any{"value": 8.0}); resp.StatusCode != http.StatusBadRequest {
		// 8.0 marshals as the number 8, which is not the camera's "8.0" string.
		t.Errorf("expected the camera's illegal-argument error, got %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(t, http.MethodPut, "/api/settings/fNumber", map[string]any{"value": "8.0"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set fNumber: %d %s", resp.StatusCode, body)
	}
	resp, body := h.do(t, http.MethodPut, "/api/settings/shutterSpeed", map[string]any{"value": "1/500"})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("shutter speed is not settable in Aperture mode: got %d %s", resp.StatusCode, body)
	}
	if e := decode[server.APIErrorBody](t, body); e.CameraCode != sony.ErrCodeAny {
		t.Errorf("expected camera code 1, got %+v", e)
	}
	if resp, _ := h.do(t, http.MethodPut, "/api/settings/warp", map[string]any{"value": "9"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown setting: status %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.do(t, http.MethodPut, "/api/settings/fNumber", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing body: status %d, want 400", resp.StatusCode)
	}
}

func TestCaptureStoresPhotoAndListsIt(t *testing.T) {
	h := newHarness(t)

	resp, body := h.do(t, http.MethodPost, "/api/capture", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capture: %d %s", resp.StatusCode, body)
	}
	m := decode[server.Media](t, body)
	if m.Kind != "photo" || !strings.HasPrefix(m.URL, "/media/DSC-") {
		t.Errorf("unexpected media: %+v", m)
	}

	resp, img := h.do(t, http.MethodGet, m.URL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", m.URL, resp.StatusCode)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(img)); err != nil {
		t.Errorf("served photo is not a JPEG: %v", err)
	}

	_, body = h.do(t, http.MethodGet, "/api/media", nil)
	if list := decode[[]server.Media](t, body); len(list) != 1 || list[0].Path != m.Path {
		t.Errorf("media listing = %s", body)
	}
}

func TestLiveviewServesMJPEG(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/api/liveview", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/x-mixed-replace") {
		t.Fatalf("Content-Type = %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	var length int
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading part headers: %v", err)
		}
		line = strings.TrimSpace(line)
		if n, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			length = atoi(n)
		}
		if line == "" && length > 0 {
			break
		}
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(br, frame); err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(frame)); err != nil {
		t.Errorf("MJPEG part is not a JPEG: %v", err)
	}

	resp2, snap := h.do(t, http.MethodGet, "/api/liveview/snapshot", nil)
	if resp2.StatusCode != http.StatusOK || resp2.Header.Get("Content-Type") != "image/jpeg" || len(snap) == 0 {
		t.Errorf("snapshot: %d %s", resp2.StatusCode, resp2.Header.Get("Content-Type"))
	}
}

func TestZoomAndFocusValidation(t *testing.T) {
	h := newHarness(t)
	if resp, body := h.do(t, http.MethodPost, "/api/zoom", map[string]string{"direction": "in"}); resp.StatusCode != http.StatusNoContent {
		t.Errorf("zoom 1shot: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(t, http.MethodPost, "/api/zoom", map[string]string{"direction": "sideways"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad direction: status %d, want 400", resp.StatusCode)
	}
	if resp, _ := h.do(t, http.MethodPost, "/api/focus/touch", map[string]float64{"x": 150, "y": 10}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("out-of-frame touch: status %d, want 400", resp.StatusCode)
	}
	if resp, body := h.do(t, http.MethodPost, "/api/focus/touch", map[string]float64{"x": 50, "y": 50}); resp.StatusCode != http.StatusNoContent {
		t.Errorf("touch AF: %d %s", resp.StatusCode, body)
	}
}

func TestRPCPassthrough(t *testing.T) {
	h := newHarness(t)

	resp, body := h.do(t, http.MethodPost, "/api/rpc/camera", map[string]any{"method": "getApplicationInfo", "params": []any{}, "version": "1.0"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Smart Remote Control") {
		t.Errorf("rpc result: %d %s", resp.StatusCode, body)
	}

	_, body = h.do(t, http.MethodPost, "/api/rpc/camera", map[string]any{"method": "selfDestruct"})
	if !strings.Contains(string(body), `"error":[12,"No Such Method"]`) {
		t.Errorf("camera errors should be mirrored verbatim: %s", body)
	}

	if resp, _ := h.do(t, http.MethodPost, "/api/rpc/toaster", map[string]any{"method": "x"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown service: status %d, want 400", resp.StatusCode)
	}

	_, body = h.do(t, http.MethodGet, "/api/rpc/methods", nil)
	if ml := decode[server.MethodList](t, body); len(ml.Available) == 0 || len(ml.Settings) == 0 {
		t.Errorf("methods: %s", body)
	}
}

func TestTimelapseLifecycle(t *testing.T) {
	h := newHarness(t)

	if resp, _ := h.do(t, http.MethodPost, "/api/timelapse/render", nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("render before any session: status %d, want 409", resp.StatusCode)
	}
	if resp, _ := h.do(t, http.MethodPost, "/api/timelapse", map[string]any{"intervalSeconds": 0.1, "count": 3}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("too-short interval: status %d, want 400", resp.StatusCode)
	}

	resp, body := h.do(t, http.MethodPost, "/api/timelapse", map[string]any{"intervalSeconds": 0.5, "count": 3})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(t, http.MethodPost, "/api/timelapse", map[string]any{"intervalSeconds": 1}); resp.StatusCode != http.StatusConflict {
		t.Errorf("second session while running: status %d, want 409", resp.StatusCode)
	}

	st := waitFor(t, h, func(st server.TimelapseState) bool { return !st.Running })
	if st.Captured != 3 || st.Error != "" || st.LastFrame == nil {
		t.Fatalf("finished session: %+v", st)
	}
	frames, _ := filepath.Glob(filepath.Join(h.media, st.Session, "frame_*.jpg"))
	if len(frames) != 3 {
		t.Errorf("expected 3 frames on disk, got %v", frames)
	}

	if resp, body := h.do(t, http.MethodPost, "/api/timelapse/render", map[string]int{"fps": 24}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("render: %d %s", resp.StatusCode, body)
	}
	if in := <-h.rendered; filepath.Base(in) != st.Session {
		t.Errorf("rendered %q, want session %q", in, st.Session)
	}
	st = waitFor(t, h, func(st server.TimelapseState) bool { return st.Render != nil && !st.Render.Running })
	if st.Render.Output == nil || st.Render.Output.Kind != "video" || st.Render.FPS != 24 {
		t.Errorf("render state: %+v", st.Render)
	}
}

func TestTimelapseStop(t *testing.T) {
	h := newHarness(t)
	if resp, body := h.do(t, http.MethodPost, "/api/timelapse", map[string]any{"intervalSeconds": 0.5}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	waitFor(t, h, func(st server.TimelapseState) bool { return st.Captured >= 1 })
	h.do(t, http.MethodDelete, "/api/timelapse", nil)
	st := waitFor(t, h, func(st server.TimelapseState) bool { return !st.Running })
	if st.Error != "" || st.FinishedAt == nil {
		t.Errorf("stopped session: %+v", st)
	}
}

func TestNotConnected(t *testing.T) {
	s := server.New(server.Config{
		MediaDir: t.TempDir(),
		Connect: func(context.Context) (*sony.Camera, error) {
			return nil, errors.New("no camera found")
		},
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "no camera found") {
		t.Errorf("status without camera: %d %s", resp.StatusCode, body)
	}

	resp, _ = http.Get(srv.URL + "/api/camera")
	var info server.CameraInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info.Connected || !strings.Contains(info.Error, "no camera found") {
		t.Errorf("camera info should carry the last connection error: %+v", info)
	}
}

func waitFor(t *testing.T, h *harness, cond func(server.TimelapseState) bool) server.TimelapseState {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, body := h.do(t, http.MethodGet, "/api/timelapse", nil)
		st := decode[server.TimelapseState](t, body)
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out; last state: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
