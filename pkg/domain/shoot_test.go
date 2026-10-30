package domain_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/domain"
)

// --- test doubles -----------------------------------------------------------

type fakeCamera struct {
	mu         sync.Mutex
	prepareErr error
	// errs is consumed one entry per Capture call; nil entries (or an exhausted
	// slice) mean success.
	errs     []error
	captures int
	onShot   func(n int)
}

func (c *fakeCamera) Prepare(context.Context) error { return c.prepareErr }

func (c *fakeCamera) Capture(context.Context) (domain.Photo, error) {
	c.mu.Lock()
	c.captures++
	n := c.captures
	var err error
	if len(c.errs) > 0 {
		err, c.errs = c.errs[0], c.errs[1:]
	}
	c.mu.Unlock()
	if c.onShot != nil {
		c.onShot(n)
	}
	if err != nil {
		return domain.Photo{}, err
	}
	return domain.Photo{Data: []byte("jpeg"), Ext: ".jpg"}, nil
}

type memStore struct {
	mu    sync.Mutex
	names []string
}

func (s *memStore) Save(name string, _ []byte, _ time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = append(s.names, name)
	return "/out/" + name, nil
}

// --- tests ------------------------------------------------------------------

func TestShootRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     domain.ShootRequest
		wantErr bool
	}{
		{"valid", domain.ShootRequest{OutputDir: "o", Interval: time.Second, Count: 10}, false},
		{"unbounded is valid", domain.ShootRequest{OutputDir: "o", Interval: time.Second}, false},
		{"missing dir", domain.ShootRequest{Interval: time.Second}, true},
		{"zero interval", domain.ShootRequest{OutputDir: "o"}, true},
		{"negative count", domain.ShootRequest{OutputDir: "o", Interval: time.Second, Count: -1}, true},
		{"negative duration", domain.ShootRequest{OutputDir: "o", Interval: time.Second, Duration: -time.Second}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestShootRequest_TotalFrames(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		duration time.Duration
		want     int
	}{
		{"count only", 300, 0, 300},
		{"duration only, exact", 0, time.Minute, 6},
		{"duration only, partial interval rounds up", 0, 61 * time.Second, 7},
		{"count wins when smaller", 3, time.Minute, 3},
		{"duration wins when smaller", 100, time.Minute, 6},
		{"unbounded", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := domain.ShootRequest{Interval: 10 * time.Second, Count: tt.count, Duration: tt.duration}
			if got := r.TotalFrames(); got != tt.want {
				t.Errorf("TotalFrames() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShooter_CapturesCountFrames(t *testing.T) {
	cam, store := &fakeCamera{}, &memStore{}
	var events []domain.ShotEvent
	s := domain.NewShooter(cam, store, &recordLogger{})

	sum, err := s.Run(context.Background(), domain.ShootRequest{OutputDir: "o", Interval: time.Millisecond, Count: 3},
		func(ev domain.ShotEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Elapsed < 2*time.Millisecond {
		t.Errorf("Elapsed = %s, want at least two intervals", sum.Elapsed)
	}
	if sum.Captured != 3 || sum.Stopped {
		t.Errorf("summary = %+v, want 3 captured, not stopped", sum)
	}
	want := []string{"frame_00001.jpg", "frame_00002.jpg", "frame_00003.jpg"}
	if strings.Join(store.names, ",") != strings.Join(want, ",") {
		t.Errorf("saved %v, want %v", store.names, want)
	}
	if len(events) != 3 || events[2].Total != 3 || !events[2].Next.IsZero() || events[0].Next.IsZero() {
		t.Errorf("unexpected events: %+v", events)
	}
}

func TestShooter_ContinuesPastIsolatedFailure(t *testing.T) {
	cam := &fakeCamera{errs: []error{nil, errors.New("wifi blip"), nil}}
	s := domain.NewShooter(cam, &memStore{}, &recordLogger{})

	sum, err := s.Run(context.Background(), domain.ShootRequest{OutputDir: "o", Interval: time.Millisecond, Count: 3}, nil)
	if err != nil {
		t.Fatalf("a single failure must not abort the session: %v", err)
	}
	if sum.Captured != 2 || sum.Failed != 1 {
		t.Errorf("summary = %+v, want 2 captured / 1 failed", sum)
	}
}

func TestShooter_GivesUpAfterConsecutiveFailures(t *testing.T) {
	boom := errors.New("camera off")
	cam := &fakeCamera{errs: []error{boom, boom, boom, nil}}
	s := domain.NewShooter(cam, &memStore{}, &recordLogger{})

	_, err := s.Run(context.Background(), domain.ShootRequest{OutputDir: "o", Interval: time.Millisecond, Count: 10}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped camera error, got %v", err)
	}
	if cam.captures != 3 {
		t.Errorf("captures = %d, want 3 before giving up", cam.captures)
	}
}

func TestShooter_CancellationStopsGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cam := &fakeCamera{onShot: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	s := domain.NewShooter(cam, &memStore{}, &recordLogger{})

	sum, err := s.Run(ctx, domain.ShootRequest{OutputDir: "o", Interval: time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("cancellation should not be an error: %v", err)
	}
	if !sum.Stopped || sum.Captured != 2 {
		t.Errorf("summary = %+v, want stopped after 2 frames", sum)
	}
}

func TestShooter_PrepareErrorAborts(t *testing.T) {
	cam := &fakeCamera{prepareErr: errors.New("not in remote mode")}
	s := domain.NewShooter(cam, &memStore{}, &recordLogger{})
	if _, err := s.Run(context.Background(), domain.ShootRequest{OutputDir: "o", Interval: time.Millisecond, Count: 1}, nil); err == nil {
		t.Fatal("expected prepare error")
	}
	if cam.captures != 0 {
		t.Error("must not capture when the camera could not be prepared")
	}
}

func TestShooter_WarnsWhenCaptureOverrunsInterval(t *testing.T) {
	log := &recordLogger{}
	cam := &fakeCamera{onShot: func(int) { time.Sleep(15 * time.Millisecond) }}
	s := domain.NewShooter(cam, &memStore{}, log)

	if _, err := s.Run(context.Background(), domain.ShootRequest{OutputDir: "o", Interval: 5 * time.Millisecond, Count: 4}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(log.warnings) != 1 {
		t.Errorf("expected exactly one overrun warning, got %v", log.warnings)
	}
}
