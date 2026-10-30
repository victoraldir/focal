// Package sonysim is an in-process simulator of a Sony camera's Camera Remote
// API. It models the parts of an a5000-class body that focal relies on —
// rec-mode gating, exposure-mode-dependent settings, power zoom, focus,
// postview downloads, and the binary live-view stream — so the CLI, the HTTP
// studio, and the tests can run without hardware.
package sonysim

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/victoraldir/focal/pkg/camera/sony"
)

// sunsetPeriod is how long one simulated sunset takes before looping.
const sunsetPeriod = 3 * time.Minute

type setting struct {
	current    string
	candidates []string
}

// Simulator is an http.Handler that behaves like a camera's API server.
type Simulator struct {
	// Model is reported as the UPnP friendly name.
	Model string
	// CaptureDelay simulates shutter + processing time.
	CaptureDelay time.Duration

	mu         sync.Mutex
	started    time.Time
	recMode    bool
	liveview   bool
	settings   map[string]*setting
	ev         int
	zoomBase   float64
	zoomDir    float64
	zoomSince  time.Time
	focusUntil time.Time
	halfPress  bool
	shots      int
	remaining  int
	photos     map[int][]byte
}

// New returns a simulator in the state a body boots into: Smart Remote running,
// rec mode not yet started.
func New() *Simulator {
	return &Simulator{
		Model:        "ILCE-5000 (simulated)",
		CaptureDelay: 250 * time.Millisecond,
		started:      time.Now(),
		remaining:    999,
		photos:       map[int][]byte{},
		settings: map[string]*setting{
			"shootMode":         {"still", []string{"still", "movie"}},
			"exposureMode":      {"Program Auto", []string{"Intelligent Auto", "Superior Auto", "Program Auto", "Aperture", "Shutter", "Manual"}},
			"fNumber":           {"5.6", []string{"3.5", "4.0", "4.5", "5.0", "5.6", "6.3", "7.1", "8.0", "9.0", "10", "11", "13", "14", "16", "18", "20", "22"}},
			"shutterSpeed":      {"1/125", []string{"30\"", "15\"", "8\"", "4\"", "2\"", "1\"", "0.5\"", "1/4", "1/8", "1/15", "1/30", "1/60", "1/125", "1/250", "1/500", "1/1000", "1/2000", "1/4000"}},
			"isoSpeedRate":      {"AUTO", []string{"AUTO", "100", "200", "400", "800", "1600", "3200", "6400", "12800", "16000"}},
			"whiteBalance":      {"Auto WB", []string{"Auto WB", "Daylight", "Shade", "Cloudy", "Incandescent", "Fluor: Warm White", "Flash"}},
			"focusMode":         {"AF-S", []string{"AF-S", "AF-C", "DMF", "MF"}},
			"flashMode":         {"off", []string{"off", "auto", "on"}},
			"selfTimer":         {"0", []string{"0", "2", "10"}},
			"postviewImageSize": {"2M", []string{"2M", "Original"}},
		},
	}
}

// ServeHTTP routes API, live-view, postview and device-description requests.
func (s *Simulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/sony/"):
		s.serveRPC(w, r, strings.TrimPrefix(r.URL.Path, "/sony/"))
	case r.URL.Path == "/liveview/liveviewstream":
		s.serveLiveview(w, r)
	case strings.HasPrefix(r.URL.Path, "/postview/"):
		s.servePostview(w, r)
	case r.URL.Path == "/DmsRmtDesc.xml":
		s.serveDescription(w, r)
	default:
		http.NotFound(w, r)
	}
}

type rpcRequest struct {
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
	ID      int               `json:"id"`
	Version string            `json:"version"`
}

func (s *Simulator) serveRPC(w http.ResponseWriter, r *http.Request, service string) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var (
		result any
		apiErr *sony.APIError
	)
	if service == sony.ServiceCamera {
		result, apiErr = s.handle(r, req)
	} else {
		apiErr = &sony.APIError{Code: sony.ErrCodeNoSuchMethod, Message: "No Such Method"}
	}
	w.Header().Set("Content-Type", "application/json")
	if apiErr != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": []any{apiErr.Code, apiErr.Message}, "id": req.ID})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "id": req.ID})
}

