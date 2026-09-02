// Package git wraps the bare-repo-over-$HOME pattern the existing config and
// secret repos already use, so dots builds on that layout instead of migrating
// it. Adopting dots must not invalidate the shell aliases still in muscle
// memory, and the two repos keep their history untouched.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Repo is one bare repository whose work tree is a directory it does not live
// inside — the dotfiles idiom.
type Repo struct {
	GitDir   string
	WorkTree string
}

// New builds a Repo handle. It does not touch the filesystem.
func New(gitDir, workTree string) *Repo {
	return &Repo{GitDir: gitDir, WorkTree: workTree}
}

// Exists reports whether the repository is present.
func (r *Repo) Exists() bool {
	if r.GitDir == "" {
		return false
	}
	st, err := os.Stat(r.GitDir)
	return err == nil && st.IsDir()
}

// Run executes a git command against this repo and returns stdout.
func (r *Repo) Run(args ...string) (string, error) {
	full := append([]string{
		"--git-dir=" + r.GitDir,
		"--work-tree=" + r.WorkTree,
	}, args...)

	cmd := exec.Command("git", full...)
	cmd.Dir = r.WorkTree
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// RunInteractive streams git's output to the terminal, for commands whose
// output is meant for the user (log, diff with a pager, push progress).
func (r *Repo) RunInteractive(args ...string) error {
	full := append([]string{
		"--git-dir=" + r.GitDir,
		"--work-tree=" + r.WorkTree,
	}, args...)

	cmd := exec.Command("git", full...)
	cmd.Dir = r.WorkTree
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// LsFiles returns every tracked path, relative to the work tree.
func (r *Repo) LsFiles() ([]string, error) {
	if !r.Exists() {
		return nil, nil
	}
	out, err := r.Run("ls-files", "-z")
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// Submodules returns the paths git tracks as gitlinks (mode 160000).
//
// A submodule is a commit pointer, not a file, so no file glob can ever match
// it and a manifest cannot declare it the way it declares everything else.
// Treating them as a distinct set keeps them from being reported as undeclared
// and pruned away — which would silently detach vim-plug, tpm and fisherman
// from the store.
func (r *Repo) Submodules() ([]string, error) {
	if !r.Exists() {
		return nil, nil
	}
	out, err := r.Run("ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	var subs []string
	for _, entry := range splitZ(out) {
		// Format: "<mode> <sha> <stage>\t<path>".
		if !strings.HasPrefix(entry, "160000 ") {
			continue
		}
		if _, path, ok := strings.Cut(entry, "\t"); ok {
			subs = append(subs, path)
		}
	}
	return subs, nil
}

// Modified returns tracked paths whose working copy differs from HEAD.
func (r *Repo) Modified() ([]string, error) {
	if !r.Exists() {
		return nil, nil
	}
	// diff-files covers unstaged edits; diff-index --cached covers staged ones.
	// Using status --porcelain instead would also surface untracked files,
	// which this repo is configured to hide and which dots resolves from the
	// manifest rather than from git.
	unstaged, err := r.Run("diff-files", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	staged, err := r.Run("diff-index", "--cached", "--name-only", "-z", "HEAD")
	if err != nil {
		// A repo with no commits yet has no HEAD; its whole index is new.
		staged = ""
	}
	return append(splitZ(unstaged), splitZ(staged)...), nil
}

// Ignored returns the subset of paths that a .gitignore excludes.
//
// A manifest that declares a path and a .gitignore that excludes it are in
// direct conflict, and git resolves it by refusing the whole `git add` — so
// one stale ignore rule blocks every unrelated file in the same batch. Naming
// the conflict is the only way to make it fixable.
func (r *Repo) Ignored(paths []string) ([]string, error) {
	if !r.Exists() || len(paths) == 0 {
		return nil, nil
	}
	// check-ignore only accepts -z together with --stdin, so paths go in on
	// stdin NUL-separated and come back the same way. Passing them as argv
	// would also break on a path list long enough to exceed ARG_MAX.
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(p)
		in.WriteByte(0)
	}

	cmd := exec.Command("git",
		"--git-dir="+r.GitDir, "--work-tree="+r.WorkTree,
		"check-ignore", "--no-index", "-z", "--stdin")
	cmd.Dir = r.WorkTree
	cmd.Stdin = &in
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Exit status 1 means "nothing matched", which is the common case and not
	// an error; anything else is.
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, fmt.Errorf("git check-ignore: %s", msg)
		}
	}
	return splitZ(stdout.String()), nil
}

// Show returns the bytes of an object, e.g. "HEAD:.kube/config". Used to
// compare a working copy against what the store holds without writing the
// stored version to disk.
func (r *Repo) Show(spec string) ([]byte, error) {
	if !r.Exists() {
		return nil, fmt.Errorf("store does not exist")
	}
	out, err := r.Run("show", spec)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Unpushed counts commits on the current branch that the remote does not have.
// It returns -1 when there is nothing to compare against -- no remote, or a
// remote-tracking ref that has never been fetched -- so a caller can tell
// "nothing to push" apart from "cannot tell".
func (r *Repo) Unpushed() (int, error) {
	if !r.Exists() {
		return -1, nil
	}
	branch, err := r.Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return -1, nil //nolint:nilerr // a repo with no commits has no branch
	}
	branch = strings.TrimSpace(branch)

	// Compare against the fetched remote ref rather than running git fetch:
	// doctor should not reach the network, and a stale count is still a
	// better signal than none.
	out, err := r.Run("rev-list", "--count", "origin/"+branch+".."+branch)
	if err != nil {
		return -1, nil //nolint:nilerr // no origin, or never fetched
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1, nil //nolint:nilerr
	}
	return n, nil
}

// Add stages paths.
func (r *Repo) Add(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	_, err := r.Run(args...)
	return err
}

// Remove unstages and forgets paths without deleting them from disk.
func (r *Repo) Remove(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"rm", "--cached", "--quiet", "--"}, paths...)
	_, err := r.Run(args...)
	return err
}

