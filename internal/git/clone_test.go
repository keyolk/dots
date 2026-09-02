package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// originRepo builds a normal (non-bare) repository with one commit, to act as
// the remote that a fresh machine clones from.
func originRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := filepath.Join(t.TempDir(), "origin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "dots test")

	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "initial")
	return dir
}

func TestCloneCreatesABareRepoWithUntrackedFilesHidden(t *testing.T) {
	// A bare repo over $HOME that reports untracked files reports the entire
	// home directory, so Clone must set this or the store is unusable.
	origin := originRepo(t, map[string]string{".bashrc": "export A=1\n"})
	root := t.TempDir()
	gitDir := filepath.Join(root, "config.repo")
	work := filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Clone(origin, gitDir, work)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if !r.Exists() {
		t.Fatal("cloned repo reports as absent")
	}

	got, err := r.Run("config", "status.showUntrackedFiles")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.TrimSpace(got) != "no" {
		t.Fatalf("status.showUntrackedFiles = %q, want no", strings.TrimSpace(got))
	}
}

func TestCloneOfAMissingURLFails(t *testing.T) {
	root := t.TempDir()
	_, err := Clone(filepath.Join(root, "no-such-repo"), filepath.Join(root, "g"), root)
	if err == nil {
		t.Fatal("Clone of a nonexistent source succeeded")
	}
}

func TestCheckoutPopulatesAnEmptyWorkTree(t *testing.T) {
	origin := originRepo(t, map[string]string{
		".bashrc":                "export A=1\n",
		".config/fish/a.fish":    "set -x A 1\n",
		".claude/hooks/guard.py": "print('x')\n",
	})
	root := t.TempDir()
	work := filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Clone(origin, filepath.Join(root, "config.repo"), work)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	conflicts, err := r.Checkout()
	if err != nil {
		t.Fatalf("Checkout into an empty tree: %v (conflicts: %v)", err, conflicts)
	}

	for _, want := range []string{".bashrc", ".config/fish/a.fish", ".claude/hooks/guard.py"} {
		if _, err := os.Stat(filepath.Join(work, want)); err != nil {
			t.Fatalf("%s was not checked out: %v", want, err)
		}
	}
}

// TestCheckoutRefusesToClobberExistingFiles is the property that makes
// bootstrapping onto a machine that already has content safe: a home directory
// is not an empty checkout target, and silently overwriting it destroys work.
func TestCheckoutRefusesToClobberExistingFiles(t *testing.T) {
	origin := originRepo(t, map[string]string{".bashrc": "from-repo\n"})
	root := t.TempDir()
	work := filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(work, ".bashrc")
	if err := os.WriteFile(local, []byte("local-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Clone(origin, filepath.Join(root, "config.repo"), work)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	conflicts, err := r.Checkout()
	if err == nil {
		t.Fatal("Checkout overwrote an existing file instead of refusing")
	}
	if len(conflicts) == 0 {
		t.Fatal("Checkout refused but named no conflicting path")
	}

	body, rerr := os.ReadFile(local)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(body) != "local-content\n" {
		t.Fatalf("the local file was modified: %q", body)
	}
}

func TestRunInteractiveExecutesTheCommand(t *testing.T) {
	// The streaming variant bypasses stdout capture, so the check is that a
	// valid command succeeds and an invalid one does not.
	r := newRepo(t)
	if err := r.RunInteractive("rev-parse", "--is-bare-repository"); err != nil {
		t.Fatalf("RunInteractive on a valid command: %v", err)
	}
	if err := r.RunInteractive("definitely-not-a-git-command"); err == nil {
		t.Fatal("RunInteractive reported success for an invalid command")
	}
}

// TestCloneSetsAFetchRefspec guards a gap that hides itself: `git clone
// --bare` writes no fetch refspec, so origin/<branch> never appears and
// nothing -- not doctor, not `git status` -- can tell whether a commit has
// been pushed. The store reads as healthy while every other machine is behind.
func TestCloneSetsAFetchRefspec(t *testing.T) {
	origin := originRepo(t, map[string]string{".bashrc": "x\n"})
	root := t.TempDir()
	work := filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Clone(origin, filepath.Join(root, "config.repo"), work)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	got, err := r.Run("config", "--get", "remote.origin.fetch")
	if err != nil {
		t.Fatalf("no fetch refspec was configured: %v", err)
	}
	if !strings.Contains(got, "refs/remotes/origin/") {
		t.Fatalf("fetch refspec = %q, want one mapping into refs/remotes/origin/", got)
	}
}

func TestUnpushedCountsCommitsTheRemoteLacks(t *testing.T) {
	origin := originRepo(t, map[string]string{".bashrc": "x\n"})
	root := t.TempDir()
	work := filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := Clone(origin, filepath.Join(root, "config.repo"), work)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := r.Run("config", "user.email", "t@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run("config", "user.name", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Checkout(); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Freshly cloned: nothing local the remote does not have.
	if n, err := r.Unpushed(); err != nil || n != 0 {
		t.Fatalf("Unpushed on a fresh clone = %d (%v), want 0", n, err)
	}

	write(t, r, ".bashrc", "changed\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("local only"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Unpushed(); err != nil || n != 1 {
		t.Fatalf("Unpushed after one local commit = %d (%v), want 1", n, err)
	}
}

// TestUnpushedReportsUnknownRatherThanZero matters because zero means "you are
// up to date" -- claiming that when there is no remote to compare against
// would be a false all-clear.
func TestUnpushedReportsUnknownRatherThanZero(t *testing.T) {
	r := newRepo(t) // no origin at all
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}
	n, err := r.Unpushed()
	if err != nil {
		t.Fatalf("Unpushed: %v", err)
	}
	if n != -1 {
		t.Fatalf("Unpushed with no remote = %d, want -1 (unknown)", n)
	}
}

func TestUnpushedOnAbsentRepoIsUnknown(t *testing.T) {
	n, err := New(filepath.Join(t.TempDir(), "absent"), t.TempDir()).Unpushed()
	if err != nil {
		t.Fatalf("Unpushed: %v", err)
	}
	if n != -1 {
		t.Fatalf("Unpushed on an absent repo = %d, want -1", n)
	}
}
