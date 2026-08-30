package dotfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/dotx/internal/manifest"
)

// fixture builds a self-contained work tree with a real bare repo over it, so
// the tests exercise the same git plumbing the command does rather than a mock.
type fixture struct {
	t        *testing.T
	work     string
	configTo string
	m        *manifest.Manifest
}

func newFixture(t *testing.T, groups ...manifest.Group) *fixture {
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

	run(t, "", "git", "init", "--bare", "-q", gitDir)
	// Identity must be set explicitly: a machine whose global git config lacks
	// user.email cannot commit, and the test would fail for an unrelated reason.
	run(t, work, "git", "--git-dir="+gitDir, "--work-tree="+work, "config", "user.email", "test@example.com")
	run(t, work, "git", "--git-dir="+gitDir, "--work-tree="+work, "config", "user.name", "dotx test")

	return &fixture{
		t:        t,
		work:     work,
		configTo: gitDir,
		m: &manifest.Manifest{
			Store: manifest.Store{
				Config:   gitDir,
				Secret:   filepath.Join(root, "secret.repo"), // deliberately absent
				WorkTree: work,
			},
			Dotfiles: groups,
		},
	}
}

func (f *fixture) write(rel, body string) {
	f.t.Helper()
	p := filepath.Join(f.work, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(paths ...string) {
	f.t.Helper()
	args := append([]string{"--git-dir=" + f.configTo, "--work-tree=" + f.work, "add", "--"}, paths...)
	run(f.t, f.work, "git", args...)
	run(f.t, f.work, "git", "--git-dir="+f.configTo, "--work-tree="+f.work, "commit", "-q", "-m", "test")
}

func (f *fixture) scan() map[string]Entry {
	f.t.Helper()
	entries, err := NewScanner(f.m, "testhost").Scan()
	if err != nil {
		f.t.Fatalf("Scan: %v", err)
	}
	out := make(map[string]Entry, len(entries))
	for _, e := range entries {
		out[e.Path] = e
	}
	return out
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// TestDeclaredButUncommittedFileIsUntracked is the central regression: the old
// bare-repo setup with status.showUntrackedFiles=no reported nothing here, which
// is how 67 skills and 16 agents stayed uncommitted indefinitely.
func TestDeclaredButUncommittedFileIsUntracked(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "claude",
		Include: []string{".claude/skills/**/*"},
	})
	f.write(".claude/skills/new-skill/SKILL.md", "# new")

	got := f.scan()
	e, ok := got[".claude/skills/new-skill/SKILL.md"]
	if !ok {
		t.Fatal("a declared file on disk was not reported at all")
	}
	if e.State != Untracked {
		t.Fatalf("state = %v, want untracked", e.State)
	}
	if e.Group != "claude" {
		t.Fatalf("group = %q, want the group that claimed it", e.Group)
	}
}

func TestCommittedUnchangedFileIsClean(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	f.write(".bashrc", "export A=1\n")
	f.commit(".bashrc")

	if got := f.scan()[".bashrc"].State; got != Clean {
		t.Fatalf("state = %v, want clean", got)
	}
}

func TestEditedCommittedFileIsModified(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	f.write(".bashrc", "export A=1\n")
	f.commit(".bashrc")
	f.write(".bashrc", "export A=2\n")

	if got := f.scan()[".bashrc"].State; got != Modified {
		t.Fatalf("state = %v, want modified", got)
	}
}

func TestTrackedFileDeletedFromDiskIsMissing(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	f.write(".bashrc", "x\n")
	f.commit(".bashrc")
	if err := os.Remove(filepath.Join(f.work, ".bashrc")); err != nil {
		t.Fatal(err)
	}

	// A deleted file no longer matches its glob, so it surfaces through the
	// tracked-but-absent path rather than the declared one.
	if got := f.scan()[".bashrc"].State; got != Missing {
		t.Fatalf("state = %v, want missing", got)
	}
}

// TestTrackedFileNoGroupClaimsIsUndeclared covers the .spin/ case: 2110 tracked
// watchman cookies that no sane manifest would declare, which the old setup had
// no way to even name.
func TestTrackedFileNoGroupClaimsIsUndeclared(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	f.write(".spin/.watchman-cookie-1", "junk")
	f.commit(".spin/.watchman-cookie-1")

	e, ok := f.scan()[".spin/.watchman-cookie-1"]
	if !ok {
		t.Fatal("a tracked file outside every group was not reported")
	}
	if e.State != Undeclared {
		t.Fatalf("state = %v, want undeclared", e.State)
	}
}

func TestExcludePrunesFromInclude(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "shell",
		Include: []string{".config/fish/*.fish"},
		Exclude: []string{".config/fish/*.bak.fish"},
	})
	f.write(".config/fish/config.fish", "set -x A 1")
	f.write(".config/fish/config.bak.fish", "old")

	got := f.scan()
	if _, ok := got[".config/fish/config.fish"]; !ok {
		t.Fatal("included file missing from scan")
	}
	if _, ok := got[".config/fish/config.bak.fish"]; ok {
		t.Fatal("excluded file was still declared")
	}
}

