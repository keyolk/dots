package cli

import (
	"github.com/bmatcuk/doublestar/v4"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/manifest"
)

// triRow is one proposal on the triage screen, with the user's changes.
type triRow struct {
	// id is the pattern classify proposed. It never changes, so the cursor
	// (and a decision) stays on this row while the pattern is edited and
	// after a write rescans everything (E2).
	id      string
	pattern string // what a write records; e edits it
	group   string
	picked  bool // group came from g, not from the survey
	verdict verdict
	reason  string
	paths   []string
}

func (r triRow) decided() bool { return r.verdict == vDeclare || r.verdict == vIgnore }

// triData is one survey of the work tree.
type triData struct {
	rows     []triRow
	groups   []string // active groups, manifest order
	declared []string // every claimed path: what an exclude is checked against
	orphans  []dotfile.Orphan
	refused  []string // proposed excludes turned into "?" because they reach a tracked file
}

// scanTriage loads the manifest and classifies its orphans. It only reads.
func scanTriage(path, host string) (triData, error) {
	m, err := manifest.Load(path)
	if err != nil {
		return triData{}, err
	}
	sc := dotfile.NewScanner(m, host)
	orphans, err := sc.Orphans()
	if err != nil {
		return triData{}, err
	}
	declared, err := sc.Declared()
	if err != nil {
		return triData{}, err
	}
	d := triData{declared: declared, orphans: orphans}
	seen := map[string]bool{}
	for _, g := range m.Dotfiles {
		if g.Applies(host) && !seen[g.Name] {
			seen[g.Name] = true
			d.groups = append(d.groups, g.Name)
		}
	}
	for _, p := range classify(orphans) {
		r := triRow{id: p.pattern, pattern: p.pattern, group: p.group, verdict: p.verdict, reason: p.reason, paths: p.paths}
		// The plain report prints these as refused and writes nothing; here
		// they become a question, so the row is still there to edit.
		if _, hit := planEdit(p, declared); hit != "" {
			r.verdict = vUnsure
			r.reason = "would also exclude " + hit
			d.refused = append(d.refused, p.pattern)
		}
		d.rows = append(d.rows, r)
	}
	return d, nil
}

// applyTriage writes the decided rows through planEdit and manifest.Apply --
// the same edits `triage --apply` makes -- then rescans. Tracked paths are read
// again here rather than taken from the screen: the screen's copy may be
// minutes old.
func applyTriage(path, host string, rows []triRow) triApplyMsg {
	var out triApplyMsg
	m, err := manifest.Load(path)
	if err != nil {
		out.err = err
		return out
	}
	declared, err := dotfile.NewScanner(m, host).Declared()
	if err != nil {
		out.err = err
		return out
	}
	var edits []manifest.Edit
	for _, r := range rows {
		if !r.decided() {
			continue
		}
		e, hit := planEdit(proposal{pattern: r.pattern, group: r.group, verdict: r.verdict}, declared)
		if hit != "" {
			out.refused = append(out.refused, r.pattern+" (would also exclude "+hit+")")
			continue
		}
		edits = append(edits, e)
	}
	if out.written, out.err = manifest.Apply(m.Path, edits); out.err != nil {
		return out
	}
	out.data, out.scanErr = scanTriage(path, host)
	return out
}

// matching lists the orphans a pattern covers.
func matching(pattern string, orphans []dotfile.Orphan) []string {
	var out []string
	for _, o := range orphans {
		if ok, _ := doublestar.Match(pattern, o.Path); ok {
			out = append(out, o.Path)
		}
	}
	return out
}
