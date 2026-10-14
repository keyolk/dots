package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// lineWith returns the first output line containing needle.
func lineWith(t *testing.T, out, needle string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	t.Fatalf("no line contains %q:\n%s", needle, out)
	return ""
}

// TestConfigKeepsALongGroupNameOnItsRow: the group column used Style.Width,
// which wraps, so a name longer than the column split its row in two and the
// match count landed on a line of its own.
func TestConfigKeepsALongGroupNameOnItsRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name    = "claude-code-plugins-extra"
include = [".claude/plugins/**"]
`)
	out, err := e.run("config")
	if err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	row := lineWith(t, out, "claude-code-plugins-extra")
	if !strings.Contains(row, "matched") {
		t.Fatalf("the long group name was wrapped off its row:\n%s", out)
	}
}

// TestTriageAlignsCJKPatterns: the pattern column was padded with %-38s,
// which counts runes, so a Korean file name (two cells per rune) pushed its
// reason column further right than its neighbours'.
func TestTriageAlignsCJKPatterns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := triageEnv(t)
	e.write(".claude/hooks/guard.py", "x")
	if out, err := e.run("add", "--commit"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	e.write(".claude/hooks/state", "opaque")
	e.write(".claude/hooks/메모장", "opaque")

	out, err := e.run("triage")
	if err != nil {
		t.Fatalf("triage: %v\n%s", err, out)
	}
	col := func(needle string) int {
		row := lineWith(t, out, needle)
		i := strings.Index(row, "no rule matched")
		if i < 0 {
			t.Fatalf("row for %q has no reason:\n%s", needle, out)
		}
		return lipgloss.Width(row[:i])
	}
	if a, b := col("hooks/state"), col("hooks/메모장"); a != b {
		t.Fatalf("reason column at cell %d for ASCII, %d for CJK:\n%s", a, b, out)
	}
}
