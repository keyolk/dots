package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Clicks aim at coordinates found in the rendered View, never at numbers
// recomputed with the handler's own arithmetic.

// at finds needle in the rendered View and returns its leftmost cell: the
// list is the left pane, so a pattern the detail pane repeats resolves to its
// list row.
func at(t *testing.T, m triModel, needle string) (int, int) {
	t.Helper()
	bx, by := -1, -1
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(line, needle); i >= 0 {
			if x := ansi.StringWidth(line[:i]); bx < 0 || x < bx {
				bx, by = x, y
			}
		}
	}
	if bx < 0 {
		t.Fatalf("%q is not on screen:\n%s", needle, ansi.Strip(m.View()))
	}
	return bx, by
}

func mouseAt(x, y int, b tea.MouseButton, a tea.MouseAction) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: b, Action: a, Type: tea.MouseLeft}
}

func clickAt(t *testing.T, m triModel, x, y int) triModel {
	t.Helper()
	return send(t, m, mouseAt(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
}

func clickOn(t *testing.T, m triModel, needle string) triModel {
	t.Helper()
	x, y := at(t, m, needle)
	return clickAt(t, m, x+1, y)
}

func wheel(t *testing.T, m triModel, x, y int, b tea.MouseButton) triModel {
	t.Helper()
	return send(t, m, mouseAt(x, y, b, tea.MouseActionPress))
}

func withClock(m triModel, t0 *time.Time) triModel {
	m.now = func() time.Time { return *t0 }
	return m
}

func manifestBytes(t *testing.T, e *env) string {
	t.Helper()
	b, err := os.ReadFile(e.manifest)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMouseClickSelectsRowNeverDecides(t *testing.T) {
	e := hooksEnv(t)
	before := manifestBytes(t, e)
	m := startTUI(t, e, 120, 20)
	verdicts := fmt.Sprint(m.rows)

	m = clickOn(t, m, ".claude/hooks/state")
	if got := m.list.SelectedID(); got != ".claude/hooks/state" {
		t.Fatalf("click on state selected %q", got)
	}
	// A second and third click are still only selection: no write, no verdict.
	m = clickOn(t, m, ".claude/hooks/state")
	m = clickOn(t, m, ".claude/hooks/state")
	if got := fmt.Sprint(m.rows); got != verdicts {
		t.Fatalf("clicks changed verdicts:\n%s\n%s", verdicts, got)
	}
	if m.mode != modeList || m.applying.Busy() {
		t.Fatalf("clicks left the list mode or started a write: mode %v", m.mode)
	}
	if manifestBytes(t, e) != before {
		t.Fatal("a click wrote the manifest")
	}
	// Clicking the top row selects it again, going upward.
	m = clickOn(t, m, ".claude/hooks/*.md")
	if got := m.list.SelectedID(); got != ".claude/hooks/*.md" {
		t.Fatalf("click on *.md selected %q", got)
	}
}

func TestMouseSelectionIsByIDAcrossScroll(t *testing.T) {
	e := hooksEnv(t)
	for _, ext := range []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh"} {
		e.write(".claude/hooks/f."+ext, "x")
	}
	m := startTUI(t, e, 120, 12)
	for range 8 {
		m, _ = press(t, m, "j")
	}
	start, _ := m.list.Window()
	if start == 0 {
		t.Fatal("the list did not scroll; the test cannot prove offsets")
	}
	// Click the row drawn on the first visible line.
	first := m.rows[m.list.Items()[start].idx].pattern
	m = clickOn(t, m, first)
	if got := m.list.SelectedID(); got != m.rows[m.list.Items()[start].idx].id {
		t.Fatalf("click on %q (offset %d) selected %q", first, start, got)
	}
}

func TestMouseWheelMovesSelectionThree(t *testing.T) {
	e := hooksEnv(t)
	for _, ext := range []string{"aa", "bb", "cc", "dd", "ee"} {
		e.write(".claude/hooks/f."+ext, "x")
	}
	m := startTUI(t, e, 120, 20)
	x, y := at(t, m, ".claude/hooks/*.md")
	m = wheel(t, m, x, y, tea.MouseButtonWheelDown)
	if got := m.list.Index(); got != 3 {
		t.Fatalf("wheel down moved to %d, want 3", got)
	}
	m = wheel(t, m, x, y, tea.MouseButtonWheelUp)
	if got := m.list.Index(); got != 0 {
		t.Fatalf("wheel up moved to %d, want 0", got)
	}
	// The detail pane is not the list: the wheel there does nothing.
	dx, dy := at(t, m, "· group")
	m = wheel(t, m, dx, dy, tea.MouseButtonWheelDown)
	if got := m.list.Index(); got != 0 {
		t.Fatalf("wheel over the detail pane moved the list to %d", got)
	}
}

func TestMouseNarrowLayout(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 60, 20)
	if strings.Contains(ansi.Strip(m.View()), "│") {
		t.Fatal("expected the narrow layout without a detail pane")
	}
	m = clickOn(t, m, ".claude/hooks/*.log")
	if got := m.list.SelectedID(); got != ".claude/hooks/*.log" {
		t.Fatalf("narrow click selected %q", got)
	}
}

func TestMouseIgnoresReleaseMotionAndOtherButtons(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 120, 20)
	x, y := at(t, m, ".claude/hooks/state")
	for _, msg := range []tea.MouseMsg{
		mouseAt(x+1, y, tea.MouseButtonLeft, tea.MouseActionRelease),
		mouseAt(x+1, y, tea.MouseButtonLeft, tea.MouseActionMotion),
		mouseAt(x+1, y, tea.MouseButtonNone, tea.MouseActionMotion),
		mouseAt(x+1, y, tea.MouseButtonRight, tea.MouseActionPress),
		mouseAt(x+1, y, tea.MouseButtonMiddle, tea.MouseActionPress),
	} {
		m = send(t, m, msg)
		if got := m.list.Index(); got != 0 {
			t.Fatalf("%+v moved the cursor to %d", msg, got)
		}
	}
}

