// Package dotfile compares three views of the machine's configuration: what the
// manifest declares should be tracked, what the git store actually tracks, and
// what exists on disk.
//
// The existing setup collapses these into one and loses the difference. With
// status.showUntrackedFiles=no a bare repo over $HOME reports only changes to
// files it already knows, so a newly written hook, skill, or script is invisible
// forever — measured on this machine: 42 of 123 files in ~/.local/bin tracked,
// 0 of 67 skills, 0 of 16 agents. Making the three views explicit is the fix.
package dotfile

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/keyolk/dots/internal/git"
	"github.com/keyolk/dots/internal/manifest"
	"github.com/keyolk/dots/internal/normalize"
	"github.com/keyolk/dots/internal/tmpl"
)

// State is where one path stands relative to the manifest and the store.
type State int

const (
	// Clean: declared, tracked, and identical on disk.
	Clean State = iota
	// Modified: declared and tracked, but the working copy differs.
	Modified
	// Untracked: the manifest says it should be tracked and it exists on disk,
	// but the store has never seen it. This is the state the old setup could
	// not report.
	Untracked
	// Missing: tracked by the store but absent from disk.
	Missing
	// Undeclared: tracked by the store but no longer matched by any manifest
	// group — leftovers from an earlier layout, e.g. .spin/ watchman cookies.
	Undeclared
	// Submodule: a gitlink. No file glob can match a commit pointer, so these
	// are recognised from the index rather than declared, and are never
	// candidates for pruning.
	Submodule
	// Inactive: declared, but by a group whose os/host constraint excludes this
	// machine. A Linux-only .xprofile is not undeclared on a Mac — reporting it
	// as such would let a prune here delete the other machine's config.
	Inactive
	// Artifact: matched by a group, but a compiled binary or an oversized file.
	// Reported rather than silently skipped, because the fix is a manifest
	// exclude or a package declaration, not a bigger repo.
	Artifact
)

func (s State) String() string {
	switch s {
	case Clean:
		return "clean"
	case Modified:
		return "modified"
	case Untracked:
		return "untracked"
	case Missing:
		return "missing"
	case Undeclared:
		return "undeclared"
	case Submodule:
		return "submodule"
	case Inactive:
		return "inactive"
	case Artifact:
		return "artifact"
	}
	return "unknown"
}

// Symbol is the single-character status marker, matching git's vocabulary
// closely enough that muscle memory carries over.
func (s State) Symbol() string {
	switch s {
	case Clean:
		return " "
	case Modified:
		return "M"
	case Untracked:
		return "?"
	case Missing:
		return "D"
	case Undeclared:
		return "-"
	case Submodule:
		return "S"
	case Inactive:
		return "~"
	case Artifact:
		return "B"
	}
	return "!"
}

// Entry is one path with its resolved state.
type Entry struct {
	// Path is relative to the work tree.
	Path string
	// Group is the manifest group that claimed it, empty when Undeclared.
	Group string
	// Store names the group's sensitivity: "secret" for a group exempt from
	// the credential scan, "config" otherwise. It no longer selects a
	// repository -- there is only one.
	Store string
	State State
	// Template is true when Path is a `.tmpl` source rather than a live file.
	Template bool
}

// Scanner resolves manifest globs against disk and the git stores.
type Scanner struct {
	m    *manifest.Manifest
	host string

	repo *git.Repo

	// subRoots caches the gitlink paths, so the walk can prune them. A
	// submodule is its own repository: its files belong to that repo, and
	// staging one against the outer store fails outright.
	subRoots map[string]bool
}

// NewScanner builds a scanner over the manifest's two stores.
func NewScanner(m *manifest.Manifest, host string) *Scanner {
	return &Scanner{
		m:    m,
		host: host,
		repo: git.New(m.Store.Config, m.Store.WorkTree),
	}
}