func TestDoublestarMatchesNestedSubtree(t *testing.T) {
	// `.claude/hooks/**/*.py` must reach an arbitrarily nested hook. A plain
	// `*` glob would find only the top level, which is precisely how a subset
	// of hooks ended up tracked and the rest did not.
	f := newFixture(t, manifest.Group{
		Name:    "claude",
		Include: []string{".claude/hooks/**/*.py"},
	})
	f.write(".claude/hooks/top.py", "x")
	f.write(".claude/hooks/nested/deep/inner.py", "x")
	f.write(".claude/hooks/nested/notes.md", "x")

	got := f.scan()
	for _, want := range []string{".claude/hooks/top.py", ".claude/hooks/nested/deep/inner.py"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("%s not matched by the recursive glob", want)
		}
	}
	if _, ok := got[".claude/hooks/nested/notes.md"]; ok {
		t.Fatal("a non-.py file matched a *.py glob")
	}
}

func TestFirstGroupWinsAPath(t *testing.T) {
	// Ordering is how an exception is expressed: a narrow secret group listed
	// before a broad config group must keep the file out of the config store.
	f := newFixture(t,
		manifest.Group{Name: "credentials", Secret: true, Include: []string{".config/hub"}},
		manifest.Group{Name: "everything", Include: []string{".config/**/*"}},
	)
	f.write(".config/hub", "oauth_token: x")

	e := f.scan()[".config/hub"]
	if e.Group != "credentials" {
		t.Fatalf("group = %q, want the first group to claim it", e.Group)
	}
	if e.Store != "secret" {
		t.Fatalf("store = %q, want secret", e.Store)
	}
}

func TestGroupSkippedWhenOSDoesNotMatch(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "linux-only",
		OS:      []string{"plan9"},
		Include: []string{".xprofile"},
	})
	f.write(".xprofile", "x")

	if _, ok := f.scan()[".xprofile"]; ok {
		t.Fatal("a group constrained to another OS still claimed a file")
	}
}

func TestGroupSkippedWhenHostDoesNotMatch(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "work-laptop",
		Host:    []string{"some-other-host"},
		Include: []string{".bashrc"},
	})
	f.write(".bashrc", "x")

	if _, ok := f.scan()[".bashrc"]; ok {
		t.Fatal("a group constrained to another host still claimed a file")
	}
}

func TestTemplateFileIsFlagged(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:     "templated",
		Template: true,
		Include:  []string{".ccproxy/**/*.tmpl"},
	})
	f.write(".ccproxy/config.json.tmpl", `{"token": "{{ secret \"t\" }}"}`)

	e := f.scan()[".ccproxy/config.json.tmpl"]
	if !e.Template {
		t.Fatal("a .tmpl path was not flagged as a template")
	}
}

func TestMissingSecretStoreIsNotFatal(t *testing.T) {
	// The fixture points the secret store at a directory that does not exist.
	// A machine that has only cloned the config repo must still get a status.
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	f.write(".bashrc", "x")

	if _, err := NewScanner(f.m, "testhost").Scan(); err != nil {
		t.Fatalf("Scan failed with an absent secret store: %v", err)
	}
}

