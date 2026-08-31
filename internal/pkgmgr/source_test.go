package pkgmgr

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/keyolk/dots/internal/manifest"
)

// TestParseNormalisesEachSourceOutput uses the real output shapes of each tool.
// These formats differ enough that a single naive line-split would produce
// garbage package names, and the resulting diff would claim everything is
// missing - which `pkg sync` would then act on.
func TestParseNormalisesEachSourceOutput(t *testing.T) {
	cases := []struct {
		source string
		out    string
		want   []string
	}{
		{
			source: "brew",
			out:    "abseil\nage\nawscli\n",
			want:   []string{"abseil", "age", "awscli"},
		},
		{
			// cargo prints a "crate vX.Y.Z:" header then indents its binaries.
			source: "cargo",
			out:    "ripgrep v14.1.0:\n    rg\nstarship v1.20.1:\n    starship\n",
			want:   []string{"ripgrep", "starship"},
		},
		{
			// bun draws a tree and pins versions with @.
			source: "bun",
			out:    "/Users/x/.bun/install/global node_modules\n├── typescript@5.4.0\n└── @anthropic-ai/claude-code@1.2.3\n",
			want:   []string{"@anthropic-ai/claude-code", "typescript"},
		},
		{
			// mise columns are tool, version, source, requested.
			source: "mise",
			out:    "go         1.25.1   ~/.mise.toml   1.25\nnode       22.8.0   ~/.config/mise/config.toml  22\n",
			want:   []string{"go", "node"},
		},
		{
			source: "krew",
			out:    "PLUGIN  VERSION\nneat    v0.0.4\n",
			want:   []string{"neat"},
		},
	}

	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			got := builtins[c.source].parse(c.out)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parse = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseSkipsBlankLines(t *testing.T) {
	if got := builtins["brew"].parse("age\n\n\njq\n"); !reflect.DeepEqual(got, []string{"age", "jq"}) {
		t.Fatalf("parse = %v", got)
	}
}

// TestScopedBunPackageKeepsItsScope guards the @-stripping: cutting at the
// first @ instead of the last would turn @anthropic-ai/claude-code into an
// empty string.
func TestScopedBunPackageKeepsItsScope(t *testing.T) {
	got := builtins["bun"].parse("├── @scope/name@1.0.0\n")
	if len(got) != 1 || got[0] != "@scope/name" {
		t.Fatalf("parse = %v, want the scope preserved", got)
	}
}

func TestResolvedAddsOSSpecificPackages(t *testing.T) {
	p := &manifest.PkgList{
		Packages: []string{"common"},
		Darwin:   []string{"mac-only"},
		Linux:    []string{"linux-only"},
	}
	got := p.Resolved()
	if len(got) != 2 || got[0] != "common" {
		t.Fatalf("Resolved = %v, want common plus exactly one OS list", got)
	}
}

func TestReconcileSplitsMissingFromExtra(t *testing.T) {
	// The source is named "sh" so its probe resolves to a real executable,
	// while the list command returns a fixed set. That makes the whole
	// reconciliation deterministic without depending on brew being installed.
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"sh": {
				List:     "printf 'both\\ninstalled-only\\n'",
				Install:  "true %s",
				Packages: []string{"both", "declared-only"},
			},
		},
	}

	diffs := Reconcile(m)
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	d := diffs[0]
	if !d.Available {
		t.Fatal("a source whose probe is on PATH was reported unavailable")
	}
	if !reflect.DeepEqual(d.Missing, []string{"declared-only"}) {
		t.Fatalf("Missing = %v, want the declared-but-absent package", d.Missing)
	}
	if !reflect.DeepEqual(d.Extra, []string{"installed-only"}) {
		t.Fatalf("Extra = %v, want the installed-but-undeclared package", d.Extra)
	}
	if d.Managed != 1 {
		t.Fatalf("Managed = %d, want 1 for the package in both sets", d.Managed)
	}
}

// TestReconcileTreatsAnUnlistableSourceAsUnavailable guards a dangerous
// failure mode: if the list command errors, an empty installed set would make
// every declared package look missing, and sync would reinstall all of them.
func TestReconcileTreatsAnUnlistableSourceAsUnavailable(t *testing.T) {
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"sh": {
				List:     "exit 1",
				Packages: []string{"a", "b"},
			},
		},
	}
	d := Reconcile(m)[0]
	if d.Available {
		t.Fatal("a source whose list command failed was reported available")
	}
	if len(d.Missing) != 0 {
		t.Fatalf("Missing = %v, want nothing when the source cannot be queried", d.Missing)
	}
}

