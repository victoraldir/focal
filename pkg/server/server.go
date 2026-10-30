// Package server is focal's HTTP studio: a browser UI for live view, shooting
// and timelapse capture, a REST API over the camera, and interactive OpenAPI
// documentation for that API. Every asset is embedded so the studio works
// while the laptop is joined to the camera's own (internet-less) Wi-Fi.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/victoraldir/focal/pkg/camera/sony"
	"github.com/victoraldir/focal/pkg/domain"
	"github.com/victoraldir/focal/pkg/framestore"
)

//go:embed web
var webFS embed.FS

// ConnectFunc resolves the camera, e.g. via SSDP discovery.
type ConnectFunc func(ctx context.Context) (*sony.Camera, error)

// Config wires the server's collaborators.
type Config struct {
	// MediaDir receives captured photos, timelapse sessions and renders.
	MediaDir string
	// Connect resolves the camera. It is called lazily and again after the
	// connection is lost, so the studio can start before the laptop joins the
	// camera's Wi-Fi.
	Connect ConnectFunc
	// Render encodes a session into a video; nil disables rendering.
	Render RenderFunc
	Log    domain.Logger
}

// Server serves the studio UI and API.
type Server struct {
	cfg  Config
	hub  *liveHub
	tl   *timelapseJob
	mux  *http.ServeMux
	shot sync.Mutex // serialises manual captures

	mu         sync.Mutex
	cam        *sony.Camera
	connectErr error
}

// New builds a Server.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.hub = newLiveHub(func(ctx context.Context) (*sony.LiveviewReader, error) {
		cam, err := s.camera(ctx)
		if err != nil {
			return nil, err
		}
		lv, err := cam.OpenLiveview(ctx)
		s.noteErr(err)
		return lv, err
	})
	s.tl = &timelapseJob{
		mediaDir: cfg.MediaDir,
		camera: func(ctx context.Context) (domain.Camera, error) {
			return s.camera(ctx)
		},
		render: cfg.Render,
	}
	s.routes()
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	web, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(web))

	s.mux.HandleFunc("GET /{$}", serveFile(web, "index.html"))
	s.mux.HandleFunc("GET /docs", serveFile(web, "docs.html"))
	s.mux.HandleFunc("GET /openapi.yaml", serveFile(web, "openapi.yaml"))
	s.mux.Handle("GET /assets/", static)
	s.mux.Handle("GET /swagger/", static)
	s.mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir(s.cfg.MediaDir))))

	s.mux.HandleFunc("GET /api/camera", s.handleCamera)
	s.mux.HandleFunc("POST /api/camera/connect", s.handleConnect)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("PUT /api/settings/{name}", s.handleSetting)
	s.mux.HandleFunc("POST /api/capture", s.handleCapture)
	s.mux.HandleFunc("GET /api/liveview", s.handleLiveview)
	s.mux.HandleFunc("GET /api/liveview/snapshot", s.handleSnapshot)
	s.mux.HandleFunc("POST /api/zoom", s.handleZoom)
	s.mux.HandleFunc("POST /api/focus/half-press", s.handleHalfPress)
	s.mux.HandleFunc("POST /api/focus/release", s.handleRelease)
	s.mux.HandleFunc("POST /api/focus/touch", s.handleTouch)
	s.mux.HandleFunc("GET /api/media", s.handleMedia)
	s.mux.HandleFunc("GET /api/timelapse", s.handleTimelapse)
	s.mux.HandleFunc("POST /api/timelapse", s.handleTimelapseStart)
	s.mux.HandleFunc("DELETE /api/timelapse", s.handleTimelapseStop)
	s.mux.HandleFunc("POST /api/timelapse/render", s.handleRender)
	s.mux.HandleFunc("GET /api/rpc/methods", s.handleMethods)
	s.mux.HandleFunc("POST /api/rpc/{service}", s.handleRPC)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("no such endpoint; see /docs"))
	})
}

func serveFile(fsys fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, fsys, name)
	}
}

// --- camera connection -------------------------------------------------------

var errNotConnected = errors.New("camera not connected")

