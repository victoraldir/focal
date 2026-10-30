package server

import (
	"context"
	"sync"
	"time"

	"github.com/victoraldir/focal/pkg/camera/sony"
)

// liveHub fans a single camera live-view stream out to any number of browser
// clients. The camera serves one stream at a time and Wi-Fi bandwidth is
// scarce, so the hub opens the stream when the first viewer arrives and closes
// it when the last one leaves.
type liveHub struct {
	open func(ctx context.Context) (*sony.LiveviewReader, error)

	mu       sync.Mutex
	subs     map[chan []byte]struct{}
	cancel   context.CancelFunc
	latest   []byte
	latestAt time.Time
}

func newLiveHub(open func(ctx context.Context) (*sony.LiveviewReader, error)) *liveHub {
	return &liveHub{open: open, subs: map[chan []byte]struct{}{}}
}

// subscribe registers a viewer. Frames arrive on the returned channel, which
// is closed if the camera stream fails. Slow viewers skip frames rather than
// stall the others. The returned func unsubscribes.
func (h *liveHub) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	if h.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.run(ctx)
	}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs, ch)
		if len(h.subs) == 0 && h.cancel != nil {
			h.cancel()
			h.cancel = nil
		}
	}
}

func (h *liveHub) run(ctx context.Context) {
	h.stream(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return // stopped because the last viewer left; a new run may already own the hub
	}
	// The stream failed while viewers were attached: release them so their
	// requests end and the browser can retry.
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
	h.cancel()
	h.cancel = nil
}

func (h *liveHub) stream(ctx context.Context) {
	lv, err := h.open(ctx)
	if err != nil {
		return
	}
	defer lv.Close()
	for {
		frame, err := lv.Next()
		if err != nil {
			return
		}
		h.mu.Lock()
		h.latest, h.latestAt = frame, time.Now()
		for ch := range h.subs {
			select {
			case <-ch: // drop the stale frame a slow viewer has not taken yet
			default:
			}
			ch <- frame
		}
		h.mu.Unlock()
	}
}

// snapshot returns a recent frame, briefly joining the stream if none is
// cached.
func (h *liveHub) snapshot(ctx context.Context) ([]byte, bool) {
	h.mu.Lock()
	if time.Since(h.latestAt) < time.Second && h.latest != nil {
		frame := h.latest
		h.mu.Unlock()
		return frame, true
	}
	h.mu.Unlock()

	ch, unsubscribe := h.subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case frame, ok := <-ch:
		return frame, ok
	case <-ctx.Done():
		return nil, false
	}
}
