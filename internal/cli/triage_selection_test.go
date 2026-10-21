package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/tuikit/selection"
)

// Coordinates come from where View drew text (at, row), never from the pane
// arithmetic under test. The copy is captured through copySelection; the
// clipboard command is never run.

func captureCopies(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := copySelection
	copySelection = func(s string) tea.Cmd {
		got = append(got, s)
		return func() tea.Msg { return nil }
	}
	t.Cleanup(func() { copySelection = old })
	return &got
}

func lastCopy(c *[]string) string {
	if len(*c) == 0 {
		return ""
	}
	return (*c)[len(*c)-1]
}

// markSelection draws the highlight as upper case, so a test sees which
// cells it covers whatever the color profile.
func markSelection(t *testing.T) {
	t.Helper()
	old := selectionStyle
	selectionStyle = lipgloss.NewStyle().Transform(strings.ToUpper)
	t.Cleanup(func() { selectionStyle = old })
}

func lpress(t *testing.T, m triModel, x, y int) triModel {
	t.Helper()
	return send(t, m, mouseAt(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
}

func lmotion(t *testing.T, m triModel, x, y int) triModel {
	t.Helper()
	return send(t, m, mouseAt(x, y, tea.MouseButtonLeft, tea.MouseActionMotion))
}

func lrelease(t *testing.T, m triModel, x, y int) triModel {
	t.Helper()
	return send(t, m, mouseAt(x, y, tea.MouseButtonNone, tea.MouseActionRelease))
}

func dragFrom(t *testing.T, m triModel, x0, y0, x1, y1 int) triModel {
	t.Helper()
	return lrelease(t, lmotion(t, lpress(t, m, x0, y0), x1, y1), x1, y1)
}

func screenRow(m triModel, y int) string {
	return ansi.Strip(strings.Split(m.View(), "\n")[y])
}

// rightmost finds needle in the rendered View and returns its leftmost cell
// in the rightmost occurrence: the paths pane repeats patterns the list
// shows, and at resolves to the list.
func rightmost(t *testing.T, m triModel, needle string) (int, int) {
	t.Helper()
	bx, by := -1, -1
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.LastIndex(line, needle); i >= 0 {
			if x := ansi.StringWidth(line[:i]); x > bx {
				bx, by = x, y
			}
		}
	}
	if bx < 0 {
		t.Fatalf("%q is not on screen:\n%s", needle, ansi.Strip(m.View()))
	}
	return bx, by
}

func TestDragFromTheListTowardThePathsStaysInTheList(t *testing.T) {
	c := captureCopies(t)
	markSelection(t)
	m := startTUI(t, hooksEnv(t), 120, 20)
	x0, y0 := at(t, m, ".claude/hooks/*.log")
	_, y1 := at(t, m, ".claude/hooks/state")
	px, _ := rightmost(t, m, "README.md")
	m = dragFrom(t, m, x0, y0, px+4, y1)
	got := lastCopy(c)
	if !strings.HasPrefix(got, ".claude/hooks/*.log") || !strings.Contains(got, ".claude/hooks/state") {
		t.Fatalf("the copy should run over the list rows dragged across: %q", got)
	}
	if strings.Contains(got, "│") || strings.Contains(got, "file(s)") {
		t.Fatalf("the copy reached past the list into the divider or the paths pane: %q", got)
	}
	// The highlight is upper case: the list cells on the last row are, and
	// nothing right of the divider is.
	cols := strings.SplitN(screenRow(m, y1), "│", 2)
	if len(cols) < 2 || !strings.Contains(cols[0], ".CLAUDE/HOOKS/STATE") {
		t.Fatalf("the list cells on the last row should be highlighted: %q", screenRow(m, y1))
	}
	if cols[1] != strings.ToLower(cols[1]) {
		t.Fatalf("the highlight reached into the paths pane: %q", screenRow(m, y1))
	}
}

func TestPressWithoutMotionIsStillAClick(t *testing.T) {
	c := captureCopies(t)
	t0 := time.Unix(0, 0)
	m := withClock(startTUI(t, hooksEnv(t), 120, 20), &t0)
	x, y := at(t, m, ".claude/hooks/state")
	m = lrelease(t, lpress(t, m, x+1, y), x+1, y)
	if got := m.list.SelectedID(); got != ".claude/hooks/state" {
		t.Fatalf("a press without motion should select the row, selected %q", got)
	}
	// The list is a List pane: a second click is a click again, not a word.
	m = lrelease(t, lpress(t, m, x+1, y), x+1, y)
	if len(*c) != 0 || m.drag.Active() {
		t.Fatalf("clicks on a list row selected text: %q", lastCopy(c))
	}
	if got := m.list.SelectedID(); got != ".claude/hooks/state" {
		t.Fatalf("a second click moved the selection to %q", got)
	}
}

func TestDoubleAndTripleClickInThePathsPane(t *testing.T) {
	c := captureCopies(t)
	t0 := time.Unix(0, 0)
	m := withClock(startTUI(t, hooksEnv(t), 120, 20), &t0)
	x, y := rightmost(t, m, ".claude/hooks/README.md")
	for range 2 {
		m = lrelease(t, lpress(t, m, x+3, y), x+3, y)
	}
	if got := lastCopy(c); got != ".claude/hooks/README.md" {
		t.Fatalf("a double click should copy the path under it, got %q", got)
	}
	_, gy := rightmost(t, m, "group claude")
	gx := strings.Index(screenRow(m, gy), "│") + 2 // the paths pane's first cell
	for range 3 {
		m = lrelease(t, lpress(t, m, gx+1, gy), gx+1, gy)
	}
	if got := lastCopy(c); got != "declare · group claude · 1 file(s)" {
		t.Fatalf("a triple click should copy the line, got %q", got)
	}
}

func TestAKeyClearsTheSelection(t *testing.T) {
	captureCopies(t)
	m := startTUI(t, hooksEnv(t), 120, 20)
	x0, y0 := at(t, m, ".claude/hooks/*.md")
	_, y1 := at(t, m, ".claude/hooks/state")
	m = dragFrom(t, m, x0, y0, x0+5, y1)
	if !m.drag.Active() {
		t.Fatal("setup: the drag did not leave a selection")
	}
	m, _ = press(t, m, "k")
	if m.drag.Active() {
		t.Fatal("a key should clear the selection")
	}
}

func TestHelpOpenLeavesTheBaseUnselectable(t *testing.T) {
	c := captureCopies(t)
	m := startTUI(t, hooksEnv(t), 120, 20)
	m, _ = press(t, m, "?")
	// The list's verdict column is left of the centred help box.
	x, y := at(t, m, "ignore")
	if x > 10 {
		t.Fatalf("setup: the list should still show beside help at %d,%d:\n%s", x, y, ansi.Strip(m.View()))
	}
	got := dragFrom(t, m, x, y, x+4, y+1)
	if got.drag.Active() || len(*c) != 0 {
		t.Fatalf("a drag on the list under help selected %q", lastCopy(c))
	}
	hx, hy := at(t, m, "page down")
	got = dragFrom(t, m, hx, hy, hx+8, hy)
	if s := lastCopy(c); s != "page down" {
		t.Fatalf("a drag in help selects its text only, got %q", s)
	}
	if got.mode != modeHelp {
		t.Fatal("a drag inside help must not close it")
	}
	// From inside the box out past its left edge: the border is not taken.
	got = dragFrom(t, m, hx+8, hy, 0, hy)
	if s := lastCopy(c); strings.Contains(s, "│") || !strings.HasSuffix(s, "page down") {
		t.Fatalf("a drag out of help took its border: %q", s)
	}
}

func TestDragInTheConfirmDialogNeverWrites(t *testing.T) {
	c := captureCopies(t)
	e := hooksEnv(t)
	before := manifestBytes(t, e)
	m := startTUI(t, e, 120, 20)
	m, _ = press(t, m, "a")
	if m.mode != modeConfirm {
		t.Fatal("setup: the confirm dialog is not open")
	}
	x, y := at(t, m, "line(s) to the manifest")
	m = dragFrom(t, m, x, y, x+6, y)
	if got := lastCopy(c); got != "line(s)" {
		t.Fatalf("copy = %q", got)
	}
	if m.mode != modeConfirm || manifestBytes(t, e) != before || m.applying.Busy() {
		t.Fatal("a drag in the confirm dialog must not answer it")
	}
}

func TestDragOverTheFilterLineNeverTypes(t *testing.T) {
	c := captureCopies(t)
	m := startTUI(t, hooksEnv(t), 120, 20)
	m, _ = press(t, m, "/")
	for _, k := range "hooks" {
		m, _ = press(t, m, string(k))
	}
	// The filter line is the row that is the filter alone; "/hooks" also
	// appears inside the paths.
	x, y := -1, -1
	for i, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.TrimSpace(l) == "/hooks" {
			x, y = strings.Index(l, "/hooks"), i
		}
	}
	if y < 0 {
		t.Fatalf("setup: the filter line is not on screen:\n%s", ansi.Strip(m.View()))
	}
	m = dragFrom(t, m, x, y, x+5, y)
	if got := lastCopy(c); got != "/hooks" {
		t.Fatalf("copy = %q", got)
	}
	if m.mode != modeInput || m.filter != "hooks" || m.input.Value() != "hooks" {
		t.Fatalf("a drag over the filter line changed it: mode %v, filter %q", m.mode, m.filter)
	}
}

