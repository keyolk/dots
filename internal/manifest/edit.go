package manifest

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Edit adds patterns to one group's include or exclude list, in the manifest's
// own text.
//
// The manifest is edited as text rather than decoded and re-encoded because
// every TOML library drops comments on the way out, and this manifest is
// mostly comments: the reason a path is excluded is the part worth keeping.
// A round-trip that silently deletes "# generated state, not config" turns a
// reviewed decision back into an unexplained line.
type Edit struct {
	Group   string
	Key     string // "include" or "exclude"
	Pattern string
}

// Apply writes the edits into the manifest file at path, preserving every byte
// it does not have to change. It reports how many lines it added.
func Apply(path string, edits []Edit) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	text := string(raw)
	n := 0
	for _, e := range edits {
		switch e.Key {
		case "include", "exclude":
		default:
			return 0, fmt.Errorf("unknown key %q", e.Key)
		}
		next, err := insert(text, e)
		if err != nil {
			return 0, err
		}
		text = next
		n++
	}
	if n == 0 {
		return 0, nil
	}
	// Decoding the result before it replaces the original: an edit that
	// produces unparseable TOML would otherwise leave the machine with a
	// manifest no dots command can load.
	var check Manifest
	if _, err := toml.Decode(text, &check); err != nil {
		return 0, fmt.Errorf("the edit produced an unreadable manifest, nothing written: %w", err)
	}
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode()
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		return 0, err
	}
	return n, nil
}

// insert places one pattern into one group's array.
func insert(text string, e Edit) (string, error) {
	lines := strings.Split(text, "\n")
	start, end := groupBounds(lines, e.Group)
	if start < 0 {
		return "", fmt.Errorf("no group named %q in the manifest", e.Group)
	}

	open, close := arrayBounds(lines, start, end, e.Key)
	entry := fmt.Sprintf("%q,", e.Pattern)

	if open < 0 {
		// The group has no such key yet. It goes right after the name line,
		// which is where every group in this manifest already puts it.
		at := start
		for i := start; i < end; i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "name") {
				at = i + 1
				break
			}
		}
		block := []string{e.Key + " = [", "  " + entry, "]"}
		out := append([]string{}, lines[:at]...)
		out = append(out, block...)
		out = append(out, lines[at:]...)
		return strings.Join(out, "\n"), nil
	}

	if open == close {
		// A one-line array becomes multi-line rather than growing sideways:
		// the manifest is read in review, and a 200-column line is not.
		head := lines[open][:strings.Index(lines[open], "[")+1]
		body := lines[open][strings.Index(lines[open], "[")+1 : strings.LastIndex(lines[open], "]")]
		tail := lines[open][strings.LastIndex(lines[open], "]"):]
		var block []string
		block = append(block, head)
		for _, item := range splitItems(body) {
			block = append(block, "  "+item+",")
		}
		block = append(block, "  "+entry, tail)
		out := append([]string{}, lines[:open]...)
		out = append(out, block...)
		out = append(out, lines[open+1:]...)
		return strings.Join(out, "\n"), nil
	}

	// Multi-line: the new entry goes just before the closing bracket, indented
	// like the line above it so the array keeps whatever style it had.
	indent := "  "
	if close > open+1 {
		prev := lines[close-1]
		indent = prev[:len(prev)-len(strings.TrimLeft(prev, " \t"))]
	}
	out := append([]string{}, lines[:close]...)
	out = append(out, indent+entry)
	out = append(out, lines[close:]...)
	return strings.Join(out, "\n"), nil
}

// groupBounds returns the line range of the [[dotfiles]] block whose name
// matches, as [start, end).
func groupBounds(lines []string, name string) (int, int) {
	want := fmt.Sprintf("%q", name)
	start := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "[[dotfiles]]" {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(t, "[") {
			start = -1 // a different table began before any name matched
			continue
		}
		if start >= 0 && strings.HasPrefix(t, "name") && strings.Contains(t, want) {
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
					return start, j
				}
			}
			return start, len(lines)
		}
	}
	return -1, -1
}

// arrayBounds finds the lines holding `key = [ ... ]` inside a group. Both
// results are the same line when the array fits on one.
func arrayBounds(lines []string, start, end int, key string) (int, int) {
	for i := start; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, key) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, key))
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		if !strings.Contains(rest, "[") {
			return -1, -1
		}
		depth := 0
		for j := i; j < end; j++ {
			depth += bracketDelta(lines[j])
			if depth == 0 {
				return i, j
			}
		}
		return -1, -1
	}
	return -1, -1
}

// bracketDelta counts brackets outside of string literals. A glob may contain
// them -- `.local/bin/[a-z]*` and `{argx,ccx}` both appear in real manifests --
// so counting them naively would mis-locate the end of the array.
func bracketDelta(line string) int {
	depth, inStr := 0, false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			if i == 0 || line[i-1] != '\\' {
				inStr = !inStr
			}
		case '#':
			if !inStr {
				return depth
			}
		case '[':
			if !inStr {
				depth++
			}
		case ']':
			if !inStr {
				depth--
			}
		}
	}
	return depth
}

// splitItems breaks a one-line array body into its quoted entries, ignoring
// commas that fall inside a glob's braces.
func splitItems(body string) []string {
	var out []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' && (i == 0 || body[i-1] != '\\') {
			inStr = !inStr
		}
		if c == ',' && !inStr {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
