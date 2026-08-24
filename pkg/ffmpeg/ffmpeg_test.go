package ffmpeg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/victoraldir/focal/pkg/domain"
	"github.com/victoraldir/focal/pkg/ffmpeg"
)

// fakeRunner records the command it was asked to run instead of executing it,
// and can emit canned bytes to the stdout/stderr writers to exercise the
// encoder's progress and error handling.
type fakeRunner struct {
	name       string
	args       []string
	emitStdout string
	emitStderr string
	err        error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdout, stderr io.Writer) error {
	f.name, f.args = name, args
	if f.emitStdout != "" {
		io.WriteString(stdout, f.emitStdout)
	}
	if f.emitStderr != "" {
		io.WriteString(stderr, f.emitStderr)
	}
	return f.err
}

func TestBuildArgs_Shape(t *testing.T) {
	args := ffmpeg.BuildArgs(domain.EncodeRequest{
		FFmpegPath:     "/bin/ffmpeg",
		ConcatFilePath: "/tmp/list.txt",
		OutputPath:     "out.mp4",
		FPS:            24,
		TotalFrames:    100,
	})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-f concat", "-safe 0", "-i /tmp/list.txt",
		"-pix_fmt yuv420p", "pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-r 24", "-progress pipe:1", "-nostats",
		"-fps_mode cfr", "-frames:v 100",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if args[len(args)-1] != "out.mp4" {
		t.Errorf("output must be the last argument, got %q", args[len(args)-1])
	}
}

func TestBuildArgs_ScaleFilter(t *testing.T) {
	// No scale requested: filter is pad-only.
	plain := strings.Join(ffmpeg.BuildArgs(domain.EncodeRequest{FPS: 30}), " ")
	if strings.Contains(plain, "scale=") {
		t.Errorf("did not expect a scale filter when ScaleHeight is 0: %q", plain)
	}

	// Scale requested: downscale to height with even auto width, then pad.
	scaled := ffmpeg.BuildArgs(domain.EncodeRequest{FPS: 30, ScaleHeight: 2160})
	var vf string
	for i, a := range scaled {
		if a == "-vf" && i+1 < len(scaled) {
			vf = scaled[i+1]
		}
	}
	if vf != "scale=-2:2160,pad=ceil(iw/2)*2:ceil(ih/2)*2" {
		t.Errorf("unexpected -vf value: %q", vf)
	}
}

func TestBuildArgs_NoFramesLimitWhenUnknown(t *testing.T) {
	// TotalFrames 0 (unknown) must not emit a -frames:v cap.
	args := strings.Join(ffmpeg.BuildArgs(domain.EncodeRequest{FPS: 30}), " ")
	if strings.Contains(args, "-frames:v") {
		t.Errorf("should not cap frames when TotalFrames is 0: %q", args)
	}
}

func TestEngine_Encode_ErrorIncludesStderrTail(t *testing.T) {
	engine := ffmpeg.NewEngine(&fakeRunner{
		err:        errors.New("exit 1"),
		emitStderr: "Unknown encoder 'nope'\n",
	})
	err := engine.Encode(context.Background(), domain.EncodeRequest{
		FFmpegPath: "/bin/ffmpeg", ConcatFilePath: "x", OutputPath: "y", FPS: 30, TotalFrames: 3,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Unknown encoder") {
		t.Errorf("error should surface FFmpeg's stderr tail, got: %v", err)
	}
}

func TestEngine_Encode_RendersProgress(t *testing.T) {
	// FFmpeg-style progress stream for a 4-frame job reaching completion.
	runner := &fakeRunner{
		emitStdout: "frame=2\nfps=10.0\nprogress=continue\nframe=4\nprogress=end\n",
	}
	var buf bytes.Buffer
	engine := ffmpeg.NewEngine(runner)

	err := engine.Encode(context.Background(), domain.EncodeRequest{
		FFmpegPath: "/bin/ffmpeg", ConcatFilePath: "x", OutputPath: "y", FPS: 30, TotalFrames: 4,
	}, &buf)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "50%") {
		t.Errorf("expected a 50%% checkpoint in progress output:\n%s", out)
	}
	if !strings.Contains(out, "100%") {
		t.Errorf("expected a 100%% completion in progress output:\n%s", out)
	}
}

func TestEngine_Encode_InvokesRunner(t *testing.T) {
	runner := &fakeRunner{}
	engine := ffmpeg.NewEngine(runner)

	err := engine.Encode(context.Background(), domain.EncodeRequest{
		FFmpegPath:     "/bin/ffmpeg",
		ConcatFilePath: "/tmp/list.txt",
		OutputPath:     "out.mp4",
		FPS:            30,
	}, io.Discard)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	if runner.name != "/bin/ffmpeg" {
		t.Errorf("runner invoked with %q, want /bin/ffmpeg", runner.name)
	}
}

func TestEngine_Encode_PropagatesRunnerError(t *testing.T) {
	engine := ffmpeg.NewEngine(&fakeRunner{err: errors.New("exit 1")})
	err := engine.Encode(context.Background(), domain.EncodeRequest{
		FFmpegPath: "/bin/ffmpeg", ConcatFilePath: "x", OutputPath: "y", FPS: 30,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected runner error to propagate")
	}
}

func TestEngine_Encode_EmptyPath(t *testing.T) {
	engine := ffmpeg.NewEngine(&fakeRunner{})
	if err := engine.Encode(context.Background(), domain.EncodeRequest{}, io.Discard); err == nil {
		t.Fatal("expected error for empty ffmpeg path")
	}
}

func TestConcatGenerator_Build(t *testing.T) {
	gen := ffmpeg.NewConcatGenerator()
	images := []domain.Image{
		{Path: "/photos/a.jpg"},
		{Path: "/photos/b.jpg"},
	}

	path, cleanup, err := gen.Build(images, 10)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "file '/photos/a.jpg'") {
		t.Errorf("concat file missing first frame:\n%s", content)
	}
	if !strings.Contains(content, "duration 0.100000") {
		t.Errorf("expected per-frame duration of 0.1s at 10fps:\n%s", content)
	}
	// Last frame must appear one extra time so the demuxer honours its duration.
	if strings.Count(content, "file '/photos/b.jpg'") != 2 {
		t.Errorf("last frame should be repeated once, got:\n%s", content)
	}
}

func TestConcatGenerator_Build_CleanupRemovesFile(t *testing.T) {
	gen := ffmpeg.NewConcatGenerator()
	path, cleanup, err := gen.Build([]domain.Image{{Path: "/a.jpg"}}, 30)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup should have removed %s", path)
	}
}

func TestConcatGenerator_Build_Errors(t *testing.T) {
	gen := ffmpeg.NewConcatGenerator()
	if _, _, err := gen.Build(nil, 30); err == nil {
		t.Error("expected error for empty image slice")
	}
	if _, _, err := gen.Build([]domain.Image{{Path: "/a.jpg"}}, 0); err == nil {
		t.Error("expected error for non-positive fps")
	}
}

func TestConcatGenerator_Build_EscapesQuotes(t *testing.T) {
	gen := ffmpeg.NewConcatGenerator()
	path, cleanup, err := gen.Build([]domain.Image{{Path: "/photos/o'brien.jpg"}}, 30)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	defer cleanup()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `'\''`) {
		t.Errorf("single quote in path was not escaped:\n%s", data)
	}
}
