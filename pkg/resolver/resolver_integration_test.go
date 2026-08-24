package resolver_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/victoraldir/focal/pkg/resolver"
)

type testLogger struct{ t *testing.T }

func (l testLogger) Infof(f string, a ...any) { l.t.Logf(f, a...) }
func (l testLogger) Warnf(f string, a ...any) { l.t.Logf("WARN "+f, a...) }

// TestResolve_RealDownload exercises the full resolver against the live static
// build URL: it downloads, extracts, and runs `ffmpeg -version`. It is network-
// and platform-dependent, so it is opt-in via FOCAL_INTEGRATION=1 and is not run
// by the default `go test ./...` (and thus not in CI).
//
//	FOCAL_INTEGRATION=1 go test ./pkg/resolver -run RealDownload -v
func TestResolve_RealDownload(t *testing.T) {
	if os.Getenv("FOCAL_INTEGRATION") != "1" {
		t.Skip("set FOCAL_INTEGRATION=1 to run the live download test")
	}

	cache := t.TempDir()
	r := resolver.New(
		resolver.NewHTTPDownloader(nil),
		testLogger{t},
		// Force the download path even though ffmpeg may be on PATH here.
		resolver.WithLookPath(func(string) (string, error) { return "", exec.ErrNotFound }),
		resolver.WithCacheDir(func() (string, error) { return cache, nil }),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	path, err := r.Resolve(ctx)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	out, err := exec.CommandContext(ctx, path, "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("running downloaded ffmpeg: %v\n%s", err, out)
	}
	t.Logf("downloaded ffmpeg reports: %s", firstLine(out))
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}
