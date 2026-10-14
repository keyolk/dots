package ui

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// captureStderr runs fn with os.Stderr swapped for a pipe.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	saved := os.Stderr
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = wp
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(rp)
		done <- b.String()
	}()
	fn()
	wp.Close()
	os.Stderr = saved
	return <-done
}

// TestStderrIsPlainWhenRedirected covers `dots save 2>err.log` at a terminal:
// the colour profile follows stdout, so without stripping the log got escape
// codes.
func TestStderrIsPlainWhenRedirected(t *testing.T) {
	savedProfile, savedStrip := lipgloss.ColorProfile(), stripStderr
	lipgloss.SetColorProfile(termenv.TrueColor) // stdout is a terminal
	stripStderr = true                          // stderr is not
	defer func() { lipgloss.SetColorProfile(savedProfile); stripStderr = savedStrip }()

	got := captureStderr(t, func() {
		fmt.Fprintf(Stderr, "%s %s\n", Refused.Render("refused"), Muted.Render("why"))
	})
	if hasANSI(got) {
		t.Fatalf("redirected stderr carries escape codes: %q", got)
	}
	if got != "refused why\n" {
		t.Fatalf("stripping changed the text: %q", got)
	}
}

func TestStderrKeepsColourOnATerminal(t *testing.T) {
	savedProfile, savedStrip := lipgloss.ColorProfile(), stripStderr
	lipgloss.SetColorProfile(termenv.TrueColor)
	stripStderr = false
	defer func() { lipgloss.SetColorProfile(savedProfile); stripStderr = savedStrip }()

	got := captureStderr(t, func() { fmt.Fprint(Stderr, Fail.Render("x")) })
	if !hasANSI(got) {
		t.Fatalf("terminal stderr lost its colour: %q", got)
	}
}