// sameAfterNormalize reports whether the working copy and the stored copy are
// equivalent once both are canonicalised. A file that cannot be read, fetched
// or normalised is treated as genuinely modified: reporting a change that is
// not there is recoverable, hiding one is not.
func (s *Scanner) sameAfterNormalize(rel, kind string) bool {
	if !normalize.Known(normalize.Kind(kind)) {
		return false
	}
	live, err := os.ReadFile(filepath.Join(s.m.Store.WorkTree, rel))
	if err != nil {
		return false
	}
	stored, err := s.repo.Show("HEAD:" + rel)
	if err != nil {
		return false
	}
	a, err := normalize.Apply(normalize.Kind(kind), live)
	if err != nil {
		return false
	}
	b, err := normalize.Apply(normalize.Kind(kind), stored)
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}

// isArtifact reports whether a declared path is a build product rather than
// configuration.
func (s *Scanner) isArtifact(rel string) bool {
	abs := filepath.Join(s.m.Store.WorkTree, rel)
	fi, err := os.Lstat(abs)
	if err != nil {
		return false
	}
	// A symlink is judged by its own size, never by following it into a
	// package manager's cellar.
	if fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if fi.Size() > maxTrackedSize {
		return true
	}
	return isCompiledBinary(abs)
}

// submoduleRoots returns the gitlink paths, cached.
func (s *Scanner) submoduleRoots() map[string]bool {
	if s.subRoots != nil {
		return s.subRoots
	}
	s.subRoots = map[string]bool{}
	subs, err := s.repo.Submodules()
	if err != nil {
		// A store that cannot be queried simply contributes no roots; the scan
		// still runs, and the paths surface as ordinary entries.
		return s.subRoots
	}
	for _, sub := range subs {
		s.subRoots[sub] = true
	}
	return s.subRoots
}