func TestWalkOrphansFindsUndeclaredFilesOnDisk(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "scripts",
		Include: []string{".local/bin/*.sh"},
	})
	f.write(".local/bin/known.sh", "#!/bin/sh")
	f.write(".local/bin/mystery-binary", "ELF")

	orphans, err := NewScanner(f.m, "testhost").WalkOrphans([]string{".local/bin"})
	if err != nil {
		t.Fatalf("WalkOrphans: %v", err)
	}
	found := false
	for _, o := range orphans {
		if o == ".local/bin/mystery-binary" {
			found = true
		}
		if o == ".local/bin/known.sh" {
			t.Fatal("a declared file was reported as an orphan")
		}
	}
	if !found {
		t.Fatal("an undeclared file on disk was not reported as an orphan")
	}
}

// TestUnrootedPatternIsRejected guards a performance cliff that presents as a
// hang: a bare "**/*.tmpl" walks the entire home directory, which on a real
// machine is over a million files under Library, src, .cargo and .rustup.
func TestUnrootedPatternIsRejected(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "bad", Include: []string{"**/*.tmpl"}})
	_, err := NewScanner(f.m, "testhost").Scan()
	if err == nil {
		t.Fatal("an unrooted pattern was accepted")
	}
	if !strings.Contains(err.Error(), "unrooted") {
		t.Fatalf("error %q does not explain the problem", err)
	}
}

// TestLiteralTopLevelPathNeedsNoWalk covers the same cliff from the other side:
// ".bashrc" sits directly in the work tree, and resolving it must not be
// mistaken for a pattern that has to walk everything.
func TestLiteralTopLevelPathNeedsNoWalk(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	f.write(".bashrc", "x")

	got := f.scan()
	if _, ok := got[".bashrc"]; !ok {
		t.Fatal("a literal top-level path was not resolved")
	}
}

func TestNoisyDirectoriesArePruned(t *testing.T) {
	// node_modules and friends are large, generated, and owned by a tool that
	// can recreate them. Walking them is pure cost.
	f := newFixture(t, manifest.Group{Name: "scripts", Include: []string{".local/**/*.js"}})
	f.write(".local/keep.js", "x")
	f.write(".local/node_modules/pkg/index.js", "x")

	got := f.scan()
	if _, ok := got[".local/keep.js"]; !ok {
		t.Fatal("a real file was pruned")
	}
	if _, ok := got[".local/node_modules/pkg/index.js"]; ok {
		t.Fatal("node_modules was not pruned")
	}
}

// TestSubmoduleIsNotReportedAsUndeclared guards a path to real data loss: a
// gitlink is a commit pointer, so no file glob can ever match it, and treating
// it as undeclared would make `dotx prune` detach vim-plug, tpm and fisherman
// from the store.
func TestSubmoduleIsNotReportedAsUndeclared(t *testing.T) {
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})

	// Build a tiny repo and add it as a gitlink.
	sub := filepath.Join(f.t.TempDir(), "plugin")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, sub, "git", "init", "-q", "-b", "main")
	run(t, sub, "git", "config", "user.email", "t@example.com")
	run(t, sub, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(sub, "plugin.vim"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, sub, "git", "add", "-A")
	run(t, sub, "git", "commit", "-q", "-m", "init")

	run(t, f.work, "git", "--git-dir="+f.configTo, "--work-tree="+f.work,
		"-c", "protocol.file.allow=always",
		"submodule", "add", "-q", sub, ".vim/autoload/vim-plug")
	run(t, f.work, "git", "--git-dir="+f.configTo, "--work-tree="+f.work,
		"commit", "-q", "-m", "add submodule")

	e, ok := f.scan()[".vim/autoload/vim-plug"]
	if !ok {
		t.Fatal("the submodule was not reported at all")
	}
	if e.State != Submodule {
		t.Fatalf("state = %v, want submodule (undeclared would make it prunable)", e.State)
	}
}

// TestPathDeclaredByAnInactiveGroupIsNotUndeclared covers the cross-machine
// case: .xprofile belongs to a linux-only group, and on a Mac it must not look
// like a leftover — pruning here would delete the other machine's config.
func TestPathDeclaredByAnInactiveGroupIsNotUndeclared(t *testing.T) {
	f := newFixture(t,
		manifest.Group{Name: "shell", Include: []string{".bashrc"}},
		manifest.Group{Name: "linux-desktop", OS: []string{"plan9"}, Include: []string{".xprofile"}},
	)
	f.write(".xprofile", "export GTK_IM_MODULE=kime\n")
	f.commit(".xprofile")

	e, ok := f.scan()[".xprofile"]
	if !ok {
		t.Fatal("the path was not reported at all")
	}
	if e.State != Inactive {
		t.Fatalf("state = %v, want inactive", e.State)
	}
	if e.Group != "linux-desktop" {
		t.Fatalf("group = %q, want the inactive group that claims it", e.Group)
	}
}

