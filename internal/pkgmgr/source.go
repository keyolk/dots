// Package pkgmgr reconciles declared packages against what is actually
// installed, across every source a machine accumulates.
//
// The measured starting point on this machine: 316 brew formulae, 31 casks, 20
// cargo crates, 374 bun globals, 5 mise tools, 1 krew plugin, and roughly 60
// binaries in ~/.local/bin with no recorded origin at all — and no Brewfile.
// None of it is reproducible on a new machine. The fix is not to pick one
// package manager but to make every source enumerable through one interface.
package pkgmgr

import (
	"bufio"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/keyolk/dots/internal/manifest"
)

// Source enumerates and installs packages for one package manager.
type Source struct {
	// Name is the manifest key: brew, cask, cargo, bun, go, mise, krew, apt.
	Name string
	// Probe is the executable that must exist for this source to be usable.
	Probe string
	// listCmd prints installed package names, one per line.
	listCmd []string
	// installCmd is appended with the package name.
	installCmd []string
	// OS restricts the source; empty means any.
	OS []string
}

// Diff is the reconciliation result for one source.
type Diff struct {
	Source string
	// Available is false when the source's tool is not installed here — the
	// difference between "nothing declared" and "cannot tell".
	Available bool
	// Missing is declared but not installed.
	Missing []string
	// Extra is installed but not declared. Not an error: it is the working set
	// waiting to be either adopted into the manifest or removed.
	Extra []string
	// Managed is the count of packages in both sets.
	Managed int
}

// builtins are the sources dots knows how to drive without configuration.
// A manifest can still override any of them, but a fresh machine gets working
// behaviour with an empty [packages.brew] table.
var builtins = map[string]Source{
	"brew": {
		Name: "brew", Probe: "brew", OS: []string{"darwin", "linux"},
		listCmd:    []string{"brew", "list", "--formula", "-1"},
		installCmd: []string{"brew", "install"},
	},
	"cask": {
		Name: "cask", Probe: "brew", OS: []string{"darwin"},
		listCmd:    []string{"brew", "list", "--cask", "-1"},
		installCmd: []string{"brew", "install", "--cask"},
	},
	"cargo": {
		Name: "cargo", Probe: "cargo",
		listCmd:    []string{"cargo", "install", "--list"},
		installCmd: []string{"cargo", "install"},
	},
	"go": {
		Name: "go", Probe: "go",
		// `go install` has no list command; dots reads ~/go/bin instead, which
		// the parser below special-cases.
		installCmd: []string{"go", "install"},
	},
	"bun": {
		Name: "bun", Probe: "bun",
		listCmd:    []string{"bun", "pm", "ls", "-g"},
		installCmd: []string{"bun", "add", "-g"},
	},
	"mise": {
		Name: "mise", Probe: "mise",
		listCmd:    []string{"mise", "ls", "--installed"},
		installCmd: []string{"mise", "use", "-g"},
	},
	"krew": {
		Name: "krew", Probe: "kubectl",
		listCmd:    []string{"kubectl", "krew", "list"},
		installCmd: []string{"kubectl", "krew", "install"},
	},
	"apt": {
		Name: "apt", Probe: "apt-mark", OS: []string{"linux"},
		// apt-mark showmanual lists what was explicitly installed, excluding
		// the thousands of automatically-pulled dependencies.
		listCmd:    []string{"apt-mark", "showmanual"},
		installCmd: []string{"sudo", "apt-get", "install", "-y"},
	},
}

// Known returns the builtin source names in a stable order.
func Known() []string {
	out := make([]string, 0, len(builtins))
	for k := range builtins {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolve merges a manifest entry over the builtin for that name.
func resolve(name string, p *manifest.PkgList) Source {
	s, ok := builtins[name]
	if !ok {
		s = Source{Name: name, Probe: name}
	}
	if p != nil {
		if p.List != "" {
			s.listCmd = []string{"sh", "-c", p.List}
		}
		if p.Install != "" {
			// %s is substituted per package at install time.
			s.installCmd = nil
		}
	}
	return s
}

// Available reports whether this source can be queried on this machine.
func (s Source) Available() bool {
	if len(s.OS) > 0 {
		ok := false
		for _, o := range s.OS {
			if o == runtime.GOOS {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	_, err := exec.LookPath(s.Probe)
	return err == nil
}

// Installed enumerates what this source currently has.
func (s Source) Installed() ([]string, error) {
	if s.Name == "go" {
		return goBinaries()
	}
	if len(s.listCmd) == 0 {
		return nil, fmt.Errorf("source %s has no list command", s.Name)
	}
	out, err := exec.Command(s.listCmd[0], s.listCmd[1:]...).Output()
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", s.Name, err)
	}
	return s.parse(string(out)), nil
}

// parse normalises each source's list output to bare package names.
func (s Source) parse(out string) []string {
	var res []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch s.Name {
		case "cargo":
			// "ripgrep v14.1.0:" header lines, then indented binary names.
			if strings.HasPrefix(sc.Text(), " ") {
				continue
			}
			line = strings.Fields(line)[0]
		case "bun":
			// Tree output: "├── typescript@5.4.0".
			line = strings.TrimLeft(line, "├└─│ ")
			if line == "" || strings.HasSuffix(line, "node_modules") {
				continue
			}
			if i := strings.LastIndex(line, "@"); i > 0 {
				line = line[:i]
			}
		case "mise":
			// "go  1.25.1  ~/.mise.toml  1.25" -> "go".
			line = strings.Fields(line)[0]
		case "krew":
			if strings.HasPrefix(line, "PLUGIN") {
				continue
			}
			line = strings.Fields(line)[0]
		}
		res = append(res, line)
	}
	sort.Strings(res)
	return dedupe(res)
}

// Reconcile compares the manifest against reality for every declared source.
func Reconcile(m *manifest.Manifest) []Diff {
	names := make([]string, 0, len(m.Packages))
	for k := range m.Packages {
		names = append(names, k)
	}
	sort.Strings(names)

	out := make([]Diff, 0, len(names))
	for _, name := range names {
		list := m.Packages[name]
		src := resolve(name, list)
		d := Diff{Source: name, Available: src.Available()}
		if !d.Available {
			out = append(out, d)
			continue
		}

		installed, err := src.Installed()
		if err != nil {
			// A source that cannot be listed is reported as unavailable rather
			// than as "everything is missing", which would be a lie that
			// `dots pkg sync` would then act on.
			d.Available = false
			out = append(out, d)
			continue
		}
		have := set(installed)
		want := set(list.Resolved())

		for _, w := range list.Resolved() {
			if !have[w] {
				d.Missing = append(d.Missing, w)
			} else {
				d.Managed++
			}
		}
		for _, h := range installed {
			if !want[h] {
				d.Extra = append(d.Extra, h)
			}
		}
		sort.Strings(d.Missing)
		sort.Strings(d.Extra)
		out = append(out, d)
	}
	return out
}

// InstallCmd builds the command that installs one package from a source.
func InstallCmd(name string, p *manifest.PkgList, pkg string) []string {
	if p != nil && p.Install != "" {
		return []string{"sh", "-c", strings.ReplaceAll(p.Install, "%s", pkg)}
	}
	src := resolve(name, p)
	if len(src.installCmd) == 0 {
		return nil
	}
	return append(append([]string(nil), src.installCmd...), pkg)
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func dedupe(xs []string) []string {
	out := xs[:0]
	var prev string
	for i, x := range xs {
		if i == 0 || x != prev {
			out = append(out, x)
		}
		prev = x
	}
	return out
}
