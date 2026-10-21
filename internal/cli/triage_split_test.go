package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/toast"
)

// quits runs cmd as Bubble Tea would and reports whether it ends the program:
// quitting writes ui.json first, and only the write's Saved{Final} quits.
func quits(t *testing.T, m triModel, cmd tea.Cmd) bool {
	t.Helper()
	for range 10 {
		if cmd == nil {
			return false
		}
		switch msg := cmd().(type) {
		case tea.QuitMsg:
			return true
		case prefs.Saved, prefs.SaveTick:
			var next tea.Model
			next, cmd = m.Update(msg)
			m = next.(triModel)
		default:
			return false
		}
	}
	return false
}

// Split tests read positions out of View(): the divider glyph, a row's text.

// dividerCol is the divider's column: the one whose every body row is `│`
// (`┃` while dragging). -1 when only the list shows.
func dividerCol(t *testing.T, m triModel) int {
	t.Helper()
	var grid [][]string
	for _, row := range strings.Split(ansi.Strip(m.View()), "\n") {
		var cells []string
		for _, r := range row {
			cells = append(cells, string(r))
			for range ansi.StringWidth(string(r)) - 1 {
				cells = append(cells, "")
			}
		}
		grid = append(grid, cells)
	}
	top, h := m.bodyTop(), m.bodyHeight()
	for x := range grid[top] {
		ok := true
		for y := top; y < top+h && ok; y++ {
			ok = x < len(grid[y]) && (grid[y][x] == "│" || grid[y][x] == "┃")
		}
		if ok {
			return x
		}
	}
	return -1
}

func mustCol(t *testing.T, m triModel) int {
	t.Helper()
	x := dividerCol(t, m)
	if x < 0 {
		t.Fatalf("no divider:\n%s", ansi.Strip(m.View()))
	}
	return x
}

func TestDividerDragMovesTheBoundary(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 140, 24)
	x := mustCol(t, m)
	_, rowY := at(t, m, "no rule matched") // the third list row, at the list's right end
	pathX, pathY := rightmost(t, m, ".claude/hooks/README.md")

	m = lpress(t, m, x, m.bodyTop()+2)
	if !strings.Contains(ansi.Strip(m.View()), "┃") {
		t.Error("a press on the divider did not start a drag (no heavy divider)")
	}
	m = lmotion(t, m, x-20, m.bodyTop()+5)
	if len(highlighted(m)) != 0 {
		t.Error("a divider drag selected text")
	}
	m = lrelease(t, m, x-20, m.bodyTop()+5)
	nx := mustCol(t, m)
	if nx != x-20 {
		t.Fatalf("divider at %d after dragging from %d by -20", nx, x)
	}
	if gx, gy := rightmost(t, m, ".claude/hooks/README.md"); gx != pathX-20 || gy != pathY {
		t.Errorf("paths pane text at %d,%d, want %d,%d", gx, gy, pathX-20, pathY)
	}

	// A cell that was the list's is the paths pane's now: a click there no
	// longer moves the list cursor.
	before := m.list.Index()
	m = lpress(t, m, nx+5, rowY)
	m = lrelease(t, m, nx+5, rowY)
	if m.list.Index() != before {
		t.Errorf("a click right of the moved divider moved the list cursor %d -> %d", before, m.list.Index())
	}
	m = lpress(t, m, 4, rowY)
	if m.list.Index() == before {
		t.Error("control: a click on the list row did not move the cursor")
	}
	for _, p := range m.selectionPanes() {
		if p.ID == paneTriDetail && p.Rect.X != nx+2 {
			t.Errorf("paths selection pane starts at %d, divider at %d", p.Rect.X, nx)
		}
	}
}

func TestPanePressStillSelectsAndDividerPressNever(t *testing.T) {
	copies := captureCopies(t)
	m := startTUI(t, hooksEnv(t), 140, 24)
	pathX, pathY := rightmost(t, m, ".claude/hooks/README.md")
	m = dragFrom(t, m, pathX, pathY, pathX+12, pathY)
	if got := lastCopy(copies); !strings.Contains(got, ".claude/hook") {
		t.Errorf("a drag in the paths pane copied %q", got)
	}
	if len(highlighted(m)) == 0 {
		t.Fatal("setup: the copied text is not highlighted")
	}
	x := mustCol(t, m)
	m = lpress(t, m, x, pathY)
	if len(highlighted(m)) != 0 || m.drag.Active() {
		t.Error("a divider press left the old selection on screen")
	}
	m = lmotion(t, m, x-4, pathY+3)
	if len(highlighted(m)) != 0 || m.drag.Active() {
		t.Error("a divider drag started a selection")
	}
}

// The click handler takes any press as reading a sticky error; a divider
// press is the split's alone and never reaches it.
func TestDividerPressNeverReachesTheClickHandler(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 140, 24)
	t0 := time.Now()
	m.toasts = toast.WithClock(m.toasts, func() time.Time { return t0 })
	m.toasts, _ = toast.Push(m.toasts, toast.Toast{Source: "x", Level: toast.Error, Text: "boom"})
	m.toasts = toast.WithClock(m.toasts, func() time.Time { return t0.Add(time.Hour) })
	x := mustCol(t, m)
	m = lpress(t, m, x, m.bodyTop()+3)
	m = lrelease(t, m, x, m.bodyTop()+3)
	if len(toastTexts(m, toast.Error)) == 0 {
		t.Error("a divider press reached the click handler (it acknowledged the error)")
	}
	m = lpress(t, m, 4, m.bodyTop()+3)
	if len(toastTexts(m, toast.Error)) != 0 {
		t.Error("control: a list press did not acknowledge the error")
	}
}

