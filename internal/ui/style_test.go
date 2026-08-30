package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// hasANSI reports whether a rendered string carries escape sequences.
func hasANSI(s string) bool { return strings.Contains(s, "\x1b[") }

// TestStylesRenderPlainWhenColourIsOff is what keeps `dots status | grep` and
// every string assertion in the CLI tests working: the package init disables
// colour off a terminal, and a style that ignored that would put escape bytes
// into piped output.
func TestStylesRenderPlainWhenColourIsOff(t *testing.T) {
	// The test binary's stdout is not a terminal, so init already chose Ascii.
	for name, style := range map[string]lipgloss.Style{
		"OK": OK, "Warn": Warn, "Fail": Fail,
		"StateModified": StateModified, "StateUntracked": StateUntracked,
		"StateMissing": StateMissing, "Heading": Heading, "Muted": Muted,
	} {
		got := style.Render("text")
		if hasANSI(got) {
			t.Fatalf("%s rendered escape codes with colour off: %q", name, got)
		}
		if !strings.Contains(got, "text") {
			t.Fatalf("%s dropped its content: %q", name, got)
		}
	}
}

// TestAsciiIsNotZero guards a mistake that fails silently in the wrong
// direction: termenv.TrueColor is 0, so `SetColorProfile(0)` reads like
// "turn colour off" and actually forces it on.
func TestAsciiIsNotZero(t *testing.T) {
	if termenv.Ascii == 0 {
		t.Fatal("termenv.Ascii is 0; the init guard would be a no-op")
	}
	if termenv.TrueColor != 0 {
		t.Fatal("termenv.TrueColor is no longer 0; revisit the comment in init")
	}
}

func TestStylesProduceColourWhenEnabled(t *testing.T) {
	// Force a colour profile to prove the styles are actually configured, not
	// merely inert.
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	for name, style := range map[string]lipgloss.Style{
		"OK": OK, "Warn": Warn, "Fail": Fail,
		"StateModified": StateModified, "StateUntracked": StateUntracked,
		"StateMissing": StateMissing, "StateStructural": StateStructural,
	} {
		if got := style.Render("x"); !hasANSI(got) {
			t.Fatalf("%s produced no colour with TrueColor enabled: %q", name, got)
		}
	}
}

// TestVerdictColoursAreDistinct matters because ok/warn/FAIL are scanned as a
// column: two of them sharing a colour would defeat the point.
func TestVerdictColoursAreDistinct(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	seen := map[string]string{}
	for name, style := range map[string]lipgloss.Style{
		"OK": OK, "Warn": Warn, "Fail": Fail,
	} {
		got := style.Render("x")
		if prev, dup := seen[got]; dup {
			t.Fatalf("%s and %s render identically", name, prev)
		}
		seen[got] = name
	}
}

// TestCleanIsUnstyled is a deliberate design choice worth pinning: clean is
// the overwhelming majority of any listing, and colouring it would bury the
// states that need action.
func TestCleanIsUnstyled(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	if got := StateClean.Render("path"); hasANSI(got) {
		t.Fatalf("StateClean is styled: %q", got)
	}
}

// TestWidthPadsInsideTheStyle covers the alignment bug this package caused:
// %-14s counts escape bytes as characters, so padding has to be applied by
// lipgloss, which measures printable width.
func TestWidthPadsInsideTheStyle(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	got := Heading.Width(14).Render("vault")
	if w := lipgloss.Width(got); w != 14 {
		t.Fatalf("printable width = %d, want 14", w)
	}
}
