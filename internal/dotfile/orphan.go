package dotfile

import (
	"os"
	"path/filepath"
	"sort"
)

// Orphan is a file sitting inside a directory the manifest surveys that no
// group claims and no group's exclude accounts for.
//
// It is the state Untracked cannot report. Untracked means "declared, and the
// store has never seen it": the manifest already knows the path exists. An
// orphan is the file the manifest has never heard of, which is the one that
// goes missing for a year — the README's whole premise, applied to the
// manifest itself rather than to `git add`.
type Orphan struct {
	// Path is relative to the work tree.
	Path string
	// Group names the group that declared something else in the same
	// directory, which is the group a declaration would most likely join.
	Group string
	// Dir is the directory it was found in, which already holds a tracked
	// file.
	Dir string
}

// Orphans reports files sitting beside tracked ones that no group accounts
// for.
//
// The survey covers exactly the directories that already hold a declared file.
// An include root is not a claim on a directory: `.aws/**/*.tmpl` says
// templates under ~/.aws are declared, not that ~/.aws should be audited —
// surveying by root walked 145481 files there, almost all of them an AWS CLI
// credential cache. A directory holding something you track is a directory
// whose contents you have already made a decision about, so a file that
// appears in it is a decision you have not made yet.
//
// A file an exclude covers is not an orphan. The exclude is a decision already
// made — re-reporting it would teach you to ignore the report, which is the
// failure mode this whole tool exists to avoid. Excludes count across groups,
// not only within the group that owns the directory: an exclude exists to drop
// a path, and nothing having claimed it afterwards is the exclude working.
func (s *Scanner) Orphans() ([]Orphan, error) {
	declared, inactive, err := s.declaredWithInactive()
	if err != nil {
		return nil, err
	}

	// Each directory is attributed to the group that declared something in it,
	// which is the group a declaration would most likely join. Two groups can
	// both declare files in one directory -- .claude holds both "claude" and
	// "claude-plugins" paths -- so the manifest's own order breaks the tie, the
	// same precedence used when two groups claim one path. Ranging over the
	// declared map instead would pick a different group on every run, and
	// triage --apply would write the same pattern into a different group each
	// time.
	rank := make(map[string]int, len(s.m.Dotfiles))
	for i, g := range s.m.Dotfiles {
		if _, seen := rank[g.Name]; !seen {
			rank[g.Name] = i
		}
	}
	owner := map[string]string{}
	for path, d := range declared {
		dir := filepath.Dir(path)
		cur, taken := owner[dir]
		if !taken || rank[d.group] < rank[cur] {
			owner[dir] = d.group
		}
	}

	dirs := make([]string, 0, len(owner))
	for dir := range owner {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var out []Orphan
	for _, dir := range dirs {
		ents, err := os.ReadDir(filepath.Join(s.m.Store.WorkTree, dir))
		if err != nil {
			// A directory that cannot be read contributes nothing; it is not
			// worth failing a survey over.
			continue
		}
		for _, e := range ents {
			rel := filepath.Join(dir, e.Name())
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil || !trackable(info.Mode()) {
				continue
			}
			if _, ok := declared[rel]; ok {
				continue
			}
			if s.excludedByAny(rel) || alwaysPrune[e.Name()] {
				continue
			}
			// A path a linux-only group declares is that machine's file, not
			// this one's leftover — the same reason status reports Inactive
			// rather than Undeclared.
			if claimedByInactiveGroup(inactive, rel) != "" {
				continue
			}
			out = append(out, Orphan{Path: rel, Group: owner[dir], Dir: dir})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// excludedByAny reports whether any active group's excludes cover this path.
func (s *Scanner) excludedByAny(rel string) bool {
	for _, g := range s.m.Dotfiles {
		if !g.Applies(s.host) {
			continue
		}
		if s.excluded(g, rel) {
			return true
		}
	}
	return false
}
