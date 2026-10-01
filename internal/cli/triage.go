package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/manifest"
	"github.com/keyolk/dots/internal/ui"
)

// verdict is what triage proposes for a set of orphans.
type verdict int

const (
	vIgnore  verdict = iota // add an exclude: the file is derived or disposable
	vDeclare                // add an include: the file is authored content
	vUnsure                 // no rule matched; say so rather than guess
)

// proposal is one line of the report: a pattern, a verdict, and the orphans it
// accounts for. Patterns rather than paths are the unit, because a manifest of
// literal paths is the thing dots exists to replace — an exclude written as
// `.spin/.watchman-cookie-*` keeps working tomorrow, and 2108 literal lines do
// not.
type proposal struct {
	pattern string
	group   string
	verdict verdict
	reason  string
	paths   []string
}

func newTriageCmd() *cobra.Command {
	var apply, showPaths bool

	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Classify files no manifest group accounts for",
		Long: `triage surveys the directories that already hold a tracked file and reports
the files in them no group claims and no exclude covers.

This is the gap status cannot report. "untracked" means declared but never
committed -- the manifest already knows the path. An orphan is a path the
manifest has never heard of, which is the one that goes unnoticed for a year.

Each orphan is classified by shape:

  ignore    derived, generated or disposable -- logs, locks, caches, backups
  declare   authored content, by extension: config, scripts, documentation
  ?         no rule matched; a decision only you can make

--apply writes the ignore and declare lines into the manifest, as globs, and
leaves the unsure ones alone. Nothing is deleted and no file is touched: the
manifest gains lines, which dots status then acts on. An exclude that would
also cover something already tracked is refused rather than written.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			sc := dotfile.NewScanner(m, hostname())
			orphans, err := sc.Orphans()
			if err != nil {
				return err
			}
			if len(orphans) == 0 {
				fmt.Println("every file beside a tracked one is accounted for")
				return nil
			}

			props := classify(orphans)
			declared, err := sc.Declared()
			if err != nil {
				return err
			}

			var edits []manifest.Edit
			var unsure []proposal
			for _, p := range props {
				label, style := "ignore", ui.StateUndeclared
				switch p.verdict {
				case vDeclare:
					label, style = "declare", ui.StateUntracked
				case vUnsure:
					label, style = "?", ui.StateModified
				}
				fmt.Printf("%s %5d  %-38s %s\n",
					style.Width(8).Render(label), len(p.paths),
					p.pattern, ui.Muted.Render(p.reason))
				if showPaths {
					for _, path := range p.paths {
						fmt.Printf("%s\n", ui.Muted.Render("           "+path))
					}
				}

				if p.verdict == vUnsure {
					unsure = append(unsure, p)
					continue
				}
				// An exclude that also covers a tracked file would untrack it
				// on the next prune. The bulk pattern is built from what the
				// orphan names share precisely so it cannot reach a tracked
				// neighbour -- .spin/** would have swallowed .spin/config --
				// and this check is what proves it for every pattern.
				if p.verdict == vIgnore {
					if hit := covers(p.pattern, declared); hit != "" {
						fmt.Printf("%s   %s\n", ui.Refused.Render("         refused"),
							ui.Muted.Render("would also exclude "+hit))
						continue
					}
				}
				key := "exclude"
				if p.verdict == vDeclare {
					key = "include"
				}
				edits = append(edits, manifest.Edit{Group: p.group, Key: key, Pattern: p.pattern})
			}

			n := 0
			for _, p := range props {
				if p.verdict != vUnsure {
					n += len(p.paths)
				}
			}
			fmt.Println()
			if !apply {
				fmt.Printf("%d path(s) classified in %d pattern(s)", n, len(edits))
				if len(unsure) > 0 {
					fmt.Printf(", %s", ui.Warn.Render(
						fmt.Sprintf("%d needing a decision", countPaths(unsure))))
				}
				fmt.Printf("\n%s\n", ui.Fix.Render("dots triage --apply"))
				return nil
			}

			written, err := manifest.Apply(m.Path, edits)
			if err != nil {
				return err
			}
			fmt.Printf("%s %d line(s) to %s\n", ui.OK.Render("added"), written, m.Path)
			if len(unsure) > 0 {
				fmt.Printf("%s %d path(s) left for you to decide\n",
					ui.Warn.Render("unresolved"), countPaths(unsure))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "write the classified patterns into the manifest")
	cmd.Flags().BoolVar(&showPaths, "paths", false, "list the files behind each pattern")
	return cmd
}

func countPaths(ps []proposal) int {
	n := 0
	for _, p := range ps {
		n += len(p.paths)
	}
	return n
}

// covers reports the first declared path a pattern would match.
func covers(pattern string, declared []string) string {
	for _, d := range declared {
		if ok, _ := doublestar.Match(pattern, d); ok {
			return d
		}
	}
	return ""
}

// rule names a shape of file, the verdict it implies, and the pattern that
// verdict should be written as.
//
// The pattern matters as much as the verdict. A rule that fires on a name --
// "cache", "history" -- has no business widening to an extension: `*.json`
// written because two caches matched would also swallow every config file that
// arrives in that directory later. Only a rule that fires on a suffix may
// generalise, because the suffix is the thing it matched.
type rule struct {
	reason  string
	verdict verdict
	// match reports whether the rule fires, and returns the pattern to write.
	// An empty pattern means the path itself, which is what a name-based rule
	// returns: it recognised this file, not a class of them.
	match func(dir, base string) (pattern string, ok bool)
}

// suffixes that mark a file as something a tool wrote, not something you did.
var derivedSuffixes = []string{
	".lock", ".log", ".pid", ".sock", ".stamp", ".gob", ".pyc",
	".db-shm", ".db-wal", ".db", ".swp", ".tmp",
}

// markers that mark a copy of something else. The timestamped forms --
// `.bak-20260824-151215`, `.tweb-backup-1786425212-94816` -- are the same
// decision as the bare suffix, so the pattern wraps the marker on both sides.
var backupMarkers = []string{
	".bak", ".backup", "-backup-", ".orig", ".original", ".sample",
}

// extensions that normally hold something a person wrote. .json is absent on
// purpose: on this machine it is state far more often than config --
// badges.json, policy-limits.json, gh-pr-status-cache.json -- so a .json file
// is a question, not an answer.
var authoredExts = map[string]bool{
	".md": true, ".toml": true, ".yaml": true, ".yml": true, ".conf": true,
	".ini": true, ".fish": true, ".sh": true, ".bash": true, ".zsh": true,
	".py": true, ".lua": true, ".vim": true, ".tmpl": true, ".rc": true,
}

var rules = []rule{
	{"generated", vIgnore, func(dir, base string) (string, bool) {
		for _, suf := range derivedSuffixes {
			if strings.HasSuffix(base, suf) {
				return filepath.Join(dir, "*"+suf), true
			}
		}
		return "", false
	}},
	{"backup copy", vIgnore, func(dir, base string) (string, bool) {
		for _, mk := range backupMarkers {
			if strings.Contains(base, mk) {
				return filepath.Join(dir, "*"+mk+"*"), true
			}
		}
		if strings.HasSuffix(base, "~") {
			return filepath.Join(dir, "*~"), true
		}
		return "", false
	}},
	{"cache", vIgnore, func(dir, base string) (string, bool) {
		return "", strings.Contains(strings.ToLower(base), "cache")
	}},
	{"shell history", vIgnore, func(dir, base string) (string, bool) {
		return "", strings.Contains(strings.ToLower(base), "history")
	}},
	{"authored", vDeclare, func(dir, base string) (string, bool) {
		ext := strings.ToLower(filepath.Ext(base))
		if !authoredExts[ext] {
			return "", false
		}
		return filepath.Join(dir, "*"+ext), true
	}},
}

// bulkThreshold is how many orphans a directory must contribute before triage
// proposes one prefix glob for the lot. Below it, per-file rules read better;
// above it, a report nobody finishes reading is a report nobody acts on.
const bulkThreshold = 10

// classify turns orphans into proposals, collapsing each directory's orphans
// into as few patterns as the rules allow.
func classify(orphans []dotfile.Orphan) []proposal {
	byDir := map[string][]dotfile.Orphan{}
	var dirs []string
	for _, o := range orphans {
		if _, ok := byDir[o.Dir]; !ok {
			dirs = append(dirs, o.Dir)
		}
		byDir[o.Dir] = append(byDir[o.Dir], o)
	}
	sort.Strings(dirs)

	var out []proposal
	for _, dir := range dirs {
		in := byDir[dir]
		// A directory full of one tool's droppings is one decision, not
		// hundreds: .spin held 2108 watchman cookies beside two tracked files.
		// The pattern comes from what the names actually share, so it cannot
		// reach the tracked ones.
		if len(in) >= bulkThreshold {
			if pre := commonPrefix(in); len(pre) >= 3 {
				var paths []string
				for _, o := range in {
					paths = append(paths, o.Path)
				}
				out = append(out, proposal{
					pattern: filepath.Join(dir, pre) + "*",
					group:   in[0].Group,
					verdict: vIgnore,
					reason:  fmt.Sprintf("%d files sharing one prefix", len(in)),
					paths:   paths,
				})
				continue
			}
		}
		out = append(out, classifyDir(dir, in)...)
	}
	return out
}

// classifyDir applies the per-file rules, merging everything that produced the
// same pattern into one proposal.
func classifyDir(dir string, in []dotfile.Orphan) []proposal {
	type key struct {
		pattern string
		reason  string
		v       verdict
	}
	groups := map[key][]string{}
	var order []key

	for _, o := range in {
		base := filepath.Base(o.Path)
		k := key{o.Path, "no rule matched", vUnsure}
		for _, r := range rules {
			pattern, ok := r.match(dir, base)
			if !ok {
				continue
			}
			// A rule that recognised this file rather than a class of them
			// returns no pattern, and the path stands in for itself.
			if pattern == "" {
				pattern = o.Path
			}
			v := r.verdict
			// The work tree root is not a config directory, it is where
			// everything lands. "authored" there found ten draft replies and
			// proposed tracking every future *.md in $HOME. A file at the root
			// is a decision, not an inference; the ignore rules still hold,
			// since a backup is a backup wherever it sits.
			if v == vDeclare && dir == "." {
				k = key{o.Path, "at the work tree root", vUnsure}
				break
			}
			k = key{pattern, r.reason, v}
			break
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], o.Path)
	}

	var out []proposal
	for _, k := range order {
		out = append(out, proposal{
			pattern: k.pattern, group: in[0].Group,
			verdict: k.v, reason: k.reason, paths: groups[k],
		})
	}
	return out
}

// commonPrefix returns the longest basename prefix every orphan shares.
func commonPrefix(in []dotfile.Orphan) string {
	pre := filepath.Base(in[0].Path)
	for _, o := range in[1:] {
		b := filepath.Base(o.Path)
		i := 0
		for i < len(pre) && i < len(b) && pre[i] == b[i] {
			i++
		}
		pre = pre[:i]
		if pre == "" {
			return ""
		}
	}
	return pre
}
