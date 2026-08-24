package ffmpeg

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// barWidth is the number of cells in the rendered progress bar.
const barWidth = 24

// progressRenderer is an io.Writer that consumes FFmpeg's "-progress pipe:1"
// key=value stream and renders a progress bar to an underlying writer. FFmpeg
// emits blocks of key=value lines terminated by "progress=continue" (or
// "progress=end" for the final block); this type accumulates partial writes,
// tracks the current frame, and redraws on each block.
//
// On a terminal it redraws in place with a carriage return; when the output is
// redirected (a file or pipe) it instead prints a new line at every ~10% step so
// logs stay readable. It is not safe for concurrent use, which is fine: a single
// process writes to it sequentially.
type progressRenderer struct {
	out      io.Writer
	total    int
	tty      bool
	partial  []byte
	frame    int
	fps      string
	lastStep int  // last printed 10% bucket, for the non-TTY path
	drew     bool // whether anything has been drawn (so Finish can close the line)
	done     bool
}

// newProgressRenderer builds a renderer writing to out for a job of total
// frames. A non-positive total switches the bar to an indeterminate,
// frame-counting display.
func newProgressRenderer(out io.Writer, total int) *progressRenderer {
	return &progressRenderer{
		out:      out,
		total:    total,
		tty:      isTerminal(out),
		lastStep: -1,
	}
}

// Write implements io.Writer, buffering until whole lines are available.
func (p *progressRenderer) Write(b []byte) (int, error) {
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexByte(p.partial, '\n')
		if i < 0 {
			break
		}
		line := string(p.partial[:i])
		p.partial = p.partial[i+1:]
		p.handleLine(strings.TrimRight(line, "\r"))
	}
	return len(b), nil
}

// handleLine interprets a single key=value progress line.
func (p *progressRenderer) handleLine(line string) {
	key, val, ok := strings.Cut(line, "=")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	switch key {
	case "frame":
		if n, err := strconv.Atoi(val); err == nil {
			p.frame = n
		}
	case "fps":
		p.fps = val
	case "progress":
		p.render(val == "end")
	}
}

// render draws the current state. When done is true it forces 100% and closes
// the line.
func (p *progressRenderer) render(done bool) {
	if done {
		p.done = true
	}

	// Indeterminate mode: we don't know the total, so just count frames.
	if p.total <= 0 {
		if p.tty {
			fmt.Fprintf(p.out, "\r  encoding… %d frames", p.frame)
			p.drew = true
			if done {
				fmt.Fprintln(p.out)
			}
		} else if done {
			fmt.Fprintf(p.out, "  encoded %d frames\n", p.frame)
		}
		return
	}

	pct := p.frame * 100 / p.total
	if pct > 100 || done {
		pct = 100
	}
	shown := min(p.frame, p.total)

	if p.tty {
		line := fmt.Sprintf("\r  %s %3d%% · %d/%d frames", bar(pct), pct, shown, p.total)
		if p.fps != "" && p.fps != "0.00" && !done {
			line += " · " + p.fps + " fps"
		}
		fmt.Fprint(p.out, line)
		p.drew = true
		if done {
			fmt.Fprintln(p.out)
		}
		return
	}

	// Non-TTY: emit a line only when we cross a new 10% bucket (or finish).
	step := pct / 10
	if done || step != p.lastStep {
		fmt.Fprintf(p.out, "  encoding… %d%% (%d/%d frames)\n", pct, shown, p.total)
		p.lastStep = step
	}
}

// Finish closes an in-progress terminal line if FFmpeg exited before emitting a
// final "progress=end" block (for example on error), so subsequent output starts
// on a fresh line.
func (p *progressRenderer) Finish() {
	if p.tty && p.drew && !p.done {
		fmt.Fprintln(p.out)
	}
}

// bar renders a [██████░░░░] style bar filled to pct percent.
func bar(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct * barWidth / 100
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + "]"
}

// isTerminal reports whether w is a character device (an interactive terminal),
// using the classic no-dependency check so redirected output degrades to plain
// lines instead of carriage-return redraws.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// tailBuffer is a bounded io.Writer that retains only the last maxTailBytes of
// what is written to it. It captures FFmpeg's stderr without letting a chatty or
// runaway process consume unbounded memory; on failure the retained tail is the
// most relevant part (the error message near the end).
type tailBuffer struct {
	buf bytes.Buffer
}

// maxTailBytes bounds how much trailing stderr is kept for error reporting.
const maxTailBytes = 4096

func (t *tailBuffer) Write(b []byte) (int, error) {
	n := len(b)
	t.buf.Write(b)
	if t.buf.Len() > maxTailBytes {
		// Keep only the trailing maxTailBytes.
		trimmed := t.buf.Bytes()[t.buf.Len()-maxTailBytes:]
		next := bytes.NewBuffer(append([]byte(nil), trimmed...))
		t.buf = *next
	}
	return n, nil
}

func (t *tailBuffer) String() string { return t.buf.String() }