func errNotAvailable() *sony.APIError {
	return &sony.APIError{Code: sony.ErrCodeAny, Message: "Not Available Now"}
}

// setterFor maps setXxx method names to the setting they change.
var setterFor = map[string]string{
	"setShootMode":         "shootMode",
	"setExposureMode":      "exposureMode",
	"setFNumber":           "fNumber",
	"setShutterSpeed":      "shutterSpeed",
	"setIsoSpeedRate":      "isoSpeedRate",
	"setWhiteBalance":      "whiteBalance",
	"setFocusMode":         "focusMode",
	"setFlashMode":         "flashMode",
	"setSelfTimer":         "selfTimer",
	"setPostviewImageSize": "postviewImageSize",
}

func (s *Simulator) handle(r *http.Request, req rpcRequest) (any, *sony.APIError) {
	host := "http://" + r.Host

	s.mu.Lock()
	available := s.availableAPIsLocked()
	s.mu.Unlock()
	if req.Method != "getVersions" && req.Method != "getMethodTypes" && !slices.Contains(available, req.Method) {
		if _, known := knownMethods[req.Method]; known {
			return nil, errNotAvailable()
		}
		return nil, &sony.APIError{Code: sony.ErrCodeNoSuchMethod, Message: "No Such Method"}
	}

	switch req.Method {
	case "getVersions":
		return [][]string{{"1.0", "1.1", "1.2"}}, nil
	case "getMethodTypes":
		return nil, &sony.APIError{Code: sony.ErrCodeNoSuchMethod, Message: "No Such Method"}
	case "getApplicationInfo":
		return []string{"Smart Remote Control", "2.1.0 (simulated)"}, nil
	case "getAvailableApiList":
		return [][]string{available}, nil
	case "startRecMode":
		s.mu.Lock()
		s.recMode = true
		s.mu.Unlock()
		return []int{0}, nil
	case "stopRecMode":
		s.mu.Lock()
		s.recMode, s.liveview = false, false
		s.mu.Unlock()
		return []int{0}, nil
	case "getEvent":
		if len(req.Params) > 0 && string(req.Params[0]) == "true" {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.eventLocked(), nil
	case "actTakePicture":
		return s.takePicture(r, host)
	case "awaitTakePicture":
		return nil, &sony.APIError{Code: sony.ErrCodeAny, Message: "Not Available Now"}
	case "startLiveview":
		s.mu.Lock()
		s.liveview = true
		s.mu.Unlock()
		return []string{host + "/liveview/liveviewstream"}, nil
	case "stopLiveview":
		s.mu.Lock()
		s.liveview = false
		s.mu.Unlock()
		return []int{0}, nil
	case "actZoom":
		return s.zoom(req.Params)
	case "actHalfPressShutter":
		s.mu.Lock()
		s.halfPress = true
		s.focusUntil = time.Now().Add(400 * time.Millisecond)
		s.mu.Unlock()
		time.Sleep(400 * time.Millisecond)
		return []int{0}, nil
	case "cancelHalfPressShutter":
		s.mu.Lock()
		s.halfPress = false
		s.mu.Unlock()
		return []int{0}, nil
	case "setTouchAFPosition":
		s.mu.Lock()
		s.focusUntil = time.Now().Add(300 * time.Millisecond)
		s.mu.Unlock()
		time.Sleep(300 * time.Millisecond)
		return []any{0, map[string]any{"AFResult": true, "AFType": "Touch"}}, nil
	case "setExposureCompensation":
		n, err := intArg(req.Params)
		if err != nil || n < -9 || n > 9 {
			return nil, &sony.APIError{Code: sony.ErrCodeIllegalArgument, Message: "Illegal Argument"}
		}
		s.mu.Lock()
		s.ev = n
		s.mu.Unlock()
		return []int{0}, nil
	case "getAvailableWhiteBalance":
		s.mu.Lock()
		defer s.mu.Unlock()
		wb := s.settings["whiteBalance"]
		modes := make([]map[string]any, 0, len(wb.candidates))
		for _, m := range wb.candidates {
			modes = append(modes, map[string]any{"whiteBalanceMode": m, "colorTemperatureRange": []int{}})
		}
		return []any{map[string]any{"whiteBalanceMode": wb.current, "colorTemperature": -1}, modes}, nil
	}

	if name, ok := setterFor[req.Method]; ok {
		return s.set(name, req.Params)
	}
	if name, ok := strings.CutPrefix(req.Method, "getAvailable"); ok {
		key := lowerFirst(name)
		if key == "postviewImageSize" || s.settings[key] != nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			st := s.settings[key]
			return []any{st.current, st.candidates}, nil
		}
	}
	return nil, &sony.APIError{Code: sony.ErrCodeNoSuchMethod, Message: "No Such Method"}
}

