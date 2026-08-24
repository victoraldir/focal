package domain_test

import (
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/domain"
)

func TestLapseRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     domain.LapseRequest
		wantErr bool
	}{
		{"valid", domain.LapseRequest{InputDir: "in", OutputPath: "o.mp4", FPS: 30}, false},
		{"missing input", domain.LapseRequest{OutputPath: "o.mp4", FPS: 30}, true},
		{"missing output", domain.LapseRequest{InputDir: "in", FPS: 30}, true},
		{"zero fps", domain.LapseRequest{InputDir: "in", OutputPath: "o.mp4", FPS: 0}, true},
		{"negative fps", domain.LapseRequest{InputDir: "in", OutputPath: "o.mp4", FPS: -5}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestImage_AspectRatio(t *testing.T) {
	if got := (domain.Image{Width: 1920, Height: 1080}).AspectRatio(); got < 1.77 || got > 1.78 {
		t.Errorf("16:9 aspect ratio = %v, want ~1.777", got)
	}
	if got := (domain.Image{Width: 100, Height: 0}).AspectRatio(); got != 0 {
		t.Errorf("zero-height aspect ratio = %v, want 0 (no divide by zero)", got)
	}
}

func TestAspectRatioVaries(t *testing.T) {
	now := time.Now()
	mk := func(w, h int) domain.Image { return domain.Image{Width: w, Height: h, Timestamp: now} }

	t.Run("uniform", func(t *testing.T) {
		varies, _, _ := domain.AspectRatioVaries([]domain.Image{mk(1920, 1080), mk(1280, 720)}, 0.01)
		if varies {
			t.Error("consistent 16:9 frames should not be flagged as varying")
		}
	})

	t.Run("mixed orientation", func(t *testing.T) {
		varies, min, max := domain.AspectRatioVaries([]domain.Image{mk(1920, 1080), mk(1080, 1920)}, 0.01)
		if !varies {
			t.Error("landscape + portrait should be flagged as varying")
		}
		if min >= max {
			t.Errorf("expected min < max, got min=%v max=%v", min, max)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if varies, _, _ := domain.AspectRatioVaries(nil, 0.01); varies {
			t.Error("empty sequence should not vary")
		}
	})
}