func TestUnavailableSourceIsNotReportedAsAllMissing(t *testing.T) {
	// The distinction matters: "apt is not on this Mac" must not become
	// "every apt package is missing", which sync would try to install.
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"definitely-not-a-real-tool-xyz": {Packages: []string{"a", "b", "c"}},
		},
	}
	d := Reconcile(m)[0]
	if d.Available {
		t.Skip("environment unexpectedly has this tool")
	}
	if len(d.Missing) != 0 {
		t.Fatalf("unavailable source reported %d missing packages", len(d.Missing))
	}
}

func TestInstallCmdUsesManifestOverride(t *testing.T) {
	got := InstallCmd("custom", &manifest.PkgList{Install: "mytool add %s --yes"}, "pkg")
	want := []string{"sh", "-c", "mytool add pkg --yes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstallCmd = %v, want %v", got, want)
	}
}

func TestInstallCmdFallsBackToBuiltin(t *testing.T) {
	got := InstallCmd("brew", &manifest.PkgList{}, "jq")
	want := []string{"brew", "install", "jq"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstallCmd = %v, want %v", got, want)
	}
}

func TestCaskIsDarwinOnly(t *testing.T) {
	if len(builtins["cask"].OS) != 1 || builtins["cask"].OS[0] != "darwin" {
		t.Fatal("cask is not constrained to darwin")
	}
}

func TestAptUsesShowmanualNotFullList(t *testing.T) {
	// `dpkg -l` returns thousands of auto-installed dependencies; declaring
	// them would make the manifest meaningless.
	list := builtins["apt"].listCmd
	if len(list) < 2 || list[1] != "showmanual" {
		t.Fatalf("apt list command is %v, want apt-mark showmanual", list)
	}
}

func TestUndeclaredBinariesSkipsDeclaredOnes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"known-tool", "mystery"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"brew": {Binaries: []manifest.Binary{{Name: "known-tool"}}},
		},
	}

	got, err := UndeclaredBinaries(m, dir, nil)
	if err != nil {
		t.Fatalf("UndeclaredBinaries: %v", err)
	}
	if len(got) != 1 || got[0] != "mystery" {
		t.Fatalf("UndeclaredBinaries = %v, want just the undeclared one", got)
	}
}

func TestUndeclaredBinariesIgnoresNonExecutables(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := UndeclaredBinaries(&manifest.Manifest{}, dir, nil)
	if err != nil {
		t.Fatalf("UndeclaredBinaries: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("UndeclaredBinaries = %v, want nothing for a non-executable", got)
	}
}

func TestUndeclaredBinariesTreatsPackageNamesAsDeclared(t *testing.T) {
	// A brew formula named `jq` installs a binary named `jq`; reporting it as
	// an undeclared mystery binary would be noise.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jq"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{"brew": {Packages: []string{"jq"}}},
	}
	got, _ := UndeclaredBinaries(m, dir, nil)
	if len(got) != 0 {
		t.Fatalf("UndeclaredBinaries = %v, want the declared package excluded", got)
	}
}

func TestUndeclaredBinariesOnMissingDirIsNotAnError(t *testing.T) {
	// A machine with no ~/.local/bin yet must not fail `dots pkg bin`.
	got, err := UndeclaredBinaries(&manifest.Manifest{}, filepath.Join(t.TempDir(), "absent"), nil)
	if err != nil {
		t.Fatalf("UndeclaredBinaries: %v", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestCheckBinariesReportsAbsentDeclaration(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"brew": {Binaries: []manifest.Binary{
				{Name: "gone", Dir: dir, Install: "curl example.com | sh"},
			}},
		},
	}
	states := CheckBinaries(m)
	if len(states) != 1 {
		t.Fatalf("got %d states, want 1", len(states))
	}
	if states[0].Present {
		t.Fatal("a binary that does not exist was reported present")
	}
	if states[0].Install == "" {
		t.Fatal("the reinstall command was lost, which is the whole point of declaring it")
	}
}

func TestCheckBinariesSkipsOtherOS(t *testing.T) {
	m := &manifest.Manifest{
		Packages: map[string]*manifest.PkgList{
			"brew": {Binaries: []manifest.Binary{{Name: "x", OS: []string{"plan9"}}}},
		},
	}
	if got := CheckBinaries(m); len(got) != 0 {
		t.Fatalf("CheckBinaries = %v, want nothing for another OS", got)
	}
}
