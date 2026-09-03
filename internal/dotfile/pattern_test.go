package dotfile

import (
	"testing"

	"github.com/keyolk/dots/internal/manifest"
)

func TestStateStringsAreDistinct(t *testing.T) {
	// The strings are the --only filter's vocabulary, so a duplicate or empty
	// value would make a filter silently match the wrong set.
	seen := map[string]bool{}
	for _, s := range []State{Clean, Modified, Untracked, Missing, Undeclared} {
		name := s.String()
		if name == "" || name == "unknown" {
			t.Fatalf("state %d has no name", int(s))
		}
		if seen[name] {
			t.Fatalf("duplicate state name %q", name)
		}
		seen[name] = true
	}
}

func TestStateSymbolsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []State{Clean, Modified, Untracked, Missing, Undeclared} {
		sym := s.Symbol()
		if len(sym) != 1 {
			t.Fatalf("state %s has symbol %q, want one character", s, sym)
		}
		if seen[sym] {
			t.Fatalf("duplicate symbol %q", sym)
		}
		seen[sym] = true
	}
}

func TestSymbolsMatchGitVocabulary(t *testing.T) {
	// Muscle memory carries over from git status only if these agree.
	if Modified.Symbol() != "M" || Missing.Symbol() != "D" || Untracked.Symbol() != "?" {
		t.Fatalf("symbols diverge from git: M=%s D=%s ?=%s",
			Modified.Symbol(), Missing.Symbol(), Untracked.Symbol())
	}
}

func TestUnknownStateDoesNotPanic(t *testing.T) {
	s := State(99)
	if s.String() != "unknown" || s.Symbol() != "!" {
		t.Fatalf("unknown state rendered as %q/%q", s.String(), s.Symbol())
	}
}

func TestPatternRootTrimsAtTheFirstMetacharacter(t *testing.T) {
	cases := map[string]string{
		".claude/hooks/**/*.py":   ".claude/hooks",
		".config/fish/*.fish":     ".config/fish",
		".local/bin/**/*":         ".local/bin",
		"**/*.tmpl":               ".",
		".config/a/b/c.conf":      ".config/a/b/c.conf",
		".local/bin/{a,b}":        ".local/bin",
		".config/nvim/[abc]*.lua": ".config/nvim",
	}
	for pattern, want := range cases {
		if got := patternRoot(pattern); got != want {
			t.Fatalf("patternRoot(%q) = %q, want %q", pattern, got, want)
		}
	}
}

// TestPatternRootsDropsRedundantDescendants keeps the walk from visiting the
// same subtree twice: walking .claude and .claude/hooks separately reads every
// hook file two times.
func TestPatternRootsDropsRedundantDescendants(t *testing.T) {
	got := patternRoots([]string{".claude/**/*.py", ".claude/hooks/**/*.sh"})
	if len(got) != 1 || got[0] != ".claude" {
		t.Fatalf("patternRoots = %v, want just the ancestor", got)
	}
}

func TestPatternRootsKeepsSiblingRoots(t *testing.T) {
	got := patternRoots([]string{".local/bin/**/*", ".config/bin/**/*"})
	if len(got) != 2 {
		t.Fatalf("patternRoots = %v, want both sibling roots", got)
	}
}

func TestPatternRootsCollapsesEverythingUnderADotRoot(t *testing.T) {
	// If any pattern is unrooted the walk covers everything anyway, so keeping
	// narrower roots alongside it would only duplicate work.
	got := patternRoots([]string{"**/*.tmpl", ".config/**/*.fish"})
	if len(got) != 1 || got[0] != "." {
		t.Fatalf("patternRoots = %v, want the dot root alone", got)
	}
}

