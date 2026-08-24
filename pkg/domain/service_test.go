package domain_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/domain"
)

// --- test doubles -----------------------------------------------------------

type stubScanner struct {
	images []domain.Image
	err    error
}

func (s stubScanner) Scan(context.Context, string) ([]domain.Image, error) {
	return s.images, s.err
}

type stubResolver struct {
	path string
	err  error
}

func (s stubResolver) Resolve(context.Context) (string, error) { return s.path, s.err }

type spyConcat struct {
	built       bool
	cleanupCald bool
	err         error
}

func (s *spyConcat) Build([]domain.Image, int) (string, func() error, error) {
	if s.err != nil {
		return "", nil, s.err
	}
	s.built = true
	return "/tmp/concat.txt", func() error { s.cleanupCald = true; return nil }, nil
}

type spyEncoder struct {
	called bool
	gotReq domain.EncodeRequest
	err    error
}

func (s *spyEncoder) Encode(_ context.Context, req domain.EncodeRequest, _ io.Writer) error {
	s.called = true
	s.gotReq = req
	return s.err
}

type recordLogger struct {
	warnings []string
}

func (r *recordLogger) Infof(string, ...any) {}
func (r *recordLogger) Warnf(format string, args ...any) {
	r.warnings = append(r.warnings, format)
}

func img(w, h int, ts time.Time) domain.Image {
	return domain.Image{Path: "/p", Width: w, Height: h, Timestamp: ts}
}

// --- tests ------------------------------------------------------------------

func TestCreate_HappyPath(t *testing.T) {
	concat := &spyConcat{}
	encoder := &spyEncoder{}
	log := &recordLogger{}
	ts := domain.NewTimelapser(
		stubScanner{images: []domain.Image{img(1920, 1080, time.Now())}},
		stubResolver{path: "/usr/bin/ffmpeg"},
		concat,
		encoder,
		log,
	)

	err := ts.Create(context.Background(), domain.LapseRequest{
		InputDir:   "in",
		OutputPath: "out.mp4",
		FPS:        30,
	}, io.Discard)

	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !concat.built {
		t.Error("expected concat file to be built")
	}
	if !concat.cleanupCald {
		t.Error("expected temporary concat file to be cleaned up")
	}
	if !encoder.called {
		t.Fatal("expected encoder to be called")
	}
	if encoder.gotReq.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("encoder got ffmpeg path %q, want /usr/bin/ffmpeg", encoder.gotReq.FFmpegPath)
	}
	if encoder.gotReq.OutputPath != "out.mp4" {
		t.Errorf("encoder got output %q, want out.mp4", encoder.gotReq.OutputPath)
	}
}

func TestCreate_CapsOversizedOutput(t *testing.T) {
	encoder := &spyEncoder{}
	ts := domain.NewTimelapser(
		stubScanner{images: []domain.Image{img(6000, 3376, time.Now())}}, // 6K source
		stubResolver{path: "ffmpeg"},
		&spyConcat{}, encoder, &recordLogger{},
	)
	req := domain.LapseRequest{InputDir: "in", OutputPath: "o.mp4", FPS: 30, MaxHeight: 2160}
	if err := ts.Create(context.Background(), req, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encoder.gotReq.ScaleHeight != 2160 {
		t.Errorf("expected output scaled to 2160, got ScaleHeight=%d", encoder.gotReq.ScaleHeight)
	}
}

func TestCreate_NoScaleWhenSourceFits(t *testing.T) {
	encoder := &spyEncoder{}
	ts := domain.NewTimelapser(
		stubScanner{images: []domain.Image{img(1920, 1080, time.Now())}},
		stubResolver{path: "ffmpeg"},
		&spyConcat{}, encoder, &recordLogger{},
	)
	req := domain.LapseRequest{InputDir: "in", OutputPath: "o.mp4", FPS: 30, MaxHeight: 2160}
	if err := ts.Create(context.Background(), req, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encoder.gotReq.ScaleHeight != 0 {
		t.Errorf("1080p source under a 2160 cap should not scale, got ScaleHeight=%d", encoder.gotReq.ScaleHeight)
	}
}

func TestCreate_NoImages(t *testing.T) {
	ts := domain.NewTimelapser(
		stubScanner{images: nil},
		stubResolver{path: "ffmpeg"},
		&spyConcat{}, &spyEncoder{}, &recordLogger{},
	)
	err := ts.Create(context.Background(), validReq(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no images") {
		t.Fatalf("expected no-images error, got %v", err)
	}
}

func TestCreate_InvalidRequest(t *testing.T) {
	ts := domain.NewTimelapser(stubScanner{}, stubResolver{}, &spyConcat{}, &spyEncoder{}, &recordLogger{})
	err := ts.Create(context.Background(), domain.LapseRequest{FPS: 0}, io.Discard)
	if err == nil {
		t.Fatal("expected validation error for empty request")
	}
}

func TestCreate_ScannerError(t *testing.T) {
	ts := domain.NewTimelapser(
		stubScanner{err: errors.New("boom")},
		stubResolver{path: "ffmpeg"},
		&spyConcat{}, &spyEncoder{}, &recordLogger{},
	)
	if err := ts.Create(context.Background(), validReq(), io.Discard); err == nil {
		t.Fatal("expected scanner error to propagate")
	}
}

func TestCreate_ResolverErrorSkipsEncode(t *testing.T) {
	encoder := &spyEncoder{}
	ts := domain.NewTimelapser(
		stubScanner{images: []domain.Image{img(100, 100, time.Now())}},
		stubResolver{err: errors.New("no ffmpeg")},
		&spyConcat{}, encoder, &recordLogger{},
	)
	if err := ts.Create(context.Background(), validReq(), io.Discard); err == nil {
		t.Fatal("expected resolver error")
	}
	if encoder.called {
		t.Error("encoder must not run when ffmpeg cannot be resolved")
	}
}

func TestCreate_WarnsOnVaryingAspectRatio(t *testing.T) {
	log := &recordLogger{}
	now := time.Now()
	ts := domain.NewTimelapser(
		stubScanner{images: []domain.Image{
			img(1920, 1080, now),                  // 16:9
			img(1080, 1920, now.Add(time.Second)), // 9:16
		}},
		stubResolver{path: "ffmpeg"},
		&spyConcat{}, &spyEncoder{}, log,
	)
	if err := ts.Create(context.Background(), validReq(), io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(log.warnings) == 0 {
		t.Error("expected a warning about varying aspect ratios")
	}
}

func TestCreate_CleanupOnEncodeError(t *testing.T) {
	concat := &spyConcat{}
	ts := domain.NewTimelapser(
		stubScanner{images: []domain.Image{img(100, 100, time.Now())}},
		stubResolver{path: "ffmpeg"},
		concat,
		&spyEncoder{err: errors.New("encode failed")},
		&recordLogger{},
	)
	if err := ts.Create(context.Background(), validReq(), io.Discard); err == nil {
		t.Fatal("expected encode error")
	}
	if !concat.cleanupCald {
		t.Error("temporary file must be cleaned up even when encoding fails")
	}
}

func validReq() domain.LapseRequest {
	return domain.LapseRequest{InputDir: "in", OutputPath: "out.mp4", FPS: 30}
}