func TestDoubleClickOnTheDividerResets(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 140, 24)
	x0 := mustCol(t, m)
	m, _ = press(t, m, "<")
	x := mustCol(t, m)
	if x == x0 {
		t.Fatal("< did not move the divider")
	}
	for range 2 {
		m = lpress(t, m, x, m.bodyTop()+1)
		m = lrelease(t, m, x, m.bodyTop()+1)
	}
	if got := mustCol(t, m); got != x0 {
		t.Errorf("double click left the divider at %d, want %d", got, x0)
	}
}

func TestSplitKeysMoveTheBoundary(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 140, 24)
	x0 := mustCol(t, m)
	m, _ = press(t, m, ">")
	x1 := mustCol(t, m)
	m, _ = press(t, m, "<")
	m, _ = press(t, m, "<")
	x2 := mustCol(t, m)
	m, _ = press(t, m, "=")
	x3 := mustCol(t, m)
	if !(x1 > x0 && x2 < x0 && x3 == x0) {
		t.Errorf("divider %d, > %d, << %d, = %d", x0, x1, x2, x3)
	}
	for _, c := range triageKeymap().Conflicts(triageStacks...) {
		t.Errorf("conflict: %s", c)
	}
}

func TestSplitKeysInFooterAndHelp(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 140, 24)
	if f := m.km.Footer(m.triActive(), 300, m.keyOpts(), m.th.FooterStyle()); !strings.Contains(f, "<> split") {
		t.Errorf("footer lacks the split hint: %q", f)
	}
	var help strings.Builder
	for _, s := range m.helpSections() {
		for _, e := range s.Entries {
			help.WriteString(e.Help + "\n")
		}
	}
	for _, want := range []string{"narrow the list", "widen the list", "reset the list / paths split"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	if f := m.km.Footer(m.triActive(), 300, m.keyOpts(), m.th.FooterStyle()); strings.Contains(f, "split") {
		t.Errorf("footer offers the split with no paths pane: %q", f)
	}
	m, _ = press(t, m, ">")
	if got := strings.Join(toastTexts(m, toast.Info), " "); !strings.Contains(got, "110 columns") {
		t.Errorf("> with no paths pane said %q", got)
	}
}

func TestGrowPastTheLimitThenShrinkMovesAtOnce(t *testing.T) {
	m := startTUI(t, hooksEnv(t), 140, 24)
	for range 40 {
		m, _ = press(t, m, ">")
	}
	far := mustCol(t, m)
	if got := strings.Join(toastTexts(m, toast.Info), " "); !strings.Contains(got, "limit") {
		t.Errorf("a key at the limit said %q", got)
	}
	if _, _, d := m.triPanes(); d.W < 20 {
		t.Errorf("paths pane %d wide at the limit, want >= 20", d.W)
	}
	m, _ = press(t, m, "<")
	if x := mustCol(t, m); x >= far {
		t.Errorf("one < after overshooting left the divider at %d (was %d): dead zone", x, far)
	}
}

func TestSplitRatioIsSavedAndRestored(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 140, 24)
	m.saver = prefs.NewSaver[triUIState](stateApp, 0)
	x0 := mustCol(t, m)
	for range 3 {
		var cmd tea.Cmd
		m, cmd = press(t, m, "<")
		if cmd == nil {
			t.Fatal("a split change scheduled no save")
		}
	}
	x := mustCol(t, m)
	m, cmd := press(t, m, "q")
	if !quits(t, m, cmd) {
		t.Fatal("q did not quit after writing the state")
	}
	st, err := prefs.Load(stateApp, triUIState{})
	if err != nil {
		t.Fatal(err)
	}
	fresh := startTUI(t, e, 140, 24).restoreUI(st)
	if got := mustCol(t, fresh); got != x || got == x0 {
		t.Errorf("restored divider at %d, saved at %d (default %d)", got, x, x0)
	}
}

func TestGarbageSplitRatioKeepsTheDefault(t *testing.T) {
	e := hooksEnv(t)
	x0 := mustCol(t, startTUI(t, e, 140, 24))
	path := prefs.Path(stateApp)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"triage_split":{"ratio":60}}`, `{"triage_split":{"ratio":1}}`, `{"triage_split":{}}`, `[]`} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		st, _ := prefs.Load(stateApp, triUIState{})
		if got := mustCol(t, startTUI(t, e, 140, 24).restoreUI(st)); got != x0 {
			t.Errorf("%s: divider at %d, want %d", raw, got, x0)
		}
	}
}

func TestSplitFitsNarrowTerminals(t *testing.T) {
	e := hooksEnv(t)
	for _, sz := range [][2]int{{80, 24}, {60, 24}, {109, 24}, {110, 24}, {220, 40}} {
		for _, k := range []string{">", "<"} {
			m := startTUI(t, e, sz[0], sz[1])
			for range 30 {
				m, _ = press(t, m, k)
			}
			rows := strings.Split(m.View(), "\n")
			if len(rows) != sz[1] {
				t.Errorf("%dx%d after %s: %d rows", sz[0], sz[1], k, len(rows))
			}
			for i, r := range rows {
				if w := ansi.StringWidth(r); w > sz[0] {
					t.Errorf("%dx%d after %s: row %d is %d wide", sz[0], sz[1], k, i, w)
				}
			}
			if shown := dividerCol(t, m) >= 0; shown != (sz[0] >= detailMinWidth) {
				t.Errorf("%dx%d: paths pane shown=%v, want only from %d columns", sz[0], sz[1], shown, detailMinWidth)
			}
		}
	}
}