func TestIsUnderIdentifiesDescendants(t *testing.T) {
	cases := []struct {
		path, ancestor string
		want           bool
	}{
		{".claude/hooks", ".claude", true},
		{".claude", ".claude", false},
		{".claudex", ".claude", false}, // prefix without a separator is not a descendant
		{".config/fish", ".claude", false},
		{".anything", ".", true},
		{".", ".", false},
	}
	for _, c := range cases {
		if got := isUnder(c.path, c.ancestor); got != c.want {
			t.Fatalf("isUnder(%q, %q) = %v, want %v", c.path, c.ancestor, got, c.want)
		}
	}
}

func TestScannerRepoReturnsTheStore(t *testing.T) {
	f := newFixture(t)
	sc := NewScanner(f.m, "testhost")
	if sc.Repo().GitDir != f.m.Store.Config {
		t.Fatal("Repo did not return the configured store")
	}
}

// TestMaxDepthStopsAtTheShallowestUsefulLevel is a performance property with
// a correctness consequence: a pattern without ** matches at a fixed depth, so
// descending past it can only waste time. `.vim/*.vim` was walking 17137 files
// under .vim/plugged to find three.
func TestMaxDepthStopsAtTheShallowestUsefulLevel(t *testing.T) {
	cases := map[string]int{
		".vim/*.vim":            0, // files directly in .vim
		".config/fish/*.fish":   0,
		".claude/skills/**/*":   -1, // unbounded
		".claude/hooks/**/*.py": -1,
		".aws/cli/data/*":       0,
	}
	for pattern, want := range cases {
		got := maxDepth([]string{pattern})[patternRoot(pattern)]
		if got != want {
			t.Fatalf("maxDepth(%q) = %d, want %d", pattern, got, want)
		}
	}
}

// TestMaxDepthTakesTheDeepestRequirement guards the case where one root
// swallows another during deduplication: the surviving root has to walk deep
// enough for everything beneath it.
func TestMaxDepthTakesTheDeepestRequirement(t *testing.T) {
	d := maxDepth([]string{".claude/*.md", ".claude/hooks/*.py"})
	if got := d[".claude"]; got < 1 {
		t.Fatalf("maxDepth for .claude = %d, want at least 1 to reach hooks/", got)
	}
}

func TestMaxDepthUnboundedWins(t *testing.T) {
	// One ** anywhere under a root means the whole subtree is in play.
	d := maxDepth([]string{".claude/*.md", ".claude/skills/**/*"})
	if got := d[".claude"]; got != -1 {
		t.Fatalf("maxDepth = %d, want -1 when a ** pattern shares the root", got)
	}
}

func TestDepthUnderCountsLevels(t *testing.T) {
	cases := []struct {
		root, path string
		want       int
	}{
		{".vim", ".vim", 0},
		{".vim", ".vim/autoload", 1},
		{".vim", ".vim/plugged/foo", 2},
		{".config/fish", ".config/fish/functions", 1},
	}
	for _, c := range cases {
		if got := depthUnder(c.root, c.path); got != c.want {
			t.Fatalf("depthUnder(%q, %q) = %d, want %d", c.root, c.path, got, c.want)
		}
	}
}

// TestShallowPatternDoesNotDescend is the end-to-end version: a file that only
// a deeper walk would find must not appear for a depth-limited pattern.
func TestShallowPatternDoesNotDescend(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "editor", Include: []string{".vim/*.vim"}})
	f.write(".vim/top.vim", "\" top\n")
	f.write(".vim/plugged/some-plugin/deep.vim", "\" deep\n")

	got := f.scan()
	if _, ok := got[".vim/top.vim"]; !ok {
		t.Fatal("the shallow match was missed")
	}
	if _, ok := got[".vim/plugged/some-plugin/deep.vim"]; ok {
		t.Fatal("a file below the pattern's depth was matched")
	}
}

func TestRecursivePatternStillDescends(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "claude", Include: []string{".claude/skills/**/*"}})
	f.write(".claude/skills/a/SKILL.md", "x")
	f.write(".claude/skills/a/b/c/deep.md", "x")

	got := f.scan()
	for _, want := range []string{".claude/skills/a/SKILL.md", ".claude/skills/a/b/c/deep.md"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("%s was not matched by a ** pattern", want)
		}
	}
}
