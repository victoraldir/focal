package focal

import (
	"fmt"
	"io"
)

// consoleLogger is a tiny domain.Logger implementation that writes human-readable
// info and warning lines to a writer (stderr by default). Keeping the logger
// behind the domain.Logger interface means the core use case never imports it.
type consoleLogger struct {
	out io.Writer
}

func newConsoleLogger(out io.Writer) *consoleLogger {
	return &consoleLogger{out: out}
}

func (l *consoleLogger) Infof(format string, args ...any) {
	fmt.Fprintf(l.out, "• "+format+"\n", args...)
}

func (l *consoleLogger) Warnf(format string, args ...any) {
	fmt.Fprintf(l.out, "⚠ warning: "+format+"\n", args...)
}