func TestInactiveGroupDoesNotClaimUnrelatedPaths(t *testing.T) {
	f := newFixture(t,
		manifest.Group{Name: "linux-desktop", OS: []string{"plan9"}, Include: []string{".xprofile"}},
	)
	f.write(".spin/junk", "x\n")
	f.commit(".spin/junk")

	if got := f.scan()[".spin/junk"].State; got != Undeclared {
		t.Fatalf("state = %v, want undeclared for a genuinely unclaimed path", got)
	}
}

// TestSymlinkIsTrackable matters because dotfile trees rely on symlinks:
// ~/.vim/autoload/plug.vim points into the vim-plug submodule, and excluding
// symlinks would report a tracked, intentional file as undeclared.
func TestSymlinkIsTrackable(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "editor",
		Include: []string{".vim/autoload/*.vim"},
	})
	target := filepath.Join(f.work, ".vim/autoload/real.vim")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("\" real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.vim", filepath.Join(f.work, ".vim/autoload/plug.vim")); err != nil {
		t.Fatal(err)
	}

	if _, ok := f.scan()[".vim/autoload/plug.vim"]; !ok {
		t.Fatal("a symlink matching a declared glob was not reported")
	}
}

func TestSymlinkAtTheTopLevelIsTrackable(t *testing.T) {
	// The literal-path branch resolves without a walk, so it needs the same
	// symlink handling as the glob branch.
	f := newFixture(t, manifest.Group{Name: "shell", Include: []string{".bashrc"}})
	target := filepath.Join(f.work, "real-bashrc")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-bashrc", filepath.Join(f.work, ".bashrc")); err != nil {
		t.Fatal(err)
	}

	if _, ok := f.scan()[".bashrc"]; !ok {
		t.Fatal("a top-level symlink was not reported")
	}
}

func TestSocketsAreNotTrackable(t *testing.T) {
	// ~/.gnupg holds gpg-agent sockets next to key material; git cannot store
	// them and reporting them would be noise on every status.
	if trackable(os.ModeSocket) || trackable(os.ModeDevice) || trackable(os.ModeNamedPipe) {
		t.Fatal("a non-regular, non-symlink mode was reported as trackable")
	}
	if !trackable(0) || !trackable(os.ModeSymlink) {
		t.Fatal("a regular file or symlink was reported as untrackable")
	}
}

// TestCompiledBinaryIsReportedAsArtifact guards the failure that made the
// config store unpushable: ~/.local/bin holds shell scripts and compiled tools
// side by side, and a glob over it swept in 509MB of Mach-O binaries — one of
// them 127MB, past GitHub's hard file-size limit.
func TestCompiledBinaryIsReportedAsArtifact(t *testing.T) {
	f := newFixture(t, manifest.Group{
		Name:    "scripts",
		Include: []string{".local/bin/**/*"},
	})
	f.write(".local/bin/script.sh", "#!/bin/sh\necho hi\n")

	// Mach-O 64-bit little-endian magic, which is what `go build` emits here.
	macho := append([]byte{0xcf, 0xfa, 0xed, 0xfe}, make([]byte, 64)...)
	p := filepath.Join(f.work, ".local/bin/compiled")
	if err := os.WriteFile(p, macho, 0o755); err != nil {
		t.Fatal(err)
	}

	got := f.scan()
	if e := got[".local/bin/compiled"]; e.State != Artifact {
		t.Fatalf("compiled binary state = %v, want artifact", e.State)
	}
	if e := got[".local/bin/script.sh"]; e.State != Untracked {
		t.Fatalf("shell script state = %v, want untracked", e.State)
	}
}

func TestElfBinaryIsAlsoAnArtifact(t *testing.T) {
	// The same store is checked out on Linux machines.
	f := newFixture(t, manifest.Group{Name: "scripts", Include: []string{".local/bin/*"}})
	elf := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 64)...)
	if err := os.MkdirAll(filepath.Join(f.work, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.work, ".local/bin/elfbin"), elf, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := f.scan()[".local/bin/elfbin"].State; got != Artifact {
		t.Fatalf("ELF binary state = %v, want artifact", got)
	}
}

