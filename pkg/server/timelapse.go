package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/victoraldir/focal/pkg/domain"
	"github.com/victoraldir/focal/pkg/framestore"
)

// RenderFunc encodes the frames in inputDir into a video at outputPath. The
// CLI wires it to the same domain.Timelapser that powers `focal lapse`.
type RenderFunc func(ctx context.Context, inputDir, outputPath string, fps, maxHeight int, progress io.Writer) error

// TimelapseState is the externally visible state of the current (or most
// recent) interval session.
type TimelapseState struct {
	Running         bool         `json:"running"`
	Session         string       `json:"session,omitempty"`
	IntervalSeconds float64      `json:"intervalSeconds,omitempty"`
	Total           int          `json:"total"`
	Captured        int          `json:"captured"`
	Failed          int          `json:"failed"`
	StartedAt       *time.Time   `json:"startedAt,omitempty"`
	NextShotAt      *time.Time   `json:"nextShotAt,omitempty"`
	FinishedAt      *time.Time   `json:"finishedAt,omitempty"`
	LastFrame       *Media       `json:"lastFrame,omitempty"`
	Warning         string       `json:"warning,omitempty"`
	Error           string       `json:"error,omitempty"`
	Render          *RenderState `json:"render,omitempty"`
}

// RenderState tracks a video render of a session.
type RenderState struct {
	Running bool   `json:"running"`
	FPS     int    `json:"fps"`
	Output  *Media `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// TimelapseRequest starts an interval session.
type TimelapseRequest struct {
	IntervalSeconds float64 `json:"intervalSeconds"`
	Count           int     `json:"count"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// RenderRequest renders the latest session into a video.
type RenderRequest struct {
	FPS       int `json:"fps"`
	MaxHeight int `json:"maxHeight"`
}

var (
	errBusy        = errors.New("a timelapse session is already running")
	errNoSession   = errors.New("no timelapse session to render yet")
	errRendering   = errors.New("a render is already in progress")
	errNoRenderer  = errors.New("rendering is not available in this build")
	errStillActive = errors.New("stop the running session before rendering it")
)

// timelapseJob runs at most one interval session at a time in the background.
type timelapseJob struct {
	mediaDir string
	camera   func(ctx context.Context) (domain.Camera, error)
	render   RenderFunc

	mu     sync.Mutex
	state  TimelapseState
	cancel context.CancelFunc
}

func (j *timelapseJob) snapshot() TimelapseState {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.state
	if st.Render != nil {
		r := *st.Render
		st.Render = &r
	}
	return st
}

func (j *timelapseJob) start(req TimelapseRequest) (TimelapseState, error) {
	interval := time.Duration(req.IntervalSeconds * float64(time.Second))
	session := "timelapse-" + time.Now().Format("20060102-150405")
	sreq := domain.ShootRequest{
		OutputDir: filepath.Join(j.mediaDir, session),
		Interval:  interval,
		Count:     req.Count,
		Duration:  time.Duration(req.DurationSeconds * float64(time.Second)),
	}
	if err := sreq.Validate(); err != nil {
		return TimelapseState{}, &badRequestError{err}
	}
	if interval < 500*time.Millisecond {
		return TimelapseState{}, &badRequestError{errors.New("interval must be at least 0.5 seconds")}
	}

	j.mu.Lock()
	if j.state.Running {
		j.mu.Unlock()
		return TimelapseState{}, errBusy
	}
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.state = TimelapseState{
		Running:         true,
		Session:         session,
		IntervalSeconds: interval.Seconds(),
		Total:           sreq.TotalFrames(),
		StartedAt:       &now,
		NextShotAt:      &now,
	}
	st := j.state
	j.mu.Unlock()

	go j.run(ctx, sreq)
	return st, nil
}

func (j *timelapseJob) run(ctx context.Context, req domain.ShootRequest) {
	defer func() {
		j.mu.Lock()
		now := time.Now()
		j.state.Running, j.state.NextShotAt, j.state.FinishedAt = false, nil, &now
		j.cancel = nil
		j.mu.Unlock()
	}()

	cam, err := j.camera(ctx)
	if err != nil {
		j.setError(err)
		return
	}
	shooter := domain.NewShooter(cam, framestore.New(req.OutputDir), jobLogger{j})
	_, err = shooter.Run(ctx, req, func(ev domain.ShotEvent) {
		j.mu.Lock()
		defer j.mu.Unlock()
		if ev.Err != nil {
			j.state.Failed++
			j.state.Warning = ev.Err.Error()
		} else {
			j.state.Captured++
			if info, err := os.Stat(ev.Path); err == nil {
				if m, ok := newMedia(j.mediaDir, ev.Path, info); ok {
					j.state.LastFrame = &m
				}
			}
		}
		if ev.Next.IsZero() {
			j.state.NextShotAt = nil
		} else {
			next := ev.Next
			j.state.NextShotAt = &next
		}
	})
	if err != nil {
		j.setError(err)
	}
}

func (j *timelapseJob) setError(err error) {
	j.mu.Lock()
	j.state.Error = err.Error()
	j.mu.Unlock()
}

func (j *timelapseJob) stop() TimelapseState {
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	j.mu.Unlock()
	return j.snapshot()
}

func (j *timelapseJob) startRender(req RenderRequest) (TimelapseState, error) {
	if j.render == nil {
		return TimelapseState{}, errNoRenderer
	}
	if req.FPS == 0 {
		req.FPS = 30
	}
	if req.MaxHeight == 0 {
		req.MaxHeight = 1080
	}
	if req.FPS < 1 || req.FPS > 120 {
		return TimelapseState{}, &badRequestError{fmt.Errorf("fps must be between 1 and 120, got %d", req.FPS)}
	}

	j.mu.Lock()
	switch {
	case j.state.Session == "" || j.state.Captured == 0:
		j.mu.Unlock()
		return TimelapseState{}, errNoSession
	case j.state.Running:
		j.mu.Unlock()
		return TimelapseState{}, errStillActive
	case j.state.Render != nil && j.state.Render.Running:
		j.mu.Unlock()
		return TimelapseState{}, errRendering
	}
	session := j.state.Session
	j.state.Render = &RenderState{Running: true, FPS: req.FPS}
	j.mu.Unlock()

	go func() {
		in := filepath.Join(j.mediaDir, session)
		out := filepath.Join(j.mediaDir, session+".mp4")
		err := j.render(context.Background(), in, out, req.FPS, req.MaxHeight, io.Discard)

		j.mu.Lock()
		defer j.mu.Unlock()
		if j.state.Session != session || j.state.Render == nil {
			return // a newer session replaced this one
		}
		j.state.Render.Running = false
		if err != nil {
			j.state.Render.Error = err.Error()
			return
		}
		if info, statErr := os.Stat(out); statErr == nil {
			if m, ok := newMedia(j.mediaDir, out, info); ok {
				j.state.Render.Output = &m
			}
		}
	}()
	return j.snapshot(), nil
}

// jobLogger surfaces Shooter warnings (e.g. interval overruns) in the state.
type jobLogger struct{ j *timelapseJob }

func (l jobLogger) Infof(string, ...any) {}
func (l jobLogger) Warnf(format string, args ...any) {
	l.j.mu.Lock()
	l.j.state.Warning = fmt.Sprintf(format, args...)
	l.j.mu.Unlock()
}