func TestMouseEmptyAreaDoesNothing(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 120, 20)
	_, y := at(t, m, ".claude/hooks/state")
	for _, p := range [][2]int{{5, y + 3}, {5, 0}, {5, 19}, {118, y}} {
		m = clickAt(t, m, p[0], p[1])
	}
	if got := m.list.Index(); got != 0 {
		t.Fatalf("an empty-area click moved the cursor to %d", got)
	}
	// The paths pane is not a list either.
	dx, dy := at(t, m, "· group")
	m = clickAt(t, m, dx, dy)
	if m.list.Index() != 0 {
		t.Fatal("a click on the detail pane moved the cursor")
	}
}

func TestMouseHelpOwnsTheMouse(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 120, 14)
	m, _ = press(t, m, "?")
	x, y := at(t, m, "include in the group")
	if x == 0 && y == 0 {
		t.Fatal("help is not on screen")
	}
	// Inside the box: no effect on the base; the box stays.
	m = clickAt(t, m, x, y)
	if m.mode != modeHelp || m.list.Index() != 0 {
		t.Fatalf("an inside click changed mode %v / cursor %d", m.mode, m.list.Index())
	}
	// A click aimed at a list row under the dimmed frame does not select it
	// and, being outside the box, closes help like esc.
	m = clickAt(t, m, 0, 3)
	if m.mode != modeList || m.list.Index() != 0 {
		t.Fatalf("outside click: mode %v cursor %d", m.mode, m.list.Index())
	}
	// The wheel scrolls the help, not the list.
	m, _ = press(t, m, "?")
	m = wheel(t, m, 60, 7, tea.MouseButtonWheelDown)
	if m.list.Index() != 0 {
		t.Fatal("the wheel under help moved the list")
	}
	if m.helpOff == 0 {
		t.Fatal("the wheel did not scroll the help")
	}
}

func TestMouseConfirmOnlyCancels(t *testing.T) {
	e := hooksEnv(t)
	before := manifestBytes(t, e)
	m := startTUI(t, e, 120, 20)
	m, _ = press(t, m, "a")
	if m.mode != modeConfirm {
		t.Fatalf("apply did not ask; mode %v", m.mode)
	}
	x, y := at(t, m, "include")
	// Clicking the "write" word, even twice, must not write.
	m = clickAt(t, m, x+1, y)
	m = clickAt(t, m, x+1, y)
	m = send(t, m, mouseAt(x+1, y, tea.MouseButtonRight, tea.MouseActionPress))
	if m.mode != modeConfirm || m.applying.Busy() {
		t.Fatalf("a click inside confirm changed state: mode %v busy %v", m.mode, m.applying.Busy())
	}
	m = clickAt(t, m, 0, 1)
	if m.mode != modeList || m.applying.Busy() {
		t.Fatalf("an outside click: mode %v busy %v", m.mode, m.applying.Busy())
	}
	if manifestBytes(t, e) != before {
		t.Fatal("the manifest changed without y")
	}
}

