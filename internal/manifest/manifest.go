// Package manifest reads dotx.toml, the declarative description of what a
// machine should look like: which paths are dotfiles, which of them carry
// secrets, and which packages should be installed from which source.
//
// The manifest is the only place that knows intent. Everything else in dotx
// compares intent against what is actually on disk.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// Manifest is the parsed dotx.toml.
type Manifest struct {
	// Path the manifest was read from, for error messages and for resolving
	// relative store paths.
	Path string `toml:"-"`

	Store    Store               `toml:"store"`
	Secrets  Secrets             `toml:"secrets"`
	Dotfiles []Group             `toml:"dotfiles"`
	Packages map[string]*PkgList `toml:"packages"`
}

// Store locates the git repositories that hold tracked content. dotx keeps the
// existing bare-repo-over-$HOME layout rather than inventing a source tree:
// migrating 2000 tracked paths into a new directory is a cost with no payoff,
// and `config`/`secret` keep working while dotx is adopted.
type Store struct {
	// Config is the bare repo holding non-secret dotfiles.
	Config string `toml:"config"`
	// Secret is the bare repo holding encrypted material.
	Secret string `toml:"secret"`
	// WorkTree is the checkout root both repos apply to.
	WorkTree string `toml:"work_tree"`
}

// Secrets configures the age-based secret backend.
type Secrets struct {
	// Identity is the age private key file. Never itself tracked.
	Identity string `toml:"identity"`
	// Recipients are the age public keys ciphertext is encrypted to. Listing
	// more than one is how a second machine or a recovery key gets access.
	Recipients []string `toml:"recipients"`
	// Vault is the encrypted key-value store, relative to WorkTree.
	Vault string `toml:"vault"`
}

// Group is one logical bundle of dotfiles: a name, the paths it covers, and
// the conditions under which it applies.
type Group struct {
	Name string `toml:"name"`

	// Include is a list of globs relative to WorkTree. doublestar syntax, so
	// `.claude/skills/**` picks up a whole subtree — the thing plain `git add`
	// on a bare repo silently never did.
	Include []string `toml:"include"`
	// Exclude prunes from Include. Applied after Include, so a broad include
	// plus a narrow exclude is the normal way to express a group.
	Exclude []string `toml:"exclude"`

	// OS restricts the group to matching runtime.GOOS values. Empty means all.
	OS []string `toml:"os"`
	// Host restricts the group to matching hostnames. Empty means all.
	Host []string `toml:"host"`

	// Secret marks the whole group as secret-bearing: it is stored in the
	// secret repo, encrypted, rather than in the config repo.
	Secret bool `toml:"secret"`

	// Template marks paths in this group as Go templates rendered on apply.
	// This is how a config file with one secret field stays in the config repo:
	// the file is committed as `.tmpl` with a {{ secret "..." }} call where the
	// value goes, and the rendered output is never committed.
	Template bool `toml:"template"`
}

// PkgList is the declared package set for one source (brew, cargo, ...).
type PkgList struct {
	// Install is the command template used to install one package. %s is the
	// package name. Empty means the source is known to dotx natively.
	Install string `toml:"install"`
	// List is the command that prints installed package names, one per line.
	List string `toml:"list"`

	// Packages declared for every OS.
	Packages []string `toml:"packages"`
	// Darwin and Linux add OS-specific packages on top of Packages.
	Darwin []string `toml:"darwin"`
	Linux  []string `toml:"linux"`

	// Binaries records things installed by curl/release-download, which no
	// package manager can enumerate. Each entry reproduces one binary.
	Binaries []Binary `toml:"binaries"`
}

// Binary is a manually-installed executable: the thing that lands in
// ~/.local/bin from a curl one-liner and is then impossible to account for.
type Binary struct {
	// Name is the file name under the install directory.
	Name string `toml:"name"`
	// Dir is where it lives. Defaults to ~/.local/bin.
	Dir string `toml:"dir"`
	// From is a human-readable origin: an upstream URL or repo.
	From string `toml:"from"`
	// Install is the shell command that (re)installs it. Recorded so a fresh
	// machine can reproduce the binary instead of guessing where it came from.
	Install string `toml:"install"`
	// Version, if the binary reports one, is the command to print it.
	Version string `toml:"version"`
	// OS restricts the binary to matching runtime.GOOS values.
	OS []string `toml:"os"`
}

// DefaultPath is where dotx looks for its manifest when none is given.
func DefaultPath() string {
	if p := os.Getenv("DOTX_MANIFEST"); p != "" {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "dotx", "dotx.toml")
	}
	return "dotx.toml"
}

// Load reads and validates a manifest, expanding ~ in path fields.
func Load(path string) (*Manifest, error) {
	if path == "" {
		path = DefaultPath()
	}
	var m Manifest
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	m.Path = path

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	m.Store.Config = expand(m.Store.Config, home)
	m.Store.Secret = expand(m.Store.Secret, home)
	m.Store.WorkTree = expand(m.Store.WorkTree, home)
	m.Secrets.Identity = expand(m.Secrets.Identity, home)

	if m.Store.WorkTree == "" {
		m.Store.WorkTree = home
	}
	for i := range m.Dotfiles {
		if m.Dotfiles[i].Name == "" {
			return nil, fmt.Errorf("%s: dotfiles group %d has no name", path, i)
		}
	}
	return &m, nil
}

func expand(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	default:
		return p
	}
}

// Applies reports whether a group is active on this machine.
func (g Group) Applies(host string) bool {
	return matches(g.OS, runtime.GOOS) && matches(g.Host, host)
}

// Applies reports whether a binary should be present on this machine.
func (b Binary) Applies() bool { return matches(b.OS, runtime.GOOS) }

// matches treats an empty constraint list as "no constraint".
func matches(allowed []string, actual string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == actual {
			return true
		}
	}
	return false
}

// Resolved returns the package names declared for this OS.
func (p *PkgList) Resolved() []string {
	out := append([]string(nil), p.Packages...)
	switch runtime.GOOS {
	case "darwin":
		out = append(out, p.Darwin...)
	case "linux":
		out = append(out, p.Linux...)
	}
	return out
}
