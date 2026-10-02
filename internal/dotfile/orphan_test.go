package dotfile

import (
	"testing"

	"github.com/keyolk/dots/internal/manifest"
)

func paths(orphans []Orphan) []string {
	out := make([]string, 0, len(orphans))
	for _, o := range orphans {
		out = append(out, o.Path)
	}
	return out
}

// TestOrphansFindsAFileBesideATrackedOne is the gap Untracked cannot report:
// Untracked means the manifest already knows the path. This is the file it has
// never heard of.
func TestOrphansFindsAFileBesideATrackedOne(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "claude",
		Include: []string{".claude/hooks/**/*.py"},
	})
	f.write(".claude/hooks/guard.py", "x")
	f.write(".claude/hooks/README.md", "docs")

	got, err := NewScanner(f.m, "testhost").Orphans()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != ".claude/hooks/README.md" {
		t.Fatalf("Orphans() = %v, want just the README", paths(got))
	}
	if got[0].Group != "claude" {
		t.Fatalf("attributed to %q, want the group that declared its neighbour", got[0].Group)
	}
}

// TestOrphansRespectsExcludes: an exclude is a decision already made, and
// reporting it again is how a report becomes something you learn to skip.
func TestOrphansRespectsExcludes(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "claude",
		Include: []string{".claude/hooks/**/*.py"},
		Exclude: []string{".claude/**/*.pyc"},
	})
	f.write(".claude/hooks/guard.py", "x")
	f.write(".claude/hooks/guard.pyc", "compiled")

	got, err := NewScanner(f.m, "testhost").Orphans()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Orphans() = %v, want nothing: the .pyc is excluded", paths(got))
	}
}

// TestOrphansExcludeCountsAcrossGroups: an exclude in one group that nothing
// else claims is the exclude working, not an unmade decision.
func TestOrphansExcludeCountsAcrossGroups(t *testing.T) {
	f := newFixture(t,
		manifest.Group{Name: "a", Include: []string{".claude/*.json"}, Exclude: []string{".claude/*.log"}},
		manifest.Group{Name: "b", Include: []string{".claude/*.md"}},
	)
	f.write(".claude/settings.json", "{}")
	f.write(".claude/daemon.log", "noise")

	got, err := NewScanner(f.m, "testhost").Orphans()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Orphans() = %v, want nothing", paths(got))
	}
}

// TestOrphansSurveysOnlyDirectoriesHoldingATrackedFile is what keeps the
// survey finite. An include root is not a claim on a directory: `.aws/**/*.tmpl`
// says templates under ~/.aws are declared, and surveying by root walked
// 145481 files there, nearly all of them a credential cache.
func TestOrphansSurveysOnlyDirectoriesHoldingATrackedFile(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "templated",
		Include: []string{".aws/**/*.tmpl"},
	})
	f.write(".aws/config.tmpl", "x")
	f.write(".aws/cli/cache/0addbafe.json", "a credential cache entry")
	f.write(".aws/cli/cache/1db5cb6d.json", "another")

	got, err := NewScanner(f.m, "testhost").Orphans()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Orphans() = %v, want nothing: no tracked file lives in that cache", paths(got))
	}
}

// TestOrphansIgnoresAnInactiveGroupsFiles: a linux-only path on a Mac is the
// other machine's config, which is why status reports Inactive rather than
// Undeclared.
func TestOrphansIgnoresAnInactiveGroupsFiles(t *testing.T) {
	f := newFixture(t,
		manifest.Group{Name: "shell", Include: []string{".config/sh/*.sh"}},
		manifest.Group{Name: "linux", OS: []string{"plan9"}, Include: []string{".config/sh/*.linux"}},
	)
	f.write(".config/sh/env.sh", "x")
	f.write(".config/sh/env.linux", "x")

	got, err := NewScanner(f.m, "testhost").Orphans()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Orphans() = %v, want nothing: the .linux file belongs to another machine", paths(got))
	}
}

// TestOrphansAttributionIsDeterministic: two groups declaring into one
// directory must not hand the directory to a different one on every run, or
// triage --apply writes the same pattern into a different group each time.
func TestOrphansAttributionIsDeterministic(t *testing.T) {
	f := newFixture(t,
		manifest.Group{Name: "claude", Include: []string{".claude/*.md"}},
		manifest.Group{Name: "claude-plugins", Include: []string{".claude/*.json"}},
	)
	f.write(".claude/CLAUDE.md", "x")
	f.write(".claude/settings.json", "{}")
	f.write(".claude/daemon.sock", "")

	for i := 0; i < 20; i++ {
		got, err := NewScanner(f.m, "testhost").Orphans()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("Orphans() = %v, want the socket", paths(got))
		}
		// Manifest order breaks the tie, the same precedence the scan uses
		// when two groups claim one path.
		if got[0].Group != "claude" {
			t.Fatalf("run %d attributed it to %q, want the first group in the manifest",
				i, got[0].Group)
		}
	}
}