// knownMethods are methods the simulator implements but may refuse in the
// current state ("Not Available Now" rather than "No Such Method").
var knownMethods = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, name := range allMethods {
		m[name] = struct{}{}
	}
	return m
}()

var allMethods = []string{
	"getAvailableApiList", "getApplicationInfo", "getVersions", "getEvent",
	"startRecMode", "stopRecMode", "actTakePicture", "awaitTakePicture",
	"startLiveview", "stopLiveview", "actZoom", "actHalfPressShutter",
	"cancelHalfPressShutter", "setTouchAFPosition",
	"setShootMode", "getAvailableShootMode",
	"setExposureMode", "getAvailableExposureMode",
	"setFNumber", "getAvailableFNumber",
	"setShutterSpeed", "getAvailableShutterSpeed",
	"setIsoSpeedRate", "getAvailableIsoSpeedRate",
	"setExposureCompensation",
	"setWhiteBalance", "getAvailableWhiteBalance",
	"setFocusMode", "getAvailableFocusMode",
	"setFlashMode", "getAvailableFlashMode",
	"setSelfTimer", "getAvailableSelfTimer",
	"setPostviewImageSize", "getAvailablePostviewImageSize",
}

// availableAPIsLocked mirrors how a real body gates methods: before
// startRecMode only a handful exist, and manual exposure controls depend on the
// exposure mode.
func (s *Simulator) availableAPIsLocked() []string {
	if !s.recMode {
		return []string{"getVersions", "getMethodTypes", "getApplicationInfo", "getAvailableApiList", "getEvent", "startRecMode", "stopRecMode"}
	}
	mode := s.settings["exposureMode"].current
	auto := mode == "Intelligent Auto" || mode == "Superior Auto"
	out := make([]string, 0, len(allMethods))
	for _, m := range allMethods {
		switch {
		case m == "startRecMode":
			continue
		case m == "awaitTakePicture":
			continue
		case strings.HasSuffix(m, "FNumber") && mode != "Aperture" && mode != "Manual":
			continue
		case strings.HasSuffix(m, "ShutterSpeed") && mode != "Shutter" && mode != "Manual":
			continue
		case (strings.HasSuffix(m, "IsoSpeedRate") || strings.HasSuffix(m, "WhiteBalance") || m == "setExposureCompensation") && auto:
			continue
		case m == "setExposureCompensation" && mode == "Manual":
			continue
		case m == "stopLiveview" && !s.liveview:
			continue
		}
		out = append(out, m)
	}
	return out
}

