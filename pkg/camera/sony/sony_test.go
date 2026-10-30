package sony_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/victoraldir/focal/pkg/camera/sony"
	"github.com/victoraldir/focal/pkg/camera/sony/sonysim"
)

func newSimCamera(t *testing.T) (*sony.Camera, *sonysim.Simulator) {
	t.Helper()
	sim := sonysim.New()
	sim.CaptureDelay = 0
	srv := httptest.NewServer(sim)
	t.Cleanup(srv.Close)
	cam, err := sony.Connect(context.Background(), srv.URL+"/sony/camera", srv.Client(), nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return cam, sim
}

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.168.122.1:8080/sony":        "http://192.168.122.1:8080/sony",
		"http://192.168.122.1:8080/sony/":       "http://192.168.122.1:8080/sony",
		"http://192.168.122.1:8080/sony/camera": "http://192.168.122.1:8080/sony",
		" http://10.0.0.1:10000/sony/camera/ ":  "http://10.0.0.1:10000/sony",
		"http://10.0.0.1:10000/sony/avContent":  "http://10.0.0.1:10000/sony",
	} {
		if got := sony.NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClient_DecodesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":[40401,"Camera Not Ready"],"id":1}`)
	}))
	defer srv.Close()

	_, err := sony.NewClient(srv.URL+"/sony", nil).Call(context.Background(), "camera", "actTakePicture", nil, "")
	var apiErr *sony.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != sony.ErrCodeCameraNotReady || apiErr.Message != "Camera Not Ready" {
		t.Fatalf("expected APIError 40401, got %v", err)
	}
}

func TestClient_SendsJSONRPCEnvelope(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sony/camera" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"result":[0],"id":1}`)
	}))
	defer srv.Close()

	if _, err := sony.NewClient(srv.URL+"/sony", nil).Call(context.Background(), "camera", "actZoom", []any{"in", "1shot"}, ""); err != nil {
		t.Fatal(err)
	}
	if got["method"] != "actZoom" || got["version"] != "1.0" || len(got["params"].([]any)) != 2 {
		t.Errorf("unexpected envelope: %v", got)
	}
}

func TestParseDescription(t *testing.T) {
	xml := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0"><device>
  <friendlyName>ILCE-5000</friendlyName>
  <av:X_ScalarWebAPI_DeviceInfo xmlns:av="urn:schemas-sony-com:av">
    <av:X_ScalarWebAPI_ServiceList>
      <av:X_ScalarWebAPI_Service><av:X_ScalarWebAPI_ServiceType>guide</av:X_ScalarWebAPI_ServiceType>
        <av:X_ScalarWebAPI_ActionList_URL>http://192.168.122.1:8080/sony</av:X_ScalarWebAPI_ActionList_URL></av:X_ScalarWebAPI_Service>
      <av:X_ScalarWebAPI_Service><av:X_ScalarWebAPI_ServiceType>camera</av:X_ScalarWebAPI_ServiceType>
        <av:X_ScalarWebAPI_ActionList_URL>http://192.168.122.1:8080/sony</av:X_ScalarWebAPI_ActionList_URL></av:X_ScalarWebAPI_Service>
    </av:X_ScalarWebAPI_ServiceList>
  </av:X_ScalarWebAPI_DeviceInfo>
</device></root>`
	dev, err := sony.ParseDescription(strings.NewReader(xml))
	if err != nil {
		t.Fatal(err)
	}
	if dev.FriendlyName != "ILCE-5000" || dev.BaseURL != "http://192.168.122.1:8080/sony" || len(dev.Services) != 2 {
		t.Errorf("unexpected device: %+v", dev)
	}

	if _, err := sony.ParseDescription(strings.NewReader(`<root><device><friendlyName>TV</friendlyName></device></root>`)); err == nil {
		t.Error("a device without a camera service must be rejected")
	}
}

func TestParseEvent(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"availableApiList","names":["actTakePicture","setFNumber"]},
		{"type":"cameraStatus","cameraStatus":"IDLE"},
		{"type":"zoomInformation","zoomPosition":42},
		null,
		[],
		[{"type":"storageInformation","storageID":"No Media","recordTarget":false,"numberOfRecordableImages":-1},
		 {"type":"storageInformation","storageID":"Memory Card 1","recordTarget":true,"numberOfRecordableImages":512}],
		[{"type":"batteryInfo","status":"active","levelNumer":3,"levelDenom":4}],
		{"type":"fNumber","currentFNumber":"5.6","fNumberCandidates":["4.0","5.6","8.0"]},
		{"type":"selfTimer","currentSelfTimer":2,"selfTimerCandidates":[0,2,10]},
		{"type":"exposureCompensation","currentExposureCompensation":-3,"minExposureCompensation":-9,"maxExposureCompensation":9,"stepIndexOfExposureCompensation":1}
	]`)
	st, err := sony.ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if st.CameraStatus != "IDLE" || len(st.AvailableAPIs) != 2 {
		t.Errorf("status/apis: %+v", st)
	}
	if st.ZoomPosition == nil || *st.ZoomPosition != 42 {
		t.Errorf("zoom = %v", st.ZoomPosition)
	}
	if st.Storage == nil || st.Storage.RemainingShots != 512 {
		t.Errorf("storage should be the record target: %+v", st.Storage)
	}
	if st.Battery == nil || st.Battery.Percent != 75 {
		t.Errorf("battery = %+v", st.Battery)
	}
	if st.FNumber == nil || st.FNumber.Current != "5.6" || len(st.FNumber.Candidates) != 3 {
		t.Errorf("fNumber = %+v", st.FNumber)
	}
	if st.SelfTimer == nil || st.SelfTimer.Current != "2" || st.SelfTimer.Candidates[2] != "10" {
		t.Errorf("numeric choices should be stringified: %+v", st.SelfTimer)
	}
	if ev := st.ExposureCompensation; ev == nil || ev.Current != -3 || ev.Step < 0.33 || ev.Step > 0.34 {
		t.Errorf("exposure compensation = %+v", ev)
	}
	if st.ShutterSpeed != nil {
		t.Error("unreported settings must stay nil")
	}
}

