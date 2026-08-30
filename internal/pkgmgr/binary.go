package pkgmgr

import (
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keyolk/dotx/internal/manifest"
)

// goBinaries enumerates ~/go/bin, since `go install` keeps no manifest of what
// it has installed. Each binary's module path is read from its embedded build
// info, which is what `go install` would need to reinstall it.
func goBinaries() ([]string, error) {
	dir := goBinDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if bi, err := buildinfo.ReadFile(p); err == nil && bi.Path != "" {
			out = append(out, bi.Path)
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func goBinDir() string {
	if p := os.Getenv("GOBIN"); p != "" {
		return p
	}
	if p := os.Getenv("GOPATH"); p != "" {
		return filepath.Join(p, "bin")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "go", "bin")
}

// BinaryState is one manually-installed binary checked against disk.
type BinaryState struct {
	manifest.Binary
	// Present is whether the file exists where declared.
	Present bool
	// Path is the resolved absolute location.
	Path string
	// Version is the output of the binary's version command, when declared.
	Version string
}

// CheckBinaries resolves declared binaries against the filesystem.
//
// These are the entries no package manager can answer for: a binary that
// arrived via `curl | sh` or a GitHub release tarball is indistinguishable from
// one compiled locally, and neither records where it came from. Declaring them
// turns "60 mystery files in ~/.local/bin" into a reproducible list.
func CheckBinaries(m *manifest.Manifest) []BinaryState {
	var out []BinaryState
	for _, list := range m.Packages {
		for _, b := range list.Binaries {
			if !b.Applies() {
				continue
			}
			st := BinaryState{Binary: b, Path: resolveBinPath(b)}
			if fi, err := os.Stat(st.Path); err == nil && !fi.IsDir() {
				st.Present = true
				st.Version = probeVersion(b, st.Path)
			}
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UndeclaredBinaries lists executables in dir that no manifest entry claims and
// that no package manager owns. This is the discovery half: it produces the
// candidates to declare, rather than requiring the list up front.
func UndeclaredBinaries(m *manifest.Manifest, dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	declared := map[string]bool{}
	for _, list := range m.Packages {
		for _, b := range list.Binaries {
			declared[b.Name] = true
		}
		for _, p := range list.Resolved() {
			// A brew formula named `ripgrep` installs `rg`, so this only
			// catches the common name==binary case. The residue is reported,
			// which is the safe direction: a false candidate is dismissed by
			// reading one line, a missed one stays invisible.
			declared[p] = true
			if i := strings.LastIndex(p, "/"); i >= 0 {
				declared[p[i+1:]] = true
			}
		}
	}

	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || declared[name] || strings.HasPrefix(name, ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Mode()&0o111 == 0 {
			continue
		}
		// Symlinks into a Cellar or a cargo target are owned by their manager,
		// not manually installed.
		if fi.Mode()&os.ModeSymlink != 0 {
			if dst, err := os.Readlink(filepath.Join(dir, name)); err == nil && managed(dst) {
				continue
			}
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func managed(path string) bool {
	for _, marker := range []string{"/Cellar/", "/Caskroom/", "/.cargo/", "/go/bin/", "/.bun/"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

func resolveBinPath(b manifest.Binary) string {
	dir := b.Dir
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".local", "bin")
	} else if strings.HasPrefix(dir, "~/") {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, dir[2:])
	}
	return filepath.Join(dir, b.Name)
}

func probeVersion(b manifest.Binary, path string) string {
	if b.Version == "" {
		return ""
	}
	cmd := exec.Command("sh", "-c", strings.ReplaceAll(b.Version, "%s", path))
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}
