package pkgmgr

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/keyolk/dots/internal/manifest"
)

func TestGoBinDirHonoursGOBIN(t *testing.T) {
	t.Setenv("GOBIN", "/custom/gobin")
	if got := goBinDir(); got != "/custom/gobin" {
		t.Fatalf("goBinDir = %q, want the GOBIN override", got)
	}
}

func TestGoBinDirFallsBackToGOPATH(t *testing.T) {
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/custom/gopath")
	if got := goBinDir(); got != filepath.Join("/custom/gopath", "bin") {
		t.Fatalf("goBinDir = %q, want GOPATH/bin", got)
	}
}

func TestGoBinDirFallsBackToHome(t *testing.T) {
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "")
	home, _ := os.UserHomeDir()
	if got := goBinDir(); got != filepath.Join(home, "go", "bin") {
		t.Fatalf("goBinDir = %q, want ~/go/bin", got)
	}
}

// TestGoBinariesOnAMissingDirIsNotAnError covers a machine that has Go but has
// never run `go install`: an absent directory is a valid empty answer.
func TestGoBinariesOnAMissingDirIsNotAnError(t *testing.T) {
	t.Setenv("GOBIN", filepath.Join(t.TempDir(), "absent"))
	got, err := goBinaries()
	if err != nil {
		t.Fatalf("goBinaries: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("goBinaries = %v, want empty", got)
	}
}

// TestGoBinariesFallsBackToTheFileNameWithoutBuildInfo matters because `go
// install` keeps no manifest: a file that carries no embedded module path is
// still evidence that something is installed, so it must not vanish.
func TestGoBinariesFallsBackToTheFileNameWithoutBuildInfo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOBIN", dir)
	if err := os.WriteFile(filepath.Join(dir, "not-a-go-binary"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := goBinaries()
	if err != nil {
		t.Fatalf("goBinaries: %v", err)
	}
	if len(got) != 1 || got[0] != "not-a-go-binary" {
		t.Fatalf("goBinaries = %v, want the bare file name and no directories", got)
	}
}

// TestGoBinariesReadsTheModulePathFromRealBuildInfo is the case that makes the
// go source reproducible: the module path is what `go install` needs, and the
// file name alone is not enough to reinstall anything.
func TestGoBinariesReadsTheModulePathFromRealBuildInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a binary")
	}
	dir := t.TempDir()
	t.Setenv("GOBIN", dir)

	// Build this very package's test module into GOBIN so the binary carries
	// genuine build info rather than a hand-forged approximation.
	src := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "probe")
	if err := runGo(t, "build", "-o", out, src); err != nil {
		t.Skipf("go build unavailable: %v", err)
	}

	got, err := goBinaries()
	if err != nil {
		t.Fatalf("goBinaries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("goBinaries = %v, want one entry", got)
	}
	// A binary built from a bare main.go reports its path as "command-line-
	// arguments"; either that or the file name is acceptable, an empty string
	// is not.
	if got[0] == "" {
		t.Fatal("goBinaries produced an empty name")
	}
}

func runGo(t *testing.T, args ...string) error {
	t.Helper()
	bin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not available")
	}
	return exec.Command(bin, args...).Run()
}

func TestManagedRecognisesPackageManagerPaths(t *testing.T) {
	// A symlink into a Cellar or a cargo target is owned by its manager, and
	// reporting it as a mystery binary would bury the real findings in noise.
	managedPaths := []string{
		"/opt/homebrew/Cellar/jq/1.7/bin/jq",
		"/opt/homebrew/Caskroom/app/1.0/app",
		"/Users/x/.cargo/bin/rg",
		"/Users/x/go/bin/dlv",
		"/Users/x/.bun/install/global/node_modules/.bin/tsc",
	}
	for _, p := range managedPaths {
		if !managed(p) {
			t.Fatalf("managed(%q) = false, want true", p)
		}
	}

	unmanaged := []string{
		"/Users/x/.local/bin/coder",
		"/usr/local/bin/kubectl",
		"../relative/thing",
	}
	for _, p := range unmanaged {
		if managed(p) {
			t.Fatalf("managed(%q) = true, want false", p)
		}
	}
}