func TestLiveviewReader_SkipsInfoPacketsAndPadding(t *testing.T) {
	var buf bytes.Buffer
	_ = sony.EncodeLiveviewPacket(&buf, 0x02, 0, 0, []byte("focus-frame-info"))
	_ = sony.EncodeLiveviewPacket(&buf, 0x01, 1, 0, []byte("JPEG-1"))
	_ = sony.EncodeLiveviewPacket(&buf, 0x01, 2, 0, []byte("JPEG-22"))

	r := sony.NewLiveviewReader(io.NopCloser(&buf))
	for _, want := range []string{"JPEG-1", "JPEG-22"} {
		frame, err := r.Next()
		if err != nil || string(frame) != want {
			t.Fatalf("Next() = %q, %v; want %q", frame, err, want)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("expected EOF at end of stream, got %v", err)
	}
}

func TestLiveviewReader_RejectsGarbage(t *testing.T) {
	r := sony.NewLiveviewReader(io.NopCloser(bytes.NewReader(make([]byte, 200))))
	if _, err := r.Next(); err == nil {
		t.Fatal("expected an error for a stream without a start byte")
	}
}

func TestCamera_PrepareEntersRecModeAndCaptures(t *testing.T) {
	cam, _ := newSimCamera(t)
	ctx := context.Background()

	apis, err := cam.AvailableAPIs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(apis, "actTakePicture") {
		t.Fatal("simulator should boot without remote shooting, like a real body")
	}
	if err := cam.Prepare(ctx); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	photo, err := cam.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if photo.Ext != ".jpg" {
		t.Errorf("ext = %q", photo.Ext)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(photo.Data))
	if err != nil {
		t.Fatalf("captured data is not a JPEG: %v", err)
	}
	if cfg.Width != 3008 {
		t.Errorf("Prepare should request the Original postview size, got width %d", cfg.Width)
	}
}

func TestCamera_StatusAndSettings(t *testing.T) {
	cam, _ := newSimCamera(t)
	ctx := context.Background()
	if err := cam.Prepare(ctx); err != nil {
		t.Fatal(err)
	}

	if err := cam.Set(ctx, sony.SettingExposureMode, "Manual"); err != nil {
		t.Fatal(err)
	}
	if err := cam.Set(ctx, sony.SettingShutterSpeed, "1/500"); err != nil {
		t.Fatal(err)
	}
	if err := cam.Set(ctx, sony.SettingFNumber, "8.0"); err != nil {
		t.Fatal(err)
	}
	st, err := cam.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ExposureMode.Current != "Manual" || st.ShutterSpeed.Current != "1/500" || st.FNumber.Current != "8.0" {
		t.Errorf("settings not reflected: mode=%v shutter=%v f=%v", st.ExposureMode, st.ShutterSpeed, st.FNumber)
	}
	if st.WhiteBalance == nil || len(st.WhiteBalance.Candidates) == 0 {
		t.Errorf("white balance candidates should be fetched: %+v", st.WhiteBalance)
	}

	if err := cam.Set(ctx, sony.SettingFNumber, "99"); !sony.IsAPIError(err, sony.ErrCodeIllegalArgument) {
		t.Errorf("invalid value should surface the camera's error, got %v", err)
	}
	if err := cam.Set(ctx, "warpDrive", "on"); !errors.Is(err, sony.ErrUnknownSetting) {
		t.Errorf("expected ErrUnknownSetting, got %v", err)
	}
}

func TestCamera_LiveviewStreamsJPEGs(t *testing.T) {
	cam, _ := newSimCamera(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cam.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	lv, err := cam.OpenLiveview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lv.Close()
	frame, err := lv.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(frame)); err != nil {
		t.Fatalf("live-view frame is not a JPEG: %v", err)
	}
}