// Scan produces the full picture, sorted by path.
func (s *Scanner) Scan() ([]Entry, error) {
	declared, inactiveGroups, err := s.declaredWithInactive()
	if err != nil {
		return nil, err
	}

	files, err := s.repo.LsFiles()
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	tracked := make(map[string]bool, len(files))
	for _, f := range files {
		tracked[f] = true
	}

	// Modified sets are queried once per repo rather than per file: shelling
	// out to `git diff` for each of ~2000 paths is the difference between a
	// 70ms status and a multi-second one.
	mod, err := s.repo.Modified()
	if err != nil {
		return nil, err
	}
	modified := make(map[string]bool, len(mod))
	for _, f := range mod {
		modified[f] = true
	}

	// Gitlinks are collected up front so the undeclared sweep below cannot
	// mistake a submodule for a leftover.
	subs, err := s.repo.Submodules()
	if err != nil {
		return nil, fmt.Errorf("store submodules: %w", err)
	}
	submodules := make(map[string]bool, len(subs))
	for _, sub := range subs {
		submodules[sub] = true
	}

	seen := map[string]bool{}
	var out []Entry

	for path, d := range declared {
		seen[path] = true
		e := Entry{Path: path, Group: d.group, Store: d.store, Template: d.template}
		isTracked := tracked[path]
		switch {
		case d.artifact && !isTracked:
			// An untracked artifact is a manifest bug to fix, not something to
			// stage. One already tracked stays reported as clean/modified so
			// its removal is a deliberate prune, not a surprise.
			e.State = Artifact
		case !isTracked:
			e.State = Untracked
		case modified[path]:
			// A file the manifest asks to normalise may differ only in the
			// ordering its owning tool happened to write. Comparing the
			// normalised forms tells a real edit apart from that churn.
			if d.normalize != "" && s.sameAfterNormalize(path, d.normalize) {
				e.State = Clean
				break
			}
			e.State = Modified
		default:
			e.State = Clean
		}
		out = append(out, e)
	}

	// Anything the store carries that the manifest no longer claims. These have
	// no declaring group, so no sensitivity to report either.
	for path := range tracked {
		if seen[path] {
			continue
		}
		if submodules[path] {
			out = append(out, Entry{Path: path, State: Submodule})
			continue
		}
		if name := claimedByInactiveGroup(inactiveGroups, path); name != "" {
			out = append(out, Entry{Path: path, Group: name, State: Inactive})
			continue
		}
		st := Undeclared
		if _, err := os.Lstat(filepath.Join(s.m.Store.WorkTree, path)); err != nil {
			st = Missing
		}
		out = append(out, Entry{Path: path, State: st})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

type decl struct {
	group     string
	store     string
	template  bool
	artifact  bool
	normalize string
}

// declared walks the work tree once per group and resolves its globs.
func (s *Scanner) declared() (map[string]decl, error) {
	out, _, err := s.declaredWithInactive()
	return out, err
}

// declaredWithInactive returns the paths active groups claim, plus the glob
// patterns belonging to groups that do not apply on this machine. The second
// set is what keeps a Linux-only path from looking undeclared on a Mac.
func (s *Scanner) declaredWithInactive() (map[string]decl, []manifest.Group, error) {
	out := map[string]decl{}
	var inactive []manifest.Group

	for _, g := range s.m.Dotfiles {
		if !g.Applies(s.host) {
			inactive = append(inactive, g)
			continue
		}
		store := "config"
		if g.Secret {
			store = "secret"
		}
		paths, err := s.matchGroup(g)
		if err != nil {
			return nil, nil, fmt.Errorf("group %s: %w", g.Name, err)
		}
		for _, p := range paths {
			// First group to claim a path wins, so an early narrow group can
			// carve an exception out of a later broad one.
			if _, taken := out[p]; taken {
				continue
			}
			out[p] = decl{
				group:     g.Name,
				store:     store,
				template:  tmpl.IsTemplate(p),
				artifact:  s.isArtifact(p),
				normalize: g.Normalize,
			}
		}
	}
	return out, inactive, nil
}

// claimedByInactiveGroup reports the name of a non-applying group whose
// patterns cover this path. Matching is done against the patterns rather than
// the filesystem, since the file usually does not exist on this machine.
func claimedByInactiveGroup(groups []manifest.Group, path string) string {
	for _, g := range groups {
		for _, pattern := range g.Include {
			if ok, _ := doublestar.Match(strings.TrimPrefix(pattern, "./"), path); ok {
				return g.Name
			}
		}
	}
	return ""
}

// matchGroup expands one group's includes against the work tree.
//
// The walk is pruned rather than filtered: a pattern like `**/*.tmpl` would
// otherwise traverse all of $HOME, including Library, node_modules trees and
// every checked-out repository under src. Measured before pruning, a single
// status took over two minutes; the cost is entirely in directories no pattern
// could ever match.
func (s *Scanner) matchGroup(g manifest.Group) ([]string, error) {
	seen := map[string]bool{}
	var out []string

	// A literal path needs no walk at all: `.bashrc` is one stat, not a
	// traversal of the work tree it happens to sit directly inside.
	var globs []string
	for _, pattern := range g.Include {
		pattern = strings.TrimPrefix(pattern, "./")
		if strings.ContainsAny(pattern, "*?[{") {
			globs = append(globs, pattern)
			continue
		}
		if s.excluded(g, pattern) || seen[pattern] {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(s.m.Store.WorkTree, pattern)); err == nil && trackable(fi.Mode()) {
			seen[pattern] = true
			out = append(out, pattern)
		}
	}

	roots := patternRoots(globs)
	for _, r := range roots {
		// A pattern with no literal prefix - "**/*.tmpl" - would walk the whole
		// home directory. On this machine that is over a million files across
		// Library, src, .cargo and .rustup, none of which a dotfile manifest
		// should ever look at. Requiring a rooted pattern is a one-line change
		// to the manifest and the difference between a 50ms status and one that
		// never finishes.
		if r == "." {
			return nil, fmt.Errorf(
				"group %q has an unrooted pattern; prefix each include with a directory (e.g. %q rather than %q)",
				g.Name, ".config/**/*.tmpl", "**/*.tmpl")
		}
	}

	// How deep each root has to be walked. A pattern without `**` matches at a
	// fixed depth, so descending past it can only waste time -- `.vim/*.vim`
	// needs one level and was walking 17137 files under .vim/plugged to find
	// three.
	depth := maxDepth(globs)

	for _, root := range roots {
		abs := filepath.Join(s.m.Store.WorkTree, root)
		limit := depth[root]
		err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// An unreadable subtree is skipped: a permission-denied on one
				// directory must not fail the whole status.
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(s.m.Store.WorkTree, p)
			if err != nil {
				return nil
			}
			if rel == "." {
				return nil
			}

			if d.IsDir() {
				if s.prune(g, rel, d.Name()) {
					return fs.SkipDir
				}
				// limit < 0 means some pattern under this root uses `**`, so
				// the whole subtree is in play. Otherwise a directory deeper
				// than the pattern can reach holds nothing worth walking --
				// the root itself is depth 0, so a limit of 0 still descends
				// into it and stops at its subdirectories.
				if limit >= 0 && depthUnder(root, rel) > limit {
					return fs.SkipDir
				}
				return nil
			}
			if !trackable(d.Type()) {
				return nil
			}
			if seen[rel] || s.excluded(g, rel) {
				return nil
			}
			if s.included(g, rel) {
				seen[rel] = true
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// maxTrackedSize is the point past which a file is almost certainly a build
// artifact rather than configuration. GitHub rejects a push containing any
// file over 100MB outright and warns past 50MB, but the real signal is that no
// hand-written config approaches even a megabyte.
const maxTrackedSize = 1 << 20 // 1MB

// isCompiledBinary reports whether a file starts with an executable magic
// number. Compiled tools land in ~/.local/bin next to shell scripts, and only
// the scripts are configuration -- the binaries are rebuilt from source or
// reinstalled by a package manager, which is what the packages section is for.
//
// Measured here before this check existed: 30 binaries totalling 509MB were
// tracked, and a 127MB telepresence made the config store unpushable.
func isCompiledBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	if n, _ := io.ReadFull(f, magic[:]); n < 4 {
		return false
	}
	switch {
	case magic == [4]byte{0x7f, 'E', 'L', 'F'}: // ELF
		return true
	case magic == [4]byte{0xcf, 0xfa, 0xed, 0xfe}, // Mach-O 64 LE
		magic == [4]byte{0xce, 0xfa, 0xed, 0xfe}, // Mach-O 32 LE
		magic == [4]byte{0xfe, 0xed, 0xfa, 0xcf}, // Mach-O 64 BE
		magic == [4]byte{0xfe, 0xed, 0xfa, 0xce}: // Mach-O 32 BE
		return true
	case magic == [4]byte{0xca, 0xfe, 0xba, 0xbe}, // Mach-O universal
		magic == [4]byte{0xbe, 0xba, 0xfe, 0xca}:
		return true
	}
	// PE is deliberately not detected by its "MZ" prefix alone: those are two
	// printable characters, and a text file that happens to start with them
	// would be misread as a binary. Windows executables do not appear in a
	// dotfile tree on macOS or Linux, so the false-positive risk is not worth
	// the coverage.
	return false
}

// trackable reports whether a directory entry is something git can store.
//
// Symlinks count: git tracks them as their target path, and dotfile trees rely
// on that — ~/.vim/autoload/plug.vim is a symlink into the vim-plug submodule,
// and excluding it would report a tracked, intentional file as undeclared.
// Sockets, devices and named pipes are excluded, since gpg-agent sockets and
// the like are runtime state that git cannot meaningfully hold.
func trackable(m os.FileMode) bool {
	return m.IsRegular() || m&os.ModeSymlink != 0
}

// maxDepth reports, per root, how many directory levels below it a match can
// appear. A `**` anywhere in a pattern makes the depth unbounded, reported as
// -1; otherwise it is the number of path separators after the root.
func maxDepth(patterns []string) map[string]int {
	out := map[string]int{}
	for _, pattern := range patterns {
		pattern = strings.TrimPrefix(pattern, "./")
		root := patternRoot(pattern)
		if strings.Contains(pattern, "**") {
			out[root] = -1
			continue
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(pattern, root), "/")
		d := strings.Count(rest, "/")
		if cur, ok := out[root]; !ok || (cur >= 0 && d > cur) {
			out[root] = d
		}
	}
	// A root that swallowed a narrower one during deduplication inherits the
	// deepest requirement of anything beneath it.
	for root, d := range out {
		for other, od := range out {
			if other != root && isUnder(other, root) {
				if od < 0 || d < 0 {
					out[root] = -1
				} else if extra := strings.Count(strings.TrimPrefix(other, root+"/"), "/") + 1 + od; extra > out[root] {
					out[root] = extra
				}
			}
		}
	}
	return out
}

// depthUnder counts the directory levels between a root and a path below it.
func depthUnder(root, path string) int {
	rest := strings.TrimPrefix(strings.TrimPrefix(path, root), "/")
	if rest == "" {
		return 0
	}
	return strings.Count(rest, "/") + 1
}

// alwaysPrune are directories no dotfile manifest should ever descend into.
// They are large, machine-generated, and owned by a tool that can recreate
// them, so walking them costs seconds and yields nothing.
var alwaysPrune = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".terraform":   true,
	"target":       true, // cargo
	".next":        true,
	".DS_Store":    true,
}

// prune reports whether a directory can be skipped entirely.
func (s *Scanner) prune(g manifest.Group, rel, base string) bool {
	if alwaysPrune[base] {
		return true
	}
	// A submodule's contents belong to its own repository. Descending into one
	// would surface its files as untracked and then fail on `git add`, which
	// refuses a pathspec inside a submodule.
	if s.submoduleRoots()[rel] {
		return true
	}
	// An exclude written as `src/**` covers the directory itself, so a
	// directory that matches an exclude is skipped along with its whole
	// subtree rather than being re-tested for every file inside it.
	for _, ex := range g.Exclude {
		if ok, _ := doublestar.Match(ex, rel); ok {
			return true
		}
	}
	return false
}

// excluded reports whether a file path is pruned by the group's excludes.
func (s *Scanner) excluded(g manifest.Group, path string) bool {
	for _, ex := range g.Exclude {
		if ok, _ := doublestar.Match(ex, path); ok {
			return true
		}
	}
	return false
}

// included reports whether any of the group's patterns claims this path.
func (s *Scanner) included(g manifest.Group, rel string) bool {
	for _, pattern := range g.Include {
		if ok, _ := doublestar.Match(strings.TrimPrefix(pattern, "./"), rel); ok {
			return true
		}
	}
	return false
}

// patternRoots reduces a set of globs to the directories that must be walked:
// the literal prefix of each pattern before its first metacharacter. A pattern
// with no literal prefix forces a walk from the work tree root, which pruning
// then has to keep cheap.
func patternRoots(patterns []string) []string {
	roots := map[string]bool{}
	for _, p := range patterns {
		roots[patternRoot(strings.TrimPrefix(p, "./"))] = true
	}
	// A root that is an ancestor of another makes the descendant redundant.
	var out []string
	for r := range roots {
		redundant := false
		for other := range roots {
			if other != r && isUnder(r, other) {
				redundant = true
				break
			}
		}
		if !redundant {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

func patternRoot(pattern string) string {
	parts := strings.Split(pattern, "/")
	var literal []string
	for _, part := range parts {
		if strings.ContainsAny(part, "*?[{") {
			break
		}
		literal = append(literal, part)
	}
	if len(literal) == 0 {
		return "."
	}
	return strings.Join(literal, "/")
}

func isUnder(path, ancestor string) bool {
	if ancestor == "." {
		return path != "."
	}
	return strings.HasPrefix(path, ancestor+"/")
}

// Repo returns the store.
func (s *Scanner) Repo() *git.Repo { return s.repo }

// WalkOrphans finds files on disk under a group's include roots that no group
// claims. It answers "what did I create and never declare?" — the complement of
// Untracked, which only covers paths the manifest already knows about.
func (s *Scanner) WalkOrphans(roots []string) ([]string, error) {
	declared, err := s.declared()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, root := range roots {
		abs := filepath.Join(s.m.Store.WorkTree, root)
		err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal
			}
			rel, rerr := filepath.Rel(s.m.Store.WorkTree, p)
			if rerr != nil {
				return nil
			}
			if _, ok := declared[rel]; !ok {
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
