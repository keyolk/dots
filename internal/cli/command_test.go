package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// env is a fully isolated dots installation: its own work tree, its own bare
// stores, its own manifest and vault. Nothing here touches the developer's real
// $HOME, which matters because these tests run commands that commit and write.
type env struct {
	t        *testing.T
	root     string
	work     string
	manifest string
}

func newEnv(t *testing.T, manifestBody string) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	work := filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.repo", "secret.repo"} {
		dir := filepath.Join(root, name)
		if out, err := exec.Command("git", "init", "--bare", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", name, err, out)
		}
		for _, kv := range [][2]string{{"user.email", "t@example.com"}, {"user.name", "dots test"}} {
			cmd := exec.Command("git", "--git-dir="+dir, "--work-tree="+work, "config", kv[0], kv[1])
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git config: %v\n%s", err, out)
			}
		}
	}

	body := strings.NewReplacer(
		"{{root}}", root,
		"{{work}}", work,
	).Replace(manifestBody)

	mpath := filepath.Join(root, "dots.toml")
	if err := os.WriteFile(mpath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, root: root, work: work, manifest: mpath}
}

func (e *env) write(rel, body string) {
	e.t.Helper()
	p := filepath.Join(e.work, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// run executes one dots command through the real cobra tree and captures its
// output, so the tests exercise flag parsing and command wiring rather than
// calling internals directly.
func (e *env) run(args ...string) (string, error) {
	e.t.Helper()

	// Package-level flag state persists between cobra invocations; reset it so
	// one test cannot leak a --json or --dry-run into the next.
	flagManifest, flagJSON, flagDryRun = "", false, false

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--manifest", e.manifest}, args...))

	// Commands print results with fmt.Print rather than through cobra's writer,
	// so stdout is captured at the file-descriptor level.
	stdout, stderr := os.Stdout, os.Stderr
	rp, wp, err := os.Pipe()
	if err != nil {
		e.t.Fatal(err)
	}
	os.Stdout, os.Stderr = wp, wp

	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(rp)
		done <- b.String()
	}()

	execErr := root.Execute()

	wp.Close()
	os.Stdout, os.Stderr = stdout, stderr
	captured := <-done
	rp.Close()

	return captured + out.String(), execErr
}

const baseManifest = `
[store]
config    = "{{root}}/config.repo"
secret    = "{{root}}/secret.repo"
work_tree = "{{work}}"

[secrets]
identity = "{{root}}/identity.age"
vault    = "{{root}}/vault.age"

[[dotfiles]]
name    = "shell"
include = [".bashrc", ".config/fish/**/*.fish"]

[[dotfiles]]
name    = "creds"
secret  = true
include = [".config/hub"]
`

func TestStatusReportsAnUntrackedDeclaredFile(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "export A=1\n")

	out, err := e.run("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".bashrc") || !strings.Contains(out, "untracked") {
		t.Fatalf("status did not report the untracked file:\n%s", out)
	}
}

func TestStatusOnACleanMachineSaysSo(t *testing.T) {
	e := newEnv(t, baseManifest)
	out, err := e.run("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "clean") {
		t.Fatalf("status on an empty tree did not report clean:\n%s", out)
	}
}

func TestStatusJSONIsMachineReadable(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "x\n")

	out, err := e.run("status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("--json did not emit a JSON array:\n%s", out)
	}
	if !strings.Contains(out, `"Path"`) {
		t.Fatalf("--json output lacks the Path field:\n%s", out)
	}
}

func TestStatusOnlyFiltersToOneState(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "x\n")

	out, err := e.run("status", "--only", "modified")
	if err != nil {
		t.Fatalf("status --only: %v\n%s", err, out)
	}
	if strings.Contains(out, ".bashrc") {
		t.Fatalf("--only modified showed an untracked file:\n%s", out)
	}
}

func TestAddStagesADeclaredFile(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "export A=1\n")

	out, err := e.run("add")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".bashrc") {
		t.Fatalf("add did not report the staged file:\n%s", out)
	}
}

