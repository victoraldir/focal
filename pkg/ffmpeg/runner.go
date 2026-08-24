package ffmpeg

import (
	"context"
	"io"
	"os/exec"
)

// ExecRunner implements domain.FFmpegRunner by shelling out to the real FFmpeg
// binary via os/exec. It is the only place in the ffmpeg package that touches
// the operating system, which is what lets the Engine be unit-tested with a fake
// runner.
type ExecRunner struct{}

// NewExecRunner returns a runner that executes commands on the host.
func NewExecRunner() *ExecRunner { return &ExecRunner{} }

// Run executes name with args, connecting the process's stdout and stderr to
// the provided writers. FFmpeg's "-progress pipe:1" stream arrives on stdout
// (consumed by the progress renderer) and diagnostics arrive on stderr. The
// context governs cancellation: cancelling it terminates the child process.
func (ExecRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