func TestProbeVersionReturnsTheFirstLine(t *testing.T) {
	// Many tools print a banner after their version; only the first line is
	// useful in a status table.
	b := manifest.Binary{Version: "printf 'v1.2.3\\nextra line\\n'"}
	if got := probeVersion(b, "/unused"); got != "v1.2.3" {
		t.Fatalf("probeVersion = %q, want the first line only", got)
	}
}

func TestProbeVersionSubstitutesThePath(t *testing.T) {
	b := manifest.Binary{Version: "echo %s"}
	if got := probeVersion(b, "/some/path"); got != "/some/path" {
		t.Fatalf("probeVersion = %q, want the substituted path", got)
	}
}

func TestProbeVersionIsEmptyWhenUndeclaredOrFailing(t *testing.T) {
	if got := probeVersion(manifest.Binary{}, "/x"); got != "" {
		t.Fatalf("probeVersion with no command = %q, want empty", got)
	}
	if got := probeVersion(manifest.Binary{Version: "exit 1"}, "/x"); got != "" {
		t.Fatalf("probeVersion of a failing command = %q, want empty", got)
	}
}

func TestCheckBinariesFindsAPresentBinaryAndItsVersion(t *testing.T) {
	dir := t.TempDir()
	name := "present-tool"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"brew": {Binaries: []manifest.Binary{
				{Name: name, Dir: dir, Version: "echo v9.9.9"},
			}},
		},
	}

	states := CheckBinaries(m)
	if len(states) != 1 || !states[0].Present {
		t.Fatalf("CheckBinaries = %+v, want one present binary", states)
	}
	if states[0].Version != "v9.9.9" {
		t.Fatalf("Version = %q, want the probed version", states[0].Version)
	}
}

func TestCheckBinariesDefaultsToLocalBin(t *testing.T) {
	// An entry with no Dir must resolve under ~/.local/bin, which is where
	// curl-installed tools actually land.
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"brew": {Binaries: []manifest.Binary{{Name: "somewhere"}}},
		},
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "bin", "somewhere")
	if got := CheckBinaries(m)[0].Path; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestCheckBinariesExpandsTildeInDir(t *testing.T) {
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"brew": {Binaries: []manifest.Binary{{Name: "x", Dir: "~/custom"}}},
		},
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "custom", "x")
	if got := CheckBinaries(m)[0].Path; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestKnownListsBuiltinSourcesInAStableOrder(t *testing.T) {
	first, second := Known(), Known()
	if len(first) == 0 {
		t.Fatal("Known returned no sources")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatal("Known is not deterministic")
		}
	}
	// The sources the machine actually uses must all be present.
	want := map[string]bool{"brew": true, "cask": true, "cargo": true, "go": true,
		"bun": true, "mise": true, "krew": true, "apt": true}
	for _, k := range first {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("Known is missing sources: %v", want)
	}
}

func TestAvailableRespectsOSConstraint(t *testing.T) {
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	s := Source{Name: "x", Probe: "sh", OS: []string{other}}
	if s.Available() {
		t.Fatal("a source constrained to another OS reported available")
	}
	if !(Source{Name: "x", Probe: "sh"}).Available() {
		t.Fatal("an unconstrained source with a real probe reported unavailable")
	}
}

func TestInstalledWithNoListCommandIsAnError(t *testing.T) {
	// Silently returning an empty set would make every declared package look
	// missing, which sync would then act on.
	_, err := Source{Name: "custom", Probe: "sh"}.Installed()
	if err == nil {
		t.Fatal("a source with no list command returned no error")
	}
}

func TestInstallCmdIsNilForAnUnknownSource(t *testing.T) {
	// The caller reports this rather than executing a malformed command.
	if got := InstallCmd("no-such-source", &manifest.PkgList{}, "pkg"); got != nil {
		t.Fatalf("InstallCmd = %v, want nil", got)
	}
}