// camera returns the connected camera, connecting (and entering remote
// shooting mode) on first use.
func (s *Server) camera(ctx context.Context) (*sony.Camera, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cam != nil {
		return s.cam, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cam, err := s.cfg.Connect(cctx)
	if err == nil {
		err = cam.Prepare(cctx)
	}
	if err != nil {
		s.connectErr = err
		return nil, fmt.Errorf("%w: %v", errNotConnected, err)
	}
	s.cam, s.connectErr = cam, nil
	if s.cfg.Log != nil {
		s.cfg.Log.Infof("Connected to %s at %s", displayName(cam.Device()), cam.Device().BaseURL)
	}
	return cam, nil
}

// noteErr drops the cached camera after a transport failure (as opposed to
// the camera answering with an API error) so the next request rediscovers it,
// e.g. after the laptop re-joins the camera's Wi-Fi.
func (s *Server) noteErr(err error) {
	var apiErr *sony.APIError
	if err == nil || errors.As(err, &apiErr) || errors.Is(err, context.Canceled) {
		return
	}
	s.mu.Lock()
	s.cam = nil
	s.mu.Unlock()
}

func displayName(d sony.Device) string {
	if d.FriendlyName != "" {
		return d.FriendlyName
	}
	return "Sony camera"
}

// CameraInfo describes the connection.
type CameraInfo struct {
	Connected bool         `json:"connected"`
	Device    *sony.Device `json:"device,omitempty"`
	Error     string       `json:"error,omitempty"`
}

func (s *Server) cameraInfo() CameraInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cam != nil {
		d := s.cam.Device()
		if d.FriendlyName == "" {
			d.FriendlyName = displayName(d)
		}
		return CameraInfo{Connected: true, Device: &d}
	}
	info := CameraInfo{}
	if s.connectErr != nil {
		info.Error = s.connectErr.Error()
	}
	return info
}

func (s *Server) handleCamera(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cameraInfo())
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.cam = nil
	s.mu.Unlock()
	if _, err := s.camera(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cameraInfo())
}

// withCamera runs fn against the connected camera and maps failures to HTTP
// errors.
func (s *Server) withCamera(w http.ResponseWriter, r *http.Request, fn func(*sony.Camera) error) bool {
	cam, err := s.camera(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return false
	}
	if err := fn(cam); err != nil {
		s.noteErr(err)
		writeCameraError(w, err)
		return false
	}
	return true
}

// --- handlers ----------------------------------------------------------------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var st sony.Status
	if s.withCamera(w, r, func(cam *sony.Camera) (err error) {
		st, err = cam.Status(r.Context())
		return err
	}) {
		writeJSON(w, http.StatusOK, st)
	}
}

type settingBody struct {
	Value json.RawMessage `json:"value"`
}

