package ui

import (
	"io"
	"os"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Stderr is where styled diagnostics go. Write to it, not os.Stderr, when
// the text came through a style.
//
// The colour decision in init is made once, from stdout, because lipgloss
// styles share one renderer. That is right for stdout and wrong for stderr
// whenever the two differ: `dots save 2>err.log` at a terminal would write
// escape codes into the log. Stripping at the writer keeps the styles global
// and still gives each stream the output its destination can take.
var Stderr io.Writer = stderrWriter{}

// stripStderr is decided once in init; tests flip it directly.
var stripStderr bool

type stderrWriter struct{}

// Write resolves os.Stderr per call rather than capturing it at init, so a
// test that swaps os.Stderr for a pipe still sees the output. fmt.Fprint*
// hands over each formatted string in one Write, so an escape sequence never
// straddles two calls.
func (stderrWriter) Write(p []byte) (int, error) {
	if !stripStderr {
		return os.Stderr.Write(p)
	}
	if _, err := io.WriteString(os.Stderr, ansi.Strip(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func init() {
	stripStderr = !term.IsTerminal(int(os.Stderr.Fd()))
}