func TestMouseGroupDialog(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 120, 20)
	m.groups = []string{"claude", "shell", "editor"}
	t0 := time.Unix(1000, 0)
	m = withClock(m, &t0)
	m, _ = press(t, m, "g")
	if m.mode != modeGroup {
		t.Fatalf("g did not open the dialog; mode %v", m.mode)
	}
	// Click a row: the cursor moves, nothing is picked.
	m = clickOn(t, m, "editor")
	if m.groupCur != 2 || m.mode != modeGroup {
		t.Fatalf("click: groupCur %d mode %v", m.groupCur, m.mode)
	}
	if g := m.rows[0].group; g != "claude" {
		t.Fatalf("a single click picked group %q", g)
	}
	// Second click inside the window picks, as enter does.
	t0 = t0.Add(100 * time.Millisecond)
	m = clickOn(t, m, "editor")
	if m.mode != modeList || m.rows[0].group != "editor" || !m.rows[0].picked {
		t.Fatalf("double click: mode %v group %q", m.mode, m.rows[0].group)
	}
	// A click outside cancels and keeps the group.
	m, _ = press(t, m, "g")
	m = clickAt(t, m, 0, 1)
	if m.mode != modeList || m.rows[0].group != "editor" {
		t.Fatalf("outside click: mode %v group %q", m.mode, m.rows[0].group)
	}
	// The wheel moves the dialog cursor, not the list.
	m, _ = press(t, m, "g")
	x, y := at(t, m, "shell")
	m = wheel(t, m, x, y, tea.MouseButtonWheelUp)
	if m.groupCur != 0 || m.list.Index() != 0 {
		t.Fatalf("wheel: groupCur %d list %d", m.groupCur, m.list.Index())
	}
}

func TestMouseInputIgnoresClicks(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 120, 20)
	m, _ = press(t, m, "/")
	m = typeText(t, m, "log")
	x, y := at(t, m, ".claude/hooks/*.log")
	m = clickAt(t, m, x+1, y)
	m = wheel(t, m, x, y, tea.MouseButtonWheelDown)
	if m.mode != modeInput || m.filter != "log" {
		t.Fatalf("clicks disturbed the input: mode %v filter %q", m.mode, m.filter)
	}
}

func TestMouseTooSmallIsIgnored(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 30, 6)
	m = clickAt(t, m, 3, 2)
	if m.list.Index() != 0 {
		t.Fatal("a click on the too-small screen moved the cursor")
	}
}

func TestMouseKeyBetweenClicksBreaksDouble(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 120, 20)
	m.groups = []string{"claude", "shell", "editor"}
	t0 := time.Unix(1000, 0)
	m = withClock(m, &t0)
	m, _ = press(t, m, "g")
	m = clickOn(t, m, "editor")
	m, _ = press(t, m, "k")
	m, _ = press(t, m, "j")
	t0 = t0.Add(50 * time.Millisecond)
	m = clickOn(t, m, "editor")
	if m.mode != modeGroup || m.rows[0].picked {
		t.Fatalf("click, k, j, click counted as a double: mode %v", m.mode)
	}
}

// A line wider than its pane wraps in the terminal and shifts every row under
// it, which View strings never show; so check widths, with wide runes.
func TestMouseRowsNeverExceedPaneWidth(t *testing.T) {
	e := hooksEnv(t)
	long := strings.Repeat("긴이름", 30)
	e.write(".claude/hooks/"+long+".txt", "x")
	for _, w := range []int{60, 120} {
		m := startTUI(t, e, w, 20)
		m.groups = []string{long + long, "shell"}
		for _, key := range []string{"", "g", "esc", "a"} {
			if key != "" {
				m, _ = press(t, m, key)
			}
			// View clips an overlay to the frame and hides a too-wide box, so
			// measure the box itself, which the hit tests read.
			if r := m.overlayRect(); r.W > w {
				t.Fatalf("w=%d key %q: overlay is %d wide", w, key, r.W)
			}
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			if len(lines) > 20 {
				t.Fatalf("w=%d: %d lines for 20", w, len(lines))
			}
			for y, l := range lines {
				if got := ansi.StringWidth(l); got > w {
					t.Fatalf("w=%d key %q line %d is %d wide: %q", w, key, y, got, l)
				}
			}
		}
	}
}
