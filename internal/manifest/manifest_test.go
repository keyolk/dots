package manifest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dots.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadExpandsTilde(t *testing.T) {
	// Paths are written with ~ because a manifest is shared across machines
	// whose home directories differ.
	p := writeManifest(t, `
[store]
config = "~/.config.repo"
[secrets]
identity = "~/.config/dots/identity.age"
`)
	m, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if m.Store.Config != filepath.Join(home, ".config.repo") {
		t.Fatalf("Store.Config = %q, want the expanded path", m.Store.Config)
	}
	if m.Secrets.Identity != filepath.Join(home, ".config", "dots", "identity.age") {
		t.Fatalf("Secrets.Identity = %q, want the expanded path", m.Secrets.Identity)
	}
}

func TestLoadDefaultsWorkTreeToHome(t *testing.T) {
	m, err := Load(writeManifest(t, "[store]\nconfig = \"/tmp/x\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if m.Store.WorkTree != home {
		t.Fatalf("WorkTree = %q, want %q", m.Store.WorkTree, home)
	}
}

func TestLoadRejectsUnnamedGroup(t *testing.T) {
	// A group with no name produces unattributable status output, so it is
	// caught at load rather than surfacing as blank labels later.
	_, err := Load(writeManifest(t, "[[dotfiles]]\ninclude = [\".bashrc\"]\n"))
	if err == nil {
		t.Fatal("a group with no name was accepted")
	}
}

func TestLoadReportsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Fatal("Load of a missing manifest returned no error")
	}
}

func TestLoadParsesGroupsAndPackages(t *testing.T) {
	m, err := Load(writeManifest(t, `
[store]
config = "/tmp/c"

[[dotfiles]]
name    = "shell"
include = [".bashrc"]
exclude = ["*.bak"]

[[dotfiles]]
name   = "creds"
secret = true

[packages.brew]
packages = ["age", "jq"]

[[packages.brew.binaries]]
name    = "coder"
install = "curl example.com | sh"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Dotfiles) != 2 {
		t.Fatalf("got %d groups, want 2", len(m.Dotfiles))
	}
	if !m.Dotfiles[1].Secret {
		t.Fatal("secret flag was not parsed")
	}
	brew := m.Packages["brew"]
	if brew == nil || len(brew.Packages) != 2 {
		t.Fatalf("brew packages = %v", brew)
	}
	if len(brew.Binaries) != 1 || brew.Binaries[0].Install == "" {
		t.Fatal("binary declaration lost its install command")
	}
}

func TestAppliesWithNoConstraints(t *testing.T) {
	// An unconstrained group is the common case and must apply everywhere.
	if !(Group{Name: "x"}).Applies("any-host") {
		t.Fatal("an unconstrained group did not apply")
	}
}

func TestAppliesRespectsOSConstraint(t *testing.T) {
	if (Group{OS: []string{"plan9"}}).Applies("h") {
		t.Fatal("a group for another OS applied")
	}
	if !(Group{OS: []string{runtime.GOOS}}).Applies("h") {
		t.Fatal("a group for this OS did not apply")
	}
}

func TestAppliesRespectsHostConstraint(t *testing.T) {
	if (Group{Host: []string{"other"}}).Applies("mine") {
		t.Fatal("a group for another host applied")
	}
	if !(Group{Host: []string{"mine"}}).Applies("mine") {
		t.Fatal("a group for this host did not apply")
	}
}

func TestResolvedPicksOnlyThisOSList(t *testing.T) {
	p := PkgList{
		Packages: []string{"common"},
		Darwin:   []string{"mac"},
		Linux:    []string{"linux"},
	}
	got := p.Resolved()
	if len(got) != 2 {
		t.Fatalf("Resolved = %v, want common plus exactly one OS list", got)
	}
	want := "linux"
	if runtime.GOOS == "darwin" {
		want = "mac"
	}
	if got[1] != want {
		t.Fatalf("Resolved = %v, want %q for %s", got, want, runtime.GOOS)
	}
}

func TestDefaultPathHonoursEnvOverride(t *testing.T) {
	// The override is how a test or a second profile points at another
	// manifest without touching the real one.
	t.Setenv("DOTS_MANIFEST", "/custom/dots.toml")
	if got := DefaultPath(); got != "/custom/dots.toml" {
		t.Fatalf("DefaultPath = %q", got)
	}
}

func TestDefaultPathFallsBackToXDGStyleLocation(t *testing.T) {
	t.Setenv("DOTS_MANIFEST", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "dots", "dots.toml")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
}

func TestBinaryAppliesRespectsOSConstraint(t *testing.T) {
	// A binary declared for Linux must not be reported missing on macOS, or
	// every cross-platform manifest fails `pkg bin` on half its machines.
	if (Binary{Name: "x", OS: []string{"plan9"}}).Applies() {
		t.Fatal("a binary for another OS applied")
	}
	if !(Binary{Name: "x", OS: []string{runtime.GOOS}}).Applies() {
		t.Fatal("a binary for this OS did not apply")
	}
	if !(Binary{Name: "x"}).Applies() {
		t.Fatal("an unconstrained binary did not apply")
	}
}

func TestLoadRejectsMalformedTOML(t *testing.T) {
	if _, err := Load(writeManifest(t, "[store\nconfig = broken")); err == nil {
		t.Fatal("malformed TOML was accepted")
	}
}

func TestLoadParsesOSAndHostConstraints(t *testing.T) {
	m, err := Load(writeManifest(t, `
[[dotfiles]]
name = "linux-only"
os   = ["linux"]
host = ["work-laptop"]
include = [".xprofile"]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := m.Dotfiles[0]
	if len(g.OS) != 1 || g.OS[0] != "linux" {
		t.Fatalf("OS = %v", g.OS)
	}
	if len(g.Host) != 1 || g.Host[0] != "work-laptop" {
		t.Fatalf("Host = %v", g.Host)
	}
}

func TestLoadParsesTemplateFlag(t *testing.T) {
	m, err := Load(writeManifest(t, `
[[dotfiles]]
name     = "templated"
template = true
include  = [".config/**/*.tmpl"]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.Dotfiles[0].Template {
		t.Fatal("the template flag was not parsed")
	}
}

func TestLoadWithAnEmptyPathUsesTheDefault(t *testing.T) {
	// Passing "" is how the CLI says "no --manifest flag given".
	t.Setenv("DOTS_MANIFEST", filepath.Join(t.TempDir(), "absent.toml"))
	if _, err := Load(""); err == nil {
		t.Fatal("Load(\"\") did not consult the default path")
	}
}

func TestResolvedOnAnEmptyListIsEmpty(t *testing.T) {
	if got := (&PkgList{}).Resolved(); len(got) != 0 {
		t.Fatalf("Resolved = %v, want empty", got)
	}
}
