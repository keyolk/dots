package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo builds a real bare repo over a real work tree. The whole point of
// this package is the bare-repo-over-$HOME arrangement, which a mock would not
// exercise: the failure modes live in how git itself behaves when its git-dir
// is outside its work tree.
func newRepo(t *testing.T) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	work := filepath.Join(root, "home")
	gitDir := filepath.Join(root, "config.repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init", "--bare", "-q", gitDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	r := New(gitDir, work)
	// A machine whose global git config lacks an identity cannot commit, and
	// the test would then fail for a reason unrelated to what it checks.
	if _, err := r.Run("config", "user.email", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run("config", "user.name", "dots test"); err != nil {
		t.Fatal(err)
	}
	return r
}

func write(t *testing.T, r *Repo, rel, body string) {
	t.Helper()
	p := filepath.Join(r.WorkTree, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExistsDistinguishesPresentFromAbsent(t *testing.T) {
	r := newRepo(t)
	if !r.Exists() {
		t.Fatal("an initialised repo reported as absent")
	}
	if New(filepath.Join(t.TempDir(), "nope"), t.TempDir()).Exists() {
		t.Fatal("a missing repo reported as present")
	}
	// An empty git-dir path is how an unconfigured secret store arrives; it
	// must be reported absent rather than resolving to the process CWD.
	if New("", t.TempDir()).Exists() {
		t.Fatal("an empty git-dir reported as present")
	}
}

func TestLsFilesOnAbsentRepoIsEmptyNotAnError(t *testing.T) {
	// A machine that cloned only the config store must still get a status,
	// so the missing secret store has to degrade quietly.
	files, err := New(filepath.Join(t.TempDir(), "absent"), t.TempDir()).LsFiles()
	if err != nil {
		t.Fatalf("LsFiles on an absent repo: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("LsFiles = %v, want empty", files)
	}
}

func TestModifiedOnAbsentRepoIsEmptyNotAnError(t *testing.T) {
	mod, err := New(filepath.Join(t.TempDir(), "absent"), t.TempDir()).Modified()
	if err != nil {
		t.Fatalf("Modified on an absent repo: %v", err)
	}
	if len(mod) != 0 {
		t.Fatalf("Modified = %v, want empty", mod)
	}
}

func TestAddThenLsFilesReportsThePath(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "export A=1\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	files, err := r.LsFiles()
	if err != nil {
		t.Fatalf("LsFiles: %v", err)
	}
	if len(files) != 1 || files[0] != ".bashrc" {
		t.Fatalf("LsFiles = %v, want [.bashrc]", files)
	}
}

func TestAddWithNoPathsIsANoOp(t *testing.T) {
	// `dots add` with nothing to stage must not invoke `git add --`, which
	// would fail rather than doing nothing.
	if err := newRepo(t).Add(); err != nil {
		t.Fatalf("Add() with no paths: %v", err)
	}
}

func TestCommitReportsWhetherAnythingWasRecorded(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}

	ok, err := r.Commit("first")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !ok {
		t.Fatal("Commit reported nothing recorded despite a staged file")
	}

	// An empty commit is a no-op, not a failure: `dots save` runs across both
	// stores and only one of them may have changes.
	ok, err = r.Commit("second")
	if err != nil {
		t.Fatalf("Commit with nothing staged: %v", err)
	}
	if ok {
		t.Fatal("Commit reported a commit with nothing staged")
	}
}

func TestModifiedSeesAnUnstagedEdit(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "one\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}

	write(t, r, ".bashrc", "two\n")
	mod, err := r.Modified()
	if err != nil {
		t.Fatalf("Modified: %v", err)
	}
	if len(mod) != 1 || mod[0] != ".bashrc" {
		t.Fatalf("Modified = %v, want [.bashrc]", mod)
	}
}

func TestModifiedSeesAStagedEdit(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "one\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}

	write(t, r, ".bashrc", "two\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	mod, err := r.Modified()
	if err != nil {
		t.Fatalf("Modified: %v", err)
	}
	if len(mod) == 0 {
		t.Fatal("a staged edit was not reported as modified")
	}
}

func TestModifiedIsEmptyForACleanTree(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}

	mod, err := r.Modified()
	if err != nil {
		t.Fatalf("Modified: %v", err)
	}
	if len(mod) != 0 {
		t.Fatalf("Modified = %v, want empty for a clean tree", mod)
	}
}

// TestModifiedBeforeFirstCommitDoesNotFail covers a freshly initialised store:
// there is no HEAD to diff against, and treating that as an error would break
// the very first `dots status` on a new machine.
func TestModifiedBeforeFirstCommitDoesNotFail(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Modified(); err != nil {
		t.Fatalf("Modified with no HEAD: %v", err)
	}
}

func TestRemoveForgetsThePathButKeepsTheFile(t *testing.T) {
	// Untracking must never delete the user's actual config file.
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}

	if err := r.Remove(".bashrc"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	files, _ := r.LsFiles()
	if len(files) != 0 {
		t.Fatalf("LsFiles = %v, want the path forgotten", files)
	}
	if _, err := os.Stat(filepath.Join(r.WorkTree, ".bashrc")); err != nil {
		t.Fatal("Remove deleted the file from disk")
	}
}

func TestRemoveWithNoPathsIsANoOp(t *testing.T) {
	if err := newRepo(t).Remove(); err != nil {
		t.Fatalf("Remove() with no paths: %v", err)
	}
}

func TestRunSurfacesGitStderr(t *testing.T) {
	// A bare "exit status 128" is useless when diagnosing a store; the message
	// git printed is the part worth keeping.
	r := newRepo(t)
	_, err := r.Run("cat-file", "-p", "definitely-not-an-object")
	if err == nil {
		t.Fatal("a failing git command returned no error")
	}
	if !strings.Contains(err.Error(), "cat-file") {
		t.Fatalf("error %q does not name the command", err)
	}
}

func TestLsFilesHandlesPathsWithSpaces(t *testing.T) {
	// ~/.local/bin on this machine holds "bash (kiro-cli-term)". Without -z
	// and NUL splitting, git would quote such a path and the name would come
	// back mangled.
	r := newRepo(t)
	const name = ".local/bin/bash (kiro-cli-term)"
	write(t, r, name, "#!/bin/sh\n")
	if err := r.Add(name); err != nil {
		t.Fatalf("Add: %v", err)
	}

	files, err := r.LsFiles()
	if err != nil {
		t.Fatalf("LsFiles: %v", err)
	}
	if len(files) != 1 || files[0] != name {
		t.Fatalf("LsFiles = %q, want %q", files, name)
	}
}

func TestLsFilesHandlesNonASCIIPaths(t *testing.T) {
	r := newRepo(t)
	const name = ".config/한글/설정.conf"
	write(t, r, name, "x\n")
	if err := r.Add(name); err != nil {
		t.Fatalf("Add: %v", err)
	}
	files, err := r.LsFiles()
	if err != nil {
		t.Fatalf("LsFiles: %v", err)
	}
	if len(files) != 1 || files[0] != name {
		t.Fatalf("LsFiles = %q, want %q", files, name)
	}
}

func TestModifiedHandlesPathsWithSpaces(t *testing.T) {
	r := newRepo(t)
	const name = ".local/bin/with space"
	write(t, r, name, "one\n")
	if err := r.Add(name); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}
	write(t, r, name, "two\n")

	mod, err := r.Modified()
	if err != nil {
		t.Fatalf("Modified: %v", err)
	}
	if len(mod) != 1 || mod[0] != name {
		t.Fatalf("Modified = %q, want %q", mod, name)
	}
}

func TestSplitZDropsEmptyEntries(t *testing.T) {
	// git -z output ends with a trailing NUL, which a naive split turns into a
	// phantom empty path that then gets staged.
	got := splitZ("a\x00b\x00")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitZ = %q, want [a b]", got)
	}
	if len(splitZ("")) != 0 {
		t.Fatal("splitZ of an empty string produced entries")
	}
}

func TestRunUsesTheWorkTreeNotTheProcessCWD(t *testing.T) {
	// The command's own working directory is arbitrary; the repo must resolve
	// paths against its declared work tree regardless.
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")

	saved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(saved) //nolint:errcheck // restoring CWD; failure is untestable here
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	if err := r.Add(".bashrc"); err != nil {
		t.Fatalf("Add from an unrelated CWD: %v", err)
	}
	files, _ := r.LsFiles()
	if len(files) != 1 {
		t.Fatalf("LsFiles = %v, want the path staged", files)
	}
}

func TestSubmodulesReportsGitlinksOnly(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("init"); err != nil {
		t.Fatal(err)
	}

	// A real gitlink needs a real repository to point at.
	sub := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = sub
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = sub
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if _, err := r.Run("-c", "protocol.file.allow=always",
		"submodule", "add", "-q", sub, ".tmux/plugins/tpm"); err != nil {
		t.Skipf("submodule add unavailable: %v", err)
	}

	subs, err := r.Submodules()
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	if len(subs) != 1 || subs[0] != ".tmux/plugins/tpm" {
		t.Fatalf("Submodules = %v, want just the gitlink", subs)
	}
	// A plain file must never be mistaken for one.
	for _, s := range subs {
		if s == ".bashrc" {
			t.Fatal("a regular file was reported as a submodule")
		}
	}
}

func TestSubmodulesIsEmptyWithoutAny(t *testing.T) {
	r := newRepo(t)
	write(t, r, ".bashrc", "x\n")
	if err := r.Add(".bashrc"); err != nil {
		t.Fatal(err)
	}
	subs, err := r.Submodules()
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("Submodules = %v, want empty", subs)
	}
}

func TestSubmodulesOnAbsentRepoIsEmptyNotAnError(t *testing.T) {
	subs, err := New(filepath.Join(t.TempDir(), "absent"), t.TempDir()).Submodules()
	if err != nil {
		t.Fatalf("Submodules on an absent repo: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("Submodules = %v, want empty", subs)
	}
}