func (s *Server) handleSetting(w http.ResponseWriter, r *http.Request) {
	var body settingBody
	if err := decodeJSON(r, &body); err != nil || len(body.Value) == 0 {
		writeError(w, http.StatusBadRequest, errors.New(`expected a JSON body like {"value": "1/125"}`))
		return
	}
	value := rawScalar(body.Value)
	if s.withCamera(w, r, func(cam *sony.Camera) error {
		return cam.Set(r.Context(), r.PathValue("name"), value)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// rawScalar accepts both "5.6" and 5.6 so API clients need not quote numbers.
func rawScalar(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return string(raw)
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	s.shot.Lock()
	defer s.shot.Unlock()
	var photo domain.Photo
	if !s.withCamera(w, r, func(cam *sony.Camera) (err error) {
		photo, err = cam.Capture(r.Context())
		return err
	}) {
		return
	}
	now := time.Now()
	name := "DSC-" + now.Format("20060102-150405.000") + photo.Ext
	path, err := framestore.New(s.cfg.MediaDir).Save(name, photo.Data, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	m, _ := newMedia(s.cfg.MediaDir, path, info)
	writeJSON(w, http.StatusCreated, m)
}

const mjpegBoundary = "focalframe"

func (s *Server) handleLiveview(w http.ResponseWriter, r *http.Request) {
	if _, err := s.camera(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	frames, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mjpegBoundary)
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "close")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", mjpegBoundary, len(frame)); err != nil {
				return
			}
			if _, err := w.Write(append(frame, '\r', '\n')); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, err := s.camera(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	frame, ok := s.hub.snapshot(r.Context())
	if !ok {
		writeError(w, http.StatusBadGateway, errors.New("no live-view frame received from the camera"))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(frame)
}

type zoomBody struct {
	Direction string `json:"direction"`
	Movement  string `json:"movement"`
}

func (s *Server) handleZoom(w http.ResponseWriter, r *http.Request) {
	var body zoomBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Movement == "" {
		body.Movement = "1shot"
	}
	if (body.Direction != "in" && body.Direction != "out") || (body.Movement != "start" && body.Movement != "stop" && body.Movement != "1shot") {
		writeError(w, http.StatusBadRequest, errors.New(`direction must be "in" or "out"; movement must be "start", "stop" or "1shot"`))
		return
	}
	if s.withCamera(w, r, func(cam *sony.Camera) error {
		return cam.Zoom(r.Context(), body.Direction, body.Movement)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleHalfPress(w http.ResponseWriter, r *http.Request) {
	if s.withCamera(w, r, func(cam *sony.Camera) error { return cam.HalfPress(r.Context()) }) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	if s.withCamera(w, r, func(cam *sony.Camera) error { return cam.CancelHalfPress(r.Context()) }) {
		w.WriteHeader(http.StatusNoContent)
	}
}

type touchBody struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func (s *Server) handleTouch(w http.ResponseWriter, r *http.Request) {
	var body touchBody
	if err := decodeJSON(r, &body); err != nil || body.X < 0 || body.X > 100 || body.Y < 0 || body.Y > 100 {
		writeError(w, http.StatusBadRequest, errors.New("x and y are percentages of the frame (0–100)"))
		return
	}
	if s.withCamera(w, r, func(cam *sony.Camera) error { return cam.TouchAF(r.Context(), body.X, body.Y) }) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	items, err := listMedia(s.cfg.MediaDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []Media{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleTimelapse(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tl.snapshot())
}

func (s *Server) handleTimelapseStart(w http.ResponseWriter, r *http.Request) {
	var req TimelapseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.tl.start(req)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleTimelapseStop(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tl.stop())
}

func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	var req RenderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.tl.startRender(req)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// MethodList is the camera's current API surface.
type MethodList struct {
	Available []string `json:"available"`
	Settings  []string `json:"settings"`
}

func (s *Server) handleMethods(w http.ResponseWriter, r *http.Request) {
	var apis []string
	if s.withCamera(w, r, func(cam *sony.Camera) (err error) {
		apis, err = cam.AvailableAPIs(r.Context())
		return err
	}) {
		writeJSON(w, http.StatusOK, MethodList{Available: apis, Settings: sony.Settings()})
	}
}

// RPCRequest is a raw Camera Remote API call.
type RPCRequest struct {
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	Version string `json:"version"`
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	var req RPCRequest
	if err := decodeJSON(r, &req); err != nil || req.Method == "" {
		writeError(w, http.StatusBadRequest, errors.New(`expected {"method": "...", "params": [...], "version": "1.0"}`))
		return
	}
	service := r.PathValue("service")
	switch service {
	case sony.ServiceCamera, sony.ServiceSystem, sony.ServiceAVContent, sony.ServiceGuide:
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown service %q", service))
		return
	}
	cam, err := s.camera(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := cam.Client().Call(ctx, service, req.Method, req.Params, req.Version)
	var apiErr *sony.APIError
	switch {
	case errors.As(err, &apiErr):
		// Camera-level errors are part of the protocol: mirror its envelope.
		writeJSON(w, http.StatusOK, map[string]any{"error": []any{apiErr.Code, apiErr.Message}})
	case err != nil:
		s.noteErr(err)
		writeError(w, http.StatusBadGateway, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"result": result})
	}
}

// --- helpers -----------------------------------------------------------------

type badRequestError struct{ err error }

func (e *badRequestError) Error() string { return e.err.Error() }
func (e *badRequestError) Unwrap() error { return e.err }

func statusFor(err error) int {
	var bad *badRequestError
	switch {
	case errors.As(err, &bad):
		return http.StatusBadRequest
	case errors.Is(err, errNoRenderer):
		return http.StatusNotImplemented
	case errors.Is(err, errBusy), errors.Is(err, errRendering), errors.Is(err, errStillActive), errors.Is(err, errNoSession):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// APIErrorBody is the JSON error envelope of focal's own endpoints.
type APIErrorBody struct {
	Error      string `json:"error"`
	CameraCode int    `json:"cameraCode,omitempty"`
}

func writeCameraError(w http.ResponseWriter, err error) {
	var apiErr *sony.APIError
	if errors.As(err, &apiErr) {
		status := http.StatusConflict // the camera refused in its current state
		if apiErr.Code == sony.ErrCodeIllegalArgument {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, APIErrorBody{Error: apiErr.Error(), CameraCode: apiErr.Code})
		return
	}
	if errors.Is(err, sony.ErrUnknownSetting) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusBadGateway, err)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, APIErrorBody{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// EnsureMediaDir creates the media directory and returns its absolute path.
func EnsureMediaDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, os.MkdirAll(abs, 0o755)
}
