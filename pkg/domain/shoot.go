package domain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// maxConsecutiveFailures is how many shots in a row may fail before an
// interval session gives up. Wi-Fi links to a camera drop the odd request, so a
// single failure must not end an hour-long timelapse — but a camera that has
// switched off should not be retried forever.
const maxConsecutiveFailures = 3

// ShootRequest describes an interval-capture session.
type ShootRequest struct {
	// OutputDir is where frames are written.
	OutputDir string
	// Interval is the time between consecutive shutter releases.
	Interval time.Duration
	// Count is the number of frames to capture; zero means unbounded (stop on
	// Duration or cancellation).
	Count int
	// Duration bounds the session length; zero means unbounded. When both Count
	// and Duration are set, whichever is reached first ends the session.
	Duration time.Duration
}

// Validate reports values the Shooter cannot act on.
func (r ShootRequest) Validate() error {
	if r.OutputDir == "" {
		return fmt.Errorf("output directory is required")
	}
	if r.Interval <= 0 {
		return fmt.Errorf("interval must be positive, got %s", r.Interval)
	}
	if r.Count < 0 {
		return fmt.Errorf("count cannot be negative, got %d", r.Count)
	}
	if r.Duration < 0 {
		return fmt.Errorf("duration cannot be negative, got %s", r.Duration)
	}
	return nil
}

// TotalFrames returns how many frames the session will capture, or zero when it
// is unbounded. A Duration admits one frame per started interval, so 60s at 10s
// yields 6 frames (t=0,10,…,50).
func (r ShootRequest) TotalFrames() int {
	byDuration := 0
	if r.Duration > 0 {
		byDuration = int(math.Ceil(float64(r.Duration) / float64(r.Interval)))
	}
	switch {
	case r.Count > 0 && byDuration > 0:
		return min(r.Count, byDuration)
	case r.Count > 0:
		return r.Count
	default:
		return byDuration
	}
}

// ShotEvent reports the outcome of one scheduled shot to an observer.
type ShotEvent struct {
	// Index is the 1-based frame number.
	Index int
	// Total is the planned frame count, or zero for unbounded sessions.
	Total int
	// Path and Bytes describe the saved frame; empty when Err is set.
	Path  string
	Bytes int
	// TakenAt is when the shutter was released.
	TakenAt time.Time
	// Next is when the following shot is scheduled; zero after the last frame.
	Next time.Time
	// Err is non-nil when this shot failed. The session continues unless too
	// many shots fail in a row.
	Err error
}

// ShootSummary describes a finished session.
type ShootSummary struct {
	OutputDir string
	Captured  int
	Failed    int
	// Stopped reports that the session ended early because the context was
	// cancelled (e.g. Ctrl-C), as opposed to reaching its frame budget.
	Stopped bool
	Elapsed time.Duration
}

// Shooter is the interval-capture use case: it fires the camera on a fixed
// schedule and persists every frame, depending only on the Camera and
// FrameStore ports.
type Shooter struct {
	Camera Camera
	Store  FrameStore
	Log    Logger
}

// NewShooter wires the use case from its collaborators.
func NewShooter(camera Camera, store FrameStore, log Logger) *Shooter {
	return &Shooter{Camera: camera, Store: store, Log: log}
}

// Run captures frames until the request's budget is met or ctx is cancelled.
// Shots are anchored to the session start (start + i×interval) so timing errors
// do not accumulate; if a capture overruns its slot the next shot fires
// immediately. Cancellation is a normal way to end an unbounded session, so it
// returns the summary with a nil error and Stopped set. observer may be nil.
func (s *Shooter) Run(ctx context.Context, req ShootRequest, observer func(ShotEvent)) (summary ShootSummary, err error) {
	summary.OutputDir = req.OutputDir
	if err := req.Validate(); err != nil {
		return summary, fmt.Errorf("invalid request: %w", err)
	}
	if observer == nil {
		observer = func(ShotEvent) {}
	}

	if err := s.Camera.Prepare(ctx); err != nil {
		return summary, fmt.Errorf("preparing camera: %w", err)
	}

	total := req.TotalFrames()
	start := time.Now()
	defer func() { summary.Elapsed = time.Since(start) }()

	consecutiveFailures := 0
	warnedOverrun := false
	for i := 0; total == 0 || i < total; i++ {
		due := start.Add(time.Duration(i) * req.Interval)
		if req.Duration > 0 && i > 0 && due.Sub(start) >= req.Duration {
			break
		}
		if err := sleepUntil(ctx, due); err != nil {
			summary.Stopped = true
			return summary, nil
		}
		if lag := time.Since(due); lag > req.Interval && !warnedOverrun {
			s.Log.Warnf("capture is slower than the %s interval (running %s behind); frames will be spaced unevenly", req.Interval, lag.Round(100*time.Millisecond))
			warnedOverrun = true
		}

		ev := ShotEvent{Index: i + 1, Total: total, TakenAt: time.Now()}
		path, n, err := s.shoot(ctx, i+1)
		if err != nil && ctx.Err() != nil {
			summary.Stopped = true
			return summary, nil
		}
		last := total > 0 && i+1 >= total
		if next := start.Add(time.Duration(i+1) * req.Interval); !last && (req.Duration == 0 || next.Sub(start) < req.Duration) {
			ev.Next = next
		}
		ev.Path, ev.Bytes, ev.Err = path, n, err
		observer(ev)

		if err != nil {
			summary.Failed++
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				return summary, fmt.Errorf("giving up after %d consecutive failed shots: %w", consecutiveFailures, err)
			}
			continue
		}
		consecutiveFailures = 0
		summary.Captured++
	}
	return summary, nil
}

// shoot captures and stores frame number idx.
func (s *Shooter) shoot(ctx context.Context, idx int) (string, int, error) {
	takenAt := time.Now()
	photo, err := s.Camera.Capture(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("capturing frame %d: %w", idx, err)
	}
	if len(photo.Data) == 0 {
		return "", 0, errors.New("camera returned an empty image")
	}
	ext := photo.Ext
	if ext == "" {
		ext = ".jpg"
	}
	path, err := s.Store.Save(FrameName(idx, ext), photo.Data, takenAt)
	if err != nil {
		return "", 0, fmt.Errorf("saving frame %d: %w", idx, err)
	}
	return path, len(photo.Data), nil
}

// FrameName is the file name used for the idx-th frame of a session. The
// zero-padded counter keeps lexical and chronological order identical.
func FrameName(idx int, ext string) string {
	return fmt.Sprintf("frame_%05d%s", idx, ext)
}

// sleepUntil blocks until t or until ctx is done, returning ctx's error in the
// latter case.
func sleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