// Commit records staged changes. It reports whether anything was committed:
// an empty commit is a no-op, not a failure.
func (r *Repo) Commit(message string) (bool, error) {
	staged, err := r.Run("diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(staged) == "" {
		return false, nil
	}
	if _, err := r.Run("commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// Clone creates the bare repo and checks it out over the work tree.
func Clone(url, gitDir, workTree string) (*Repo, error) {
	cmd := exec.Command("git", "clone", "--bare", url, gitDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("clone %s: %w", url, err)
	}
	r := New(gitDir, workTree)
	// Untracked files stay hidden: dots resolves "should this be tracked?" from
	// the manifest, and a bare repo over $HOME that reports untracked files
	// reports the entire home directory.
	if _, err := r.Run("config", "status.showUntrackedFiles", "no"); err != nil {
		return nil, err
	}
	// `git clone --bare` writes no fetch refspec, so origin/<branch> never
	// appears and nothing can tell whether a commit has been pushed. Without
	// this the store looks healthy while every other machine is behind.
	if _, err := r.Run("config", "remote.origin.fetch",
		"+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return nil, err
	}
	// The refspec only governs future fetches; the branches this clone already
	// pulled down were written before it existed. One fetch populates
	// refs/remotes/origin/ so the comparison works from the first command.
	if _, err := r.Run("fetch", "origin"); err != nil {
		return nil, err
	}
	return r, nil
}

// Checkout applies HEAD over the work tree, reporting paths that would be
// overwritten rather than clobbering them.
func (r *Repo) Checkout() (conflicts []string, err error) {
	if _, err := r.Run("checkout"); err == nil {
		return nil, nil
	}
	// git names the conflicting paths in its error; re-read them from status
	// instead of parsing stderr, which is not a stable format.
	out, serr := r.Run("status", "--porcelain", "-z")
	if serr != nil {
		return nil, serr
	}
	for _, line := range splitZ(out) {
		if len(line) > 3 {
			conflicts = append(conflicts, strings.TrimSpace(line[2:]))
		}
	}
	return conflicts, fmt.Errorf("checkout would overwrite %d local file(s)", len(conflicts))
}

func splitZ(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
