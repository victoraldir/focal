package ffmpeg

import (
	"strings"
	"testing"
)

func TestProgressRenderer_HandlesSplitWrites(t *testing.T) {
	var buf strings.Builder
	r := newProgressRenderer(&buf, 10)

	// A key=value line arriving across two Write calls must still be parsed.
	r.Write([]byte("fra"))
	r.Write([]byte("me=5\nprogress=continue\n"))

	if !strings.Contains(buf.String(), "50%") {
		t.Errorf("expected 50%% after 5/10 frames, got: %q", buf.String())
	}
}

func TestProgressRenderer_Indeterminate(t *testing.T) {
	var buf strings.Builder
	r := newProgressRenderer(&buf, 0) // unknown total
	r.Write([]byte("frame=7\nprogress=end\n"))
	if !strings.Contains(buf.String(), "7 frames") {
		t.Errorf("indeterminate mode should count frames, got: %q", buf.String())
	}
}

func TestProgressRenderer_NonTTYStepsOnce(t *testing.T) {
	var buf strings.Builder
	r := newProgressRenderer(&buf, 100)
	// Two updates inside the same 10% bucket should print only once.
	r.Write([]byte("frame=1\nprogress=continue\n")) // 1%
	r.Write([]byte("frame=2\nprogress=continue\n")) // 2% (same bucket)
	if got := strings.Count(buf.String(), "encoding…"); got != 1 {
		t.Errorf("expected a single line within one 10%% bucket, got %d:\n%s", got, buf.String())
	}
}

func TestTailBuffer_KeepsOnlyTail(t *testing.T) {
	var tb tailBuffer
	// Write more than maxTailBytes; only the trailing portion is retained.
	big := strings.Repeat("A", maxTailBytes)
	tb.Write([]byte(big))
	tb.Write([]byte("TAIL-MARKER"))

	got := tb.String()
	if len(got) > maxTailBytes {
		t.Errorf("tail buffer exceeded bound: len=%d > %d", len(got), maxTailBytes)
	}
	if !strings.HasSuffix(got, "TAIL-MARKER") {
		t.Errorf("tail buffer should retain the most recent bytes, got suffix: %q", got[len(got)-20:])
	}
}