func (s *Simulator) set(name string, params []json.RawMessage) (any, *sony.APIError) {
	if len(params) == 0 {
		return nil, &sony.APIError{Code: sony.ErrCodeIllegalArgument, Message: "Illegal Argument"}
	}
	var v any
	_ = json.Unmarshal(params[0], &v)
	value := fmt.Sprint(v)
	if f, ok := v.(float64); ok {
		value = strconv.FormatFloat(f, 'f', -1, 64)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.settings[name]
	if !slices.Contains(st.candidates, value) {
		return nil, &sony.APIError{Code: sony.ErrCodeIllegalArgument, Message: "Illegal Argument"}
	}
	st.current = value
	return []int{0}, nil
}

func (s *Simulator) zoom(params []json.RawMessage) (any, *sony.APIError) {
	var dir, movement string
	if len(params) < 2 || json.Unmarshal(params[0], &dir) != nil || json.Unmarshal(params[1], &movement) != nil {
		return nil, &sony.APIError{Code: sony.ErrCodeIllegalArgument, Message: "Illegal Argument"}
	}
	sign := 1.0
	if dir == "out" {
		sign = -1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zoomBase = s.zoomPosLocked()
	s.zoomSince = time.Now()
	switch movement {
	case "start":
		s.zoomDir = sign
	case "stop":
		s.zoomDir = 0
	case "1shot":
		s.zoomDir = 0
		s.zoomBase = math.Max(0, math.Min(100, s.zoomBase+sign*10))
	default:
		return nil, &sony.APIError{Code: sony.ErrCodeIllegalArgument, Message: "Illegal Argument"}
	}
	return []int{0}, nil
}

// zoomPosLocked integrates a running continuous zoom (40%/s).
func (s *Simulator) zoomPosLocked() float64 {
	pos := s.zoomBase + s.zoomDir*time.Since(s.zoomSince).Seconds()*40
	return math.Max(0, math.Min(100, pos))
}

func (s *Simulator) takePicture(r *http.Request, host string) (any, *sony.APIError) {
	s.mu.Lock()
	if s.settings["shootMode"].current != "still" {
		s.mu.Unlock()
		return nil, errNotAvailable()
	}
	s.shots++
	id := s.shots
	params := s.sceneLocked()
	size := s.settings["postviewImageSize"].current
	s.mu.Unlock()

	select {
	case <-r.Context().Done():
		return nil, errNotAvailable()
	case <-time.After(s.CaptureDelay):
	}

	params.focused = true
	w, h := 1616, 1080
	if size == "Original" {
		w, h = 3008, 2000
	}
	data := renderScene(w, h, params, 88)

	s.mu.Lock()
	s.photos[id] = data
	delete(s.photos, id-20) // keep memory bounded
	if s.remaining > 0 {
		s.remaining--
	}
	s.mu.Unlock()
	return [][]string{{fmt.Sprintf("%s/postview/pict%04d.JPG", host, id)}}, nil
}

func (s *Simulator) sceneLocked() sceneParams {
	phase := math.Mod(time.Since(s.started).Seconds(), sunsetPeriod.Seconds()) / sunsetPeriod.Seconds()
	return sceneParams{
		phase:    phase,
		exposure: math.Pow(2, float64(s.ev)/3),
		zoom:     int(s.zoomPosLocked()),
		focused:  time.Now().After(s.focusUntil),
	}
}

func (s *Simulator) servePostview(w http.ResponseWriter, r *http.Request) {
	var id int
	if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/postview/"), "pict%04d.JPG", &id); err != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	data, ok := s.photos[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(data)
}

func (s *Simulator) serveLiveview(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	active := s.liveview
	s.mu.Unlock()
	if !active {
		http.Error(w, "live view not started", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(time.Second / 15)
	defer ticker.Stop()
	for seq := uint16(0); ; seq++ {
		s.mu.Lock()
		active, params := s.liveview, s.sceneLocked()
		s.mu.Unlock()
		if !active {
			return
		}
		frame := renderScene(640, 424, params, 70)
		if err := sony.EncodeLiveviewPacket(w, 0x01, seq, uint32(time.Since(s.started).Milliseconds()), frame); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Simulator) serveDescription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <friendlyName>%s</friendlyName>
    <av:X_ScalarWebAPI_DeviceInfo xmlns:av="urn:schemas-sony-com:av">
      <av:X_ScalarWebAPI_Version>1.0</av:X_ScalarWebAPI_Version>
      <av:X_ScalarWebAPI_ServiceList>
        <av:X_ScalarWebAPI_Service>
          <av:X_ScalarWebAPI_ServiceType>camera</av:X_ScalarWebAPI_ServiceType>
          <av:X_ScalarWebAPI_ActionList_URL>http://%s/sony</av:X_ScalarWebAPI_ActionList_URL>
        </av:X_ScalarWebAPI_Service>
      </av:X_ScalarWebAPI_ServiceList>
    </av:X_ScalarWebAPI_DeviceInfo>
  </device>
</root>`, s.Model, r.Host)
}

// eventLocked builds a getEvent result in the positional layout real bodies
// use (unreported slots are null).
func (s *Simulator) eventLocked() []any {
	slots := make([]any, 40)
	slots[0] = map[string]any{"type": "availableApiList", "names": s.availableAPIsLocked()}
	status := "IDLE"
	if !s.recMode {
		status = "NotReady"
	}
	slots[1] = map[string]any{"type": "cameraStatus", "cameraStatus": status}
	slots[2] = map[string]any{"type": "zoomInformation", "zoomPosition": int(s.zoomPosLocked()), "zoomNumberBox": 1, "zoomIndexCurrentBox": 0, "zoomPositionCurrentBox": int(s.zoomPosLocked())}
	slots[3] = map[string]any{"type": "liveviewStatus", "liveviewStatus": s.liveview}
	slots[10] = []any{map[string]any{"type": "storageInformation", "storageID": "Memory Card 1", "recordTarget": true, "numberOfRecordableImages": s.remaining, "recordableTime": -1}}
	// Battery drains 1% per simulated minute, floored at 5%.
	level := max(5, 100-int(time.Since(s.started).Minutes()))
	slots[11] = []any{map[string]any{"type": "batteryInfo", "batteryID": "1", "status": "active", "levelNumer": level, "levelDenom": 100}}
	if !s.recMode {
		return slots
	}
	idx := 12
	for _, name := range []string{"shootMode", "exposureMode", "postviewImageSize", "selfTimer", "flashMode", "fNumber", "focusMode", "isoSpeedRate", "shutterSpeed"} {
		st := s.settings[name]
		upper := strings.ToUpper(name[:1]) + name[1:]
		var cur any = st.current
		cands := make([]any, len(st.candidates))
		for i, c := range st.candidates {
			cands[i] = c
		}
		if name == "selfTimer" {
			cur, _ = strconv.Atoi(st.current)
			for i, c := range st.candidates {
				cands[i], _ = strconv.Atoi(c)
			}
		}
		slots[idx] = map[string]any{"type": name, "current" + upper: cur, name + "Candidates": cands}
		idx++
	}
	slots[idx] = map[string]any{"type": "exposureCompensation", "currentExposureCompensation": s.ev, "maxExposureCompensation": 9, "minExposureCompensation": -9, "stepIndexOfExposureCompensation": 1}
	slots[idx+1] = map[string]any{"type": "whiteBalance", "currentWhiteBalanceMode": s.settings["whiteBalance"].current, "currentColorTemperature": -1, "checkAvailability": true}
	focus := "Not Focusing"
	if s.halfPress {
		focus = "Focusing"
		if time.Now().After(s.focusUntil) {
			focus = "Focused"
		}
	}
	slots[idx+2] = map[string]any{"type": "focusStatus", "focusStatus": focus}
	return slots
}

func intArg(params []json.RawMessage) (int, error) {
	if len(params) == 0 {
		return 0, fmt.Errorf("missing argument")
	}
	var n int
	err := json.Unmarshal(params[0], &n)
	return n, err
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	// Acronym-prefixed names keep their casing rules used by the API.
	if strings.HasPrefix(s, "FNumber") {
		return "f" + s[1:]
	}
	return strings.ToLower(s[:1]) + s[1:]
}
