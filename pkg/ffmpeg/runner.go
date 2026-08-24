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

// Run executes name with args, streaming the process's stderr to the provided
// writer so FFmpeg's progress output reaches the user in real time. The context
// governs cancellation: cancelling it terminates the child process.
func (ExecRunner) Run(ctx context.Context, name string, args []string, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = stderr
	return cmd.Run()
}