func TestOversizedTextFileIsAnArtifact(t *testing.T) {
	// A 1MB+ file is a generated blob whatever its magic number — the tracked
	// ts_lint/errors.json was 1.0MB of machine output.
	f := newFixture(t, manifest.Group{Name: "claude", Include: []string{".claude/**/*.json"}})
	big := make([]byte, (1<<20)+1)
	for i := range big {
		big[i] = 'x'
	}
	p := filepath.Join(f.work, ".claude/errors.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.scan()[".claude/errors.json"].State; got != Artifact {
		t.Fatalf("oversized file state = %v, want artifact", got)
	}
}

func TestSmallScriptIsNotAnArtifact(t *testing.T) {
	// The 77 shell scripts in ~/.local/bin are exactly what should be tracked;
	// a check that swept them up would be worse than none.
	f := newFixture(t, manifest.Group{Name: "scripts", Include: []string{".local/bin/*"}})
	if err := os.MkdirAll(filepath.Join(f.work, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"sh.sh":    "#!/bin/sh\necho hi\n",
		"py.py":    "#!/usr/bin/env python3\nprint('hi')\n",
		"noext":    "#!/usr/bin/env bash\nexit 0\n",
		"data.txt": "MZ is not at offset 0 here\n",
	} {
		if err := os.WriteFile(filepath.Join(f.work, ".local/bin", name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := f.scan()
	for name := range map[string]bool{"sh.sh": true, "py.py": true, "noext": true, "data.txt": true} {
		if e := got[".local/bin/"+name]; e.State == Artifact {
			t.Fatalf("%s was misclassified as an artifact", name)
		}
	}
}

func TestAlreadyTrackedBinaryStaysReported(t *testing.T) {
	// Something already committed must not silently vanish from status —
	// removing it should be a deliberate prune, not a side effect.
	f := newFixture(t, manifest.Group{Name: "scripts", Include: []string{".local/bin/*"}})
	macho := append([]byte{0xcf, 0xfa, 0xed, 0xfe}, make([]byte, 64)...)
	if err := os.MkdirAll(filepath.Join(f.work, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.work, ".local/bin/tool"), macho, 0o755); err != nil {
		t.Fatal(err)
	}
	f.commit(".local/bin/tool")

	if got := f.scan()[".local/bin/tool"].State; got != Clean {
		t.Fatalf("tracked binary state = %v, want clean", got)
	}
}

func TestIsCompiledBinaryRecognisesEachFormat(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"elf":       {0x7f, 'E', 'L', 'F'},
		"macho64le": {0xcf, 0xfa, 0xed, 0xfe},
		"macho32le": {0xce, 0xfa, 0xed, 0xfe},
		"machobe":   {0xfe, 0xed, 0xfa, 0xcf},
		"universal": {0xca, 0xfe, 0xba, 0xbe},
	}
	for name, magic := range cases {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, append(magic, make([]byte, 32)...), 0o755); err != nil {
			t.Fatal(err)
		}
		if !isCompiledBinary(p) {
			t.Fatalf("%s was not recognised as a binary", name)
		}
	}

	text := filepath.Join(dir, "script")
	if err := os.WriteFile(text, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isCompiledBinary(text) {
		t.Fatal("a shell script was recognised as a binary")
	}

	// "MZ" is two printable characters, so a text file starting with them must
	// not be read as a Windows executable.
	mz := filepath.Join(dir, "mz.txt")
	if err := os.WriteFile(mz, []byte("MZ is just a prefix here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isCompiledBinary(mz) {
		t.Fatal("a text file starting with MZ was recognised as a binary")
	}

	// A file too short to hold a magic number must not panic.
	tiny := filepath.Join(dir, "tiny")
	if err := os.WriteFile(tiny, []byte{0x7f}, 0o644); err != nil {
		t.Fatal(err)
	}
	if isCompiledBinary(tiny) {
		t.Fatal("a 1-byte file was recognised as a binary")
	}
	if isCompiledBinary(filepath.Join(dir, "absent")) {
		t.Fatal("a missing file was recognised as a binary")
	}
}