// TestAddRefusesAFileWithACredential is the guard that matters most: the config
// store is a remote repository, and a token reaching it is unrecoverable.
func TestAddRefusesAFileWithACredential(t *testing.T) {
	e := newEnv(t, baseManifest)
	// Assembled from parts so the literal never appears in this source file.
	token := "gh" + "p_" + strings.Repeat("A1b2C3d4", 5)
	e.write(".bashrc", "export TOKEN="+token+"\n")

	out, _ := e.run("add")
	if !strings.Contains(out, "refused") {
		t.Fatalf("add did not refuse a credential-bearing file:\n%s", out)
	}
	if strings.Contains(out, token) {
		t.Fatal("add printed the full credential while refusing it")
	}
}

func TestAddStillStagesCleanFilesAlongsideARefusedOne(t *testing.T) {
	// Refusing one file must not abandon the rest of the batch, or a single
	// bad file blocks every unrelated change indefinitely.
	e := newEnv(t, baseManifest)
	token := "gh" + "p_" + strings.Repeat("A1b2C3d4", 5)
	e.write(".bashrc", "export TOKEN="+token+"\n")
	e.write(".config/fish/ok.fish", "set -x A 1\n")

	out, _ := e.run("add")
	if !strings.Contains(out, "ok.fish") {
		t.Fatalf("a clean file was not staged alongside a refused one:\n%s", out)
	}
}

// TestSecretGroupFileSkipsTheCredentialScan is why the `secret = true` flag
// still exists after the two stores were merged: ~/.ssh and ~/.gnupg hold
// credentials by design, and scanning them would refuse exactly the files that
// most need tracking.
func TestSecretGroupFileSkipsTheCredentialScan(t *testing.T) {
	e := newEnv(t, baseManifest)
	token := "gh" + "p_" + strings.Repeat("Z9y8X7w6", 5)
	e.write(".config/hub", "oauth_token: "+token+"\n")

	out, err := e.run("add")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "refused") {
		t.Fatalf("a secret-group file was refused:\n%s", out)
	}
	if !strings.Contains(out, ".config/hub") {
		t.Fatalf("the file was not staged:\n%s", out)
	}
}

func TestSaveCommitsToTheStore(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "export A=1\n")

	out, err := e.run("save", "-M", "test commit")
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if !strings.Contains(out, "committed") {
		t.Fatalf("save did not report a commit:\n%s", out)
	}

	// The commit must be real, not just reported.
	log, err := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "log", "--oneline").Output()
	if err != nil || !strings.Contains(string(log), "test commit") {
		t.Fatalf("commit not found in the store: %v\n%s", err, log)
	}
}

func TestSaveThenStatusIsClean(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "export A=1\n")
	if _, err := e.run("save", "-M", "x"); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, err := e.run("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "clean") {
		t.Fatalf("status after save is not clean:\n%s", out)
	}
}

func TestSaveRecordsADeletion(t *testing.T) {
	// A file removed from disk must have its removal committed, or every other
	// machine keeps resurrecting it.
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "x\n")
	if _, err := e.run("save", "-M", "add"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.Remove(filepath.Join(e.work, ".bashrc")); err != nil {
		t.Fatal(err)
	}

	if _, err := e.run("save", "-M", "remove"); err != nil {
		t.Fatalf("save after delete: %v", err)
	}
	files, err := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "ls-files").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files), ".bashrc") {
		t.Fatal("a deleted file is still tracked after save")
	}
}

func TestSecretSetGetRoundTripsThroughTheCLI(t *testing.T) {
	e := newEnv(t, baseManifest)
	if out, err := e.run("secret", "keygen"); err != nil {
		t.Fatalf("secret keygen: %v\n%s", err, out)
	}
	if out, err := e.run("secret", "set", "github/token", "value-123"); err != nil {
		t.Fatalf("secret set: %v\n%s", err, out)
	}

	out, err := e.run("secret", "get", "github/token")
	if err != nil {
		t.Fatalf("secret get: %v\n%s", err, out)
	}
	if !strings.Contains(out, "value-123") {
		t.Fatalf("secret get returned %q", out)
	}
}

