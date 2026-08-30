package dotfile

import (
	"testing"
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

func TestScannerRepoSelectsTheNamedStore(t *testing.T) {
	f := newFixture(t)
	sc := NewScanner(f.m, "testhost")
	if sc.Repo("secret").GitDir != f.m.Store.Secret {
		t.Fatal("Repo(\"secret\") did not return the secret store")
	}
	// Anything other than "secret" must fall back to config rather than
	// returning nil, which would panic at the call site.
	if sc.Repo("config").GitDir != f.m.Store.Config {
		t.Fatal("Repo(\"config\") did not return the config store")
	}
	if sc.Repo("").GitDir != f.m.Store.Config {
		t.Fatal("Repo(\"\") did not fall back to the config store")
	}
}
