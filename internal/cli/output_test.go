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

// baseStore is baseManifest without its groups, for tests that declare their
// own.
const baseStore = `
[store]
config    = "{{root}}/config.repo"
work_tree = "{{work}}"

[secrets]
identity = "{{root}}/identity.age"
vault    = "{{root}}/vault.age"
`

const brokenSourceManifest = `
[[dotfiles]]
name    = "shell"
include = [".bashrc"]

[packages.sh]
list     = "echo 'database locked' >&2; exit 1"
packages = ["a"]
`

// TestPkgDiffWarnsWhenASourceCannotBeListed: an installed tool whose list
// command fails is a failure, not the muted "unavailable on this machine" an
// absent tool gets, and the run has to exit non-zero.
func TestPkgDiffWarnsWhenASourceCannotBeListed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t, baseStore+brokenSourceManifest)
	out, err := e.run("pkg", "diff")
	if err == nil {
		t.Fatalf("pkg diff exited 0 with an unlistable source:\n%s", out)
	}
	if !strings.Contains(out, "could not list") || !strings.Contains(out, "database locked") {
		t.Fatalf("pkg diff hid why the source failed:\n%s", out)
	}
	if strings.Contains(out, "unavailable on this machine") {
		t.Fatalf("a failing tool was reported as absent:\n%s", out)
	}
}

func TestDoctorWarnsWhenASourceCannotBeListed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t, baseStore+brokenSourceManifest)
	out := captureDoctor(t, e.manifest)
	if strings.Contains(out, "all declared packages installed") {
		t.Fatalf("doctor called an unlistable source healthy:\n%s", out)
	}
	if !strings.Contains(lineWith(t, out, "packages"), "could not list sh") {
		t.Fatalf("doctor did not name the unlistable source:\n%s", out)
	}
}

// TestDoctorWarnsWhenTheStoreHasNoRemote: with no origin, Unpushed reports
// "cannot tell" and doctor printed no remote row at all, so a store that
// backs up nothing looked as healthy as one that is up to date.
func TestDoctorWarnsWhenTheStoreHasNoRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "x\n")
	if out, err := e.run("add", "--commit"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	out := captureDoctor(t, e.manifest)
	row := lineWith(t, out, "remote")
	if !strings.Contains(row, "warn") || !strings.Contains(row, "no origin") {
		t.Fatalf("doctor did not warn about the missing remote:\n%s", out)
	}
}
