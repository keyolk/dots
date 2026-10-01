package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyKeepsComments is the property that justifies editing text rather
// than decoding and re-encoding: the reason a line exists is a comment, and a
// TOML round-trip drops every one of them.
func TestApplyKeepsComments(t *testing.T) {
	src := `[store]
config = "/tmp/x"

[[dotfiles]]
name    = "claude"
include = [
  ".claude/hooks/**/*.py",
]
exclude = [
  ".claude/**/*.pyc",     # compiled, regenerated on every run
  # Only the fixtures that embed fake tokens.
  ".claude/hooks/test_bash_command_guard.py",
]
`
	path := filepath.Join(t.TempDir(), "dots.toml")
	write(t, path, src)

	n, err := Apply(path, []Edit{{Group: "claude", Key: "exclude", Pattern: ".claude/*.log"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n != 1 {
		t.Fatalf("added %d lines, want 1", n)
	}
	got := read(t, path)
	for _, want := range []string{
		"# compiled, regenerated on every run",
		"# Only the fixtures that embed fake tokens.",
		`".claude/*.log",`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the edit lost %q:\n%s", want, got)
		}
	}
	m, err := Load(path)
	if err != nil {
		t.Fatalf("the edited manifest no longer loads: %v", err)
	}
	if len(m.Dotfiles[0].Exclude) != 3 {
		t.Fatalf("exclude has %d entries, want 3", len(m.Dotfiles[0].Exclude))
	}
}

// TestApplyExpandsAOneLineArray: growing `exclude = ["a", "b", ...]` sideways
// produces a line nobody reads in review.
func TestApplyExpandsAOneLineArray(t *testing.T) {
	src := `[store]
config = "/tmp/x"

[[dotfiles]]
name    = "git"
include = [".gitconfig"]
exclude = [".config/pass-git-helper/*.bak.*"]
`
	path := filepath.Join(t.TempDir(), "dots.toml")
	write(t, path, src)

	if _, err := Apply(path, []Edit{{Group: "git", Key: "exclude", Pattern: ".gitconfig.bak"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := read(t, path)
	if strings.Contains(got, `exclude = [".config/pass-git-helper/*.bak.*", ".gitconfig.bak"]`) {
		t.Fatalf("the array grew sideways:\n%s", got)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dotfiles[0].Exclude) != 2 {
		t.Fatalf("exclude = %v, want both entries", m.Dotfiles[0].Exclude)
	}
}

// TestApplyCreatesAMissingKey covers the group that has only includes.
func TestApplyCreatesAMissingKey(t *testing.T) {
	src := `[store]
config = "/tmp/x"

[[dotfiles]]
name    = "terminal"
include = [".tmux.conf"]

[[dotfiles]]
name    = "editor"
include = [".vimrc"]
`
	path := filepath.Join(t.TempDir(), "dots.toml")
	write(t, path, src)

	if _, err := Apply(path, []Edit{{Group: "terminal", Key: "exclude", Pattern: ".tmux.conf.bak"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dotfiles[0].Exclude) != 1 || m.Dotfiles[0].Exclude[0] != ".tmux.conf.bak" {
		t.Fatalf("terminal.exclude = %v", m.Dotfiles[0].Exclude)
	}
	// The key must land in the named group, not the one after it.
	if len(m.Dotfiles[1].Exclude) != 0 {
		t.Fatalf("the edit leaked into the next group: %v", m.Dotfiles[1].Exclude)
	}
}

// TestApplyFindsTheRightGroup: group blocks are told apart by name, and a
// bracket inside a glob must not be read as the end of an array.
func TestApplyFindsTheRightGroup(t *testing.T) {
	src := `[store]
config = "/tmp/x"

[[dotfiles]]
name    = "scripts"
include = [".local/bin/**/*"]
exclude = [
  ".local/bin/{argx,ccx,dots}",
  ".local/bin/[0-9]*",
]

[[dotfiles]]
name    = "shell"
include = [".bashrc"]
`
	path := filepath.Join(t.TempDir(), "dots.toml")
	write(t, path, src)

	if _, err := Apply(path, []Edit{{Group: "shell", Key: "exclude", Pattern: ".bashrc.bak"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dotfiles[0].Exclude) != 2 {
		t.Fatalf("the scripts group changed: %v", m.Dotfiles[0].Exclude)
	}
	if len(m.Dotfiles[1].Exclude) != 1 {
		t.Fatalf("shell.exclude = %v, want the new entry", m.Dotfiles[1].Exclude)
	}
}

func TestApplyRejectsAnUnknownGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dots.toml")
	write(t, path, "[store]\nconfig = \"/tmp/x\"\n\n[[dotfiles]]\nname = \"a\"\ninclude = [\".x\"]\n")
	before := read(t, path)

	if _, err := Apply(path, []Edit{{Group: "nope", Key: "exclude", Pattern: ".y"}}); err == nil {
		t.Fatal("Apply accepted a group that does not exist")
	}
	if read(t, path) != before {
		t.Fatal("the manifest was modified despite the error")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