func TestSecretListShowsNamesNotValues(t *testing.T) {
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("secret", "set", "github/token", "the-actual-value"); err != nil {
		t.Fatal(err)
	}

	out, err := e.run("secret", "list")
	if err != nil {
		t.Fatalf("secret list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "github/token") {
		t.Fatalf("secret list omitted the name:\n%s", out)
	}
	if strings.Contains(out, "the-actual-value") {
		t.Fatal("secret list printed a value")
	}
}

func TestSecretKeygenRefusesToReplaceAnIdentity(t *testing.T) {
	// Overwriting the identity makes every existing vault permanently
	// unreadable, so the second call must fail rather than succeed quietly.
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("secret", "keygen"); err == nil {
		t.Fatal("secret keygen overwrote an existing identity")
	}
}

func TestApplyRendersATemplateWithItsSecret(t *testing.T) {
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name     = "templated"
template = true
include  = [".ccproxy/**/*.tmpl"]
`)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("secret", "set", "tok", "rendered-secret"); err != nil {
		t.Fatal(err)
	}
	e.write(".ccproxy/config.json.tmpl", `{"token":"{{ secret "tok" }}"}`)

	out, err := e.run("apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	body, err := os.ReadFile(filepath.Join(e.work, ".ccproxy/config.json"))
	if err != nil {
		t.Fatalf("rendered file missing: %v", err)
	}
	if !strings.Contains(string(body), "rendered-secret") {
		t.Fatalf("rendered %q, want the substituted secret", body)
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name     = "templated"
template = true
include  = [".ccproxy/**/*.tmpl"]
`)
	e.write(".ccproxy/plain.conf.tmpl", "os = {{ .OS }}\n")

	if _, err := e.run("apply", "-n"); err != nil {
		t.Fatalf("apply -n: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.work, ".ccproxy/plain.conf")); !os.IsNotExist(err) {
		t.Fatal("apply --dry-run wrote a file")
	}
}

func TestApplyFailsWhenASecretIsMissing(t *testing.T) {
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name     = "templated"
template = true
include  = [".ccproxy/**/*.tmpl"]
`)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	e.write(".ccproxy/config.json.tmpl", `{"token":"{{ secret "absent" }}"}`)

	if _, err := e.run("apply"); err == nil {
		t.Fatal("apply succeeded with a missing secret")
	}
}

func TestDoctorReportsStoreAndDotfileState(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "x\n")

	out, _ := e.run("doctor")
	for _, want := range []string{"manifest", "store", "dotfiles", "packages", "path"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor omitted the %s check:\n%s", want, out)
		}
	}
}

func TestDoctorFlagsAWorldReadableIdentity(t *testing.T) {
	// A private key readable by other users is a real finding, and the check
	// is worth having precisely because chmod mistakes are silent.
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(e.root, "identity.age"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.run("doctor")
	if err == nil {
		t.Fatal("doctor passed with a world-readable identity")
	}
	if !strings.Contains(out, "chmod") {
		t.Fatalf("doctor did not suggest the fix:\n%s", out)
	}
}

func TestUnrootedPatternFailsWithAnActionableMessage(t *testing.T) {
	e := newEnv(t, `
[store]
config    = "{{root}}/config.repo"
work_tree = "{{work}}"

[[dotfiles]]
name    = "bad"
include = ["**/*.tmpl"]
`)
	out, err := e.run("status")
	if err == nil {
		t.Fatal("an unrooted pattern was accepted")
	}
	combined := out + err.Error()
	if !strings.Contains(combined, "unrooted") {
		t.Fatalf("message does not name the problem: %s", combined)
	}
}

func TestMissingManifestSuggestsInit(t *testing.T) {
	flagManifest, flagJSON, flagDryRun = "", false, false
	root := newRootCmd()
	root.SetArgs([]string{"--manifest", filepath.Join(t.TempDir(), "absent.toml"), "status"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("a missing manifest produced no error")
	}
	if !strings.Contains(err.Error(), "init") {
		t.Fatalf("error %q does not point at `dots init`", err)
	}
}

func TestPkgDiffRunsWithNoDeclaredSources(t *testing.T) {
	e := newEnv(t, baseManifest)
	out, err := e.run("pkg", "diff")
	if err != nil {
		t.Fatalf("pkg diff: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no package sources") {
		t.Fatalf("pkg diff with no sources said:\n%s", out)
	}
}

func TestPkgSyncDryRunPrintsCommandsWithoutRunningThem(t *testing.T) {
	e := newEnv(t, baseManifest+`
[packages.sh]
list     = "printf 'installed\n'"
install  = "false %s"
packages = ["missing-package"]
`)
	out, err := e.run("pkg", "sync", "-n")
	if err != nil {
		t.Fatalf("pkg sync -n: %v\n%s", err, out)
	}
	if !strings.Contains(out, "missing-package") {
		t.Fatalf("dry run did not name the package it would install:\n%s", out)
	}
	if !strings.Contains(out, "would run") {
		t.Fatalf("dry run did not mark the command as unexecuted:\n%s", out)
	}
}

func TestInitWritesAUsableManifest(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dots.toml")

	flagManifest, flagJSON, flagDryRun = "", false, false
	root := newRootCmd()
	root.SetArgs([]string{"--manifest", target, "init"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// The generated manifest must itself load, or `dots init` hands the user a
	// broken starting point.
	flagManifest = ""
	root = newRootCmd()
	root.SetArgs([]string{"--manifest", target, "doctor"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	_ = root.Execute() // warnings are expected; a parse failure is not

	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[store]", "[secrets]", "[[dotfiles]]", "[packages.brew]"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("generated manifest lacks %s", want)
		}
	}
}

func TestInitRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dots.toml")
	if err := os.WriteFile(target, []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	flagManifest, flagJSON, flagDryRun = "", false, false
	root := newRootCmd()
	root.SetArgs([]string{"--manifest", target, "init"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("init overwrote an existing manifest without --force")
	}
	body, _ := os.ReadFile(target)
	if !strings.Contains(string(body), "# existing") {
		t.Fatal("the existing manifest was clobbered")
	}
}

func TestSecretRmRemovesAStoredSecret(t *testing.T) {
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("secret", "set", "doomed", "value"); err != nil {
		t.Fatal(err)
	}

	if out, err := e.run("secret", "rm", "doomed"); err != nil {
		t.Fatalf("secret rm: %v\n%s", err, out)
	}
	out, err := e.run("secret", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "doomed") {
		t.Fatalf("the secret survived removal:\n%s", out)
	}
}

// TestSecretRmOfAnAbsentKeyFails guards against a silent success that would let
// a typo look like a completed deletion.
func TestSecretRmOfAnAbsentKeyFails(t *testing.T) {
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("secret", "rm", "never-existed"); err == nil {
		t.Fatal("secret rm of an absent key succeeded")
	}
}

func TestSecretSetReadsAMultiLineValueFromStdin(t *testing.T) {
	// A PEM key cannot be passed as an argument; --stdin is the only way in,
	// and it must preserve every interior newline.
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}

	const pem = "-----BEGIN PRIVATE KEY-----\nline1\nline2\n-----END PRIVATE KEY-----"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString(pem + "\n")
		w.Close()
	}()
	saved := os.Stdin
	os.Stdin = r
	_, setErr := e.run("secret", "set", "tls/key", "--stdin")
	os.Stdin = saved
	r.Close()
	if setErr != nil {
		t.Fatalf("secret set --stdin: %v", setErr)
	}

	out, err := e.run("secret", "get", "tls/key")
	if err != nil {
		t.Fatalf("secret get: %v", err)
	}
	if !strings.Contains(out, "line1\nline2") {
		t.Fatalf("multi-line value came back as %q", out)
	}
}

func TestSecretGetOfAnAbsentKeyFails(t *testing.T) {
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("secret", "get", "absent"); err == nil {
		t.Fatal("secret get of an absent key succeeded")
	}
}

func TestSecretListOnAnEmptyVaultSaysSo(t *testing.T) {
	e := newEnv(t, baseManifest)
	if _, err := e.run("secret", "keygen"); err != nil {
		t.Fatal(err)
	}
	out, err := e.run("secret", "list")
	if err != nil {
		t.Fatalf("secret list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "empty") {
		t.Fatalf("secret list on an empty vault said:\n%s", out)
	}
}

func TestPkgBinReportsAMissingDeclaredBinary(t *testing.T) {
	e := newEnv(t, baseManifest+`
[packages.brew]
packages = []

[[packages.brew.binaries]]
name    = "absent-tool"
dir     = "{{root}}/emptybin"
from    = "https://example.com/tool"
install = "curl -fsSL https://example.com/install.sh | sh"
`)
	if err := os.MkdirAll(filepath.Join(e.root, "emptybin"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := e.run("pkg", "bin", "--dir", filepath.Join(e.root, "emptybin"))
	if err == nil {
		t.Fatalf("pkg bin passed with a missing declared binary:\n%s", out)
	}
	if !strings.Contains(out, "absent-tool") {
		t.Fatalf("pkg bin did not name the missing binary:\n%s", out)
	}
	// The reinstall command is the whole reason for declaring it.
	if !strings.Contains(out, "install.sh") {
		t.Fatalf("pkg bin did not print the reinstall command:\n%s", out)
	}
}

func TestPkgBinFindsAnUndeclaredBinary(t *testing.T) {
	e := newEnv(t, baseManifest)
	dir := filepath.Join(e.root, "somebin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mystery"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := e.run("pkg", "bin", "--dir", dir)
	if err != nil {
		t.Fatalf("pkg bin: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mystery") {
		t.Fatalf("pkg bin did not report the undeclared binary:\n%s", out)
	}
}

func TestPkgAdoptEmitsPastableTOML(t *testing.T) {
	e := newEnv(t, baseManifest+`
[packages.sh]
list     = "printf 'installed-thing\n'"
packages = []
`)
	out, err := e.run("pkg", "adopt")
	if err != nil {
		t.Fatalf("pkg adopt: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[packages.sh]") || !strings.Contains(out, `"installed-thing"`) {
		t.Fatalf("pkg adopt did not emit usable TOML:\n%s", out)
	}
}

func TestApplyWithNoTemplatesSaysSo(t *testing.T) {
	e := newEnv(t, baseManifest)
	out, err := e.run("apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no templates") {
		t.Fatalf("apply with no templates said:\n%s", out)
	}
}

func TestAddWithAnExplicitPathAddsOnlyThatFile(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.write(".bashrc", "x\n")
	e.write(".config/fish/other.fish", "y\n")

	out, err := e.run("add", ".bashrc")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".bashrc") {
		t.Fatalf("the named file was not added:\n%s", out)
	}
	if strings.Contains(out, "other.fish") {
		t.Fatalf("an unnamed file was added too:\n%s", out)
	}
}

func TestAddWithNothingToDoSaysSo(t *testing.T) {
	e := newEnv(t, baseManifest)
	out, err := e.run("add")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to add") {
		t.Fatalf("add with nothing pending said:\n%s", out)
	}
}

func TestSaveWithNothingToDoSaysSo(t *testing.T) {
	e := newEnv(t, baseManifest)
	out, err := e.run("save")
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to save") {
		t.Fatalf("save with nothing pending said:\n%s", out)
	}
}

// track commits a path directly through git, bypassing the manifest, to set up
// the "tracked but no longer declared" state prune exists to clean.
func (e *env) track(rel, body string) {
	e.t.Helper()
	e.write(rel, body)
	dir := filepath.Join(e.root, "config.repo")
	for _, args := range [][]string{
		{"--git-dir=" + dir, "--work-tree=" + e.work, "add", "--", rel},
		{"--git-dir=" + dir, "--work-tree=" + e.work, "commit", "-q", "-m", "seed"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			e.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestPruneUntracksAnUndeclaredPath(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.track(".spin/.watchman-cookie-1", "junk\n")

	out, err := e.run("prune", "-y", "--commit")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}

	files, err := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "ls-files").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files), "watchman-cookie") {
		t.Fatalf("the undeclared path is still tracked:\n%s", files)
	}
}

// TestPruneNeverDeletesFromDisk is the property that makes prune safe to run:
// untracking ~/.gnupg/random_seed must not remove the file gpg depends on.
func TestPruneNeverDeletesFromDisk(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.track(".gnupg/random_seed", "entropy\n")
	onDisk := filepath.Join(e.work, ".gnupg/random_seed")

	if _, err := e.run("prune", "-y", "--commit"); err != nil {
		t.Fatalf("prune: %v", err)
	}

	body, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("prune deleted the file from disk: %v", err)
	}
	if string(body) != "entropy\n" {
		t.Fatalf("prune modified the file: %q", body)
	}
}

func TestPruneLeavesDeclaredPathsAlone(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.track(".bashrc", "export A=1\n") // declared by the shell group
	e.track(".spin/junk", "x\n")       // declared by nothing

	if _, err := e.run("prune", "-y", "--commit"); err != nil {
		t.Fatalf("prune: %v", err)
	}

	files, err := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "ls-files").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files), ".bashrc") {
		t.Fatal("prune untracked a declared path")
	}
	if strings.Contains(string(files), ".spin/junk") {
		t.Fatal("prune left an undeclared path tracked")
	}
}

func TestPruneDryRunChangesNothing(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.track(".spin/junk", "x\n")

	out, err := e.run("prune", "-n")
	if err != nil {
		t.Fatalf("prune -n: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would untrack") {
		t.Fatalf("dry run did not say what it would do:\n%s", out)
	}

	files, _ := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "ls-files").Output()
	if !strings.Contains(string(files), ".spin/junk") {
		t.Fatal("prune --dry-run untracked a path")
	}
}

// TestPruneRefusesWithoutConfirmationWhenNotATerminal keeps a scripted or
// piped invocation from untracking thousands of paths unattended.
func TestPruneRefusesWithoutConfirmationWhenNotATerminal(t *testing.T) {
	e := newEnv(t, baseManifest)
	e.track(".spin/junk", "x\n")

	out, err := e.run("prune")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "aborted") {
		t.Fatalf("prune proceeded without a confirmation:\n%s", out)
	}
	files, _ := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "ls-files").Output()
	if !strings.Contains(string(files), ".spin/junk") {
		t.Fatal("prune untracked a path without confirmation")
	}
}

func TestPruneWithNothingToDoSaysSo(t *testing.T) {
	e := newEnv(t, baseManifest)
	out, err := e.run("prune")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to prune") {
		t.Fatalf("prune with a clean store said:\n%s", out)
	}
}

func TestChunkSplitsLargeBatches(t *testing.T) {
	// git rm takes paths as arguments; 2000+ undeclared paths in one call
	// would overflow ARG_MAX.
	xs := make([]string, 1100)
	for i := range xs {
		xs[i] = "p"
	}
	got := chunk(xs, 500)
	if len(got) != 3 || len(got[0]) != 500 || len(got[2]) != 100 {
		t.Fatalf("chunk produced %d batches of %v", len(got), lens(got))
	}
	if len(chunk(nil, 500)) != 0 {
		t.Fatal("chunk of an empty slice produced batches")
	}
}

func lens(xss [][]string) []int {
	out := make([]int, len(xss))
	for i, xs := range xss {
		out[i] = len(xs)
	}
	return out
}

// TestPruneLeavesInactiveGroupPathsAlone is the cross-machine safety property:
// running prune on a Mac must not untrack the Linux-only config that a
// linux-constrained group declares.
func TestPruneLeavesInactiveGroupPathsAlone(t *testing.T) {
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name    = "linux-desktop"
os      = ["plan9"]
include = [".xprofile"]
`)
	e.track(".xprofile", "export GTK_IM_MODULE=kime\n")
	e.track(".spin/junk", "x\n")

	if _, err := e.run("prune", "-y", "--commit"); err != nil {
		t.Fatalf("prune: %v", err)
	}

	files, _ := exec.Command("git",
		"--git-dir="+filepath.Join(e.root, "config.repo"),
		"--work-tree="+e.work, "ls-files").Output()
	if !strings.Contains(string(files), ".xprofile") {
		t.Fatal("prune untracked a path declared by an inactive group")
	}
	if strings.Contains(string(files), ".spin/junk") {
		t.Fatal("prune left a genuinely undeclared path tracked")
	}
}