func TestGroupDialogSecondClickStillPicks(t *testing.T) {
	c := captureCopies(t)
	t0 := time.Unix(0, 0)
	m := withClock(startTUI(t, hooksEnv(t), 120, 20), &t0)
	m, _ = press(t, m, "g")
	if m.mode != modeGroup || len(m.groups) == 0 {
		t.Fatal("setup: the group dialog is not open")
	}
	want := m.groups[m.groupCur]
	x, y := rightmost(t, m, "▸ "+want)
	for range 2 {
		m = lrelease(t, lpress(t, m, x+3, y), x+3, y)
	}
	if m.mode != modeList || len(*c) != 0 {
		t.Fatalf("the second click on the current group should pick it (mode %v, copied %q)", m.mode, lastCopy(c))
	}
}

func TestCopiedMsgShowsAToast(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 120, 20)
	m = send(t, m, selection.CopiedMsg{Text: "héllo"})
	if v := ansi.Strip(m.View()); !strings.Contains(v, "copied 5 chars") {
		t.Fatalf("no copied toast:\n%s", v)
	}
	m = send(t, m, selection.CopiedMsg{Text: "x", Err: errors.New("no clipboard")})
	if v := ansi.Strip(m.View()); !strings.Contains(v, "copy failed: no clipboard") {
		t.Fatalf("no copy error toast:\n%s", v)
	}
}

// The copy is a command Update returns; building it is not enough. The
// returned command is the swapped fake and is never run.
func TestReleaseReturnsTheCopyCommand(t *testing.T) {
	captureCopies(t)
	m := startTUI(t, hooksEnv(t), 120, 20)
	x0, y0 := at(t, m, ".claude/hooks/*.md")
	m = lmotion(t, lpress(t, m, x0, y0), x0+6, y0)
	if _, cmd := m.Update(mouseAt(x0+6, y0, tea.MouseButtonNone, tea.MouseActionRelease)); cmd == nil {
		t.Fatal("the release that ends a drag should return the copy command")
	}
}