func TestStatusOnlyInactiveFiltersToThatState(t *testing.T) {
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name    = "linux-desktop"
os      = ["plan9"]
include = [".xprofile"]
`)
	e.track(".xprofile", "x\n")

	out, err := e.run("status", "--only", "inactive")
	if err != nil {
		t.Fatalf("status --only inactive: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".xprofile") {
		t.Fatalf("--only inactive did not show the path:\n%s", out)
	}
}

// TestInitChecksOutTheStore guards a bootstrap failure that looks like success:
// cloning without checking out leaves the vault in the repository but not on
// disk, so `apply` fails on a machine that did fetch everything it needed.
func TestInitChecksOutTheStore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()

	origin := filepath.Join(root, "origin")
	for _, f := range []string{".bashrc", ".config/dots/vault.age"} {
		p := filepath.Join(origin, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e.com"},
		{"config", "user.name", "t"}, {"add", "-A"}, {"commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	flagManifest, flagJSON, flagDryRun = "", false, false
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"--manifest", filepath.Join(root, "dots.toml"), "init",
		"--clone-config", origin,
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	for _, want := range []string{".bashrc", ".config/dots/vault.age"} {
		if _, err := os.Stat(filepath.Join(home, want)); err != nil {
			t.Fatalf("%s was not checked out: %v", want, err)
		}
	}
}

func TestRedactURLHidesAnEmbeddedToken(t *testing.T) {
	// Bootstrapping a private store means putting a token in the clone URL;
	// echoing it back would write the credential into the terminal and into
	// whatever captures that output.
	token := "gh" + "p_" + strings.Repeat("A1b2C3d4", 5)
	got := redactURL("https://user:" + token + "@github.com/you/config.git")
	if strings.Contains(got, token) {
		t.Fatalf("redactURL leaked the token: %s", got)
	}
	if !strings.Contains(got, "github.com/you/config.git") {
		t.Fatalf("redactURL mangled the URL: %s", got)
	}

	// A URL with no credential must survive untouched.
	plain := "https://github.com/you/config.git"
	if got := redactURL(plain); got != plain {
		t.Fatalf("redactURL = %q, want the URL unchanged", got)
	}
	// A local path is not a URL at all.
	if got := redactURL("/home/me/.config.repo"); got != "/home/me/.config.repo" {
		t.Fatalf("redactURL mangled a path: %s", got)
	}
}

// TestPkgBinRespectsManifestExcludes covers the other half of the same noise
// problem: a path a group deliberately excludes -- a vendored shim, an editor
// backup, a compiled binary listed under packages -- is a decision already
// made, and re-reporting it asks the same question on every run.
func TestPkgBinRespectsManifestExcludes(t *testing.T) {
	// The group declares only .sh files, so "mystery" is genuinely unclaimed
	// while the two excluded names are decisions already recorded.
	e := newEnv(t, baseManifest+`
[[dotfiles]]
name    = "scripts"
include = [".local/bin/*.sh"]
exclude = [".local/bin/* (vendored)", ".local/bin/*.bak.*"]
`)
	dir := filepath.Join(e.work, ".local/bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh (vendored)", "old.bak.20260101", "mystery"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	out, err := e.run("pkg", "bin", "--dir", dir)
	if err != nil {
		t.Fatalf("pkg bin: %v\n%s", err, out)
	}
	for _, excluded := range []string{"(vendored)", ".bak."} {
		if strings.Contains(out, excluded) {
			t.Fatalf("an excluded path was reported:\n%s", out)
		}
	}
	if !strings.Contains(out, "mystery") {
		t.Fatalf("the genuinely undeclared file was not reported:\n%s", out)
	}
}
