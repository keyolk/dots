package cli

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/render"
	"github.com/keyolk/tuikit/toast"
)

// Mouse is an augmentation of the keyboard: every gesture resolves to an
// action from the binding table and runs through run, the function a key
// calls.
//
// Triage writes the manifest, so the rule here is stricter than elsewhere: a
// click only moves a cursor. Declare, ignore, skip, apply and confirm.yes stay
// on their keys; no gesture reaches them.
//
//   - list row: select it (list.down/up by the distance). The list has no
//     Enter action, so a second click does nothing more.
//   - wheel over the list: move the selection three rows.
//   - group dialog: a row click moves the cursor, a second click is
//     group.pick (what enter does; it sets the group in memory, writes
//     nothing); the wheel moves the cursor; a click outside is group.cancel.
//   - help: the wheel scrolls, a click outside is help.close.
//   - confirm: a click outside is confirm.no; a click inside does nothing,
//     since only y writes.
//   - the filter/edit input: ignored. A click never types or submits.
//   - a drag, in any mode, selects text inside the pane under the press
//     (triage_selection.go); a double or triple click in a text pane selects
//     a word or a line. Neither ever decides, types or writes.
func (m triModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.w < 40 || m.h < 8 {
		return m, nil
	}
	// The selection sees every event before any click handling, and before
	// a click dismisses a toast: the panes it hit-tests are the ones on the
	// frame the press landed on. A press that does not move stays the click
	// below.
	m, selCmd, consumed := m.selectMouse(msg)
	if consumed || m.mode == modeInput {
		return m, selCmd
	}
	next, cmd := m.clickMouse(msg)
	return next, tea.Batch(selCmd, cmd)
}

// clickMouse is the click and wheel handling for an event the selection
// left to the app.
func (m triModel) clickMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if mouse.IsLeftPress(msg) {
		m.toasts = toast.OnKey(m.toasts) // a click is a keystroke for toasts
	}
	switch m.mode {
	case modeHelp:
		return m.helpMouse(msg)
	case modeGroup:
		return m.groupMouse(msg)
	case modeConfirm:
		if mouse.IsLeftPress(msg) && !m.overlayRect().Contains(msg.X, msg.Y) {
			return m.run("confirm.no")
		}
		return m, nil
	}
	return m.listMouse(msg)
}

// steps runs action n times through run, the way n key presses would.
func (m triModel) steps(up, down string, d int) (triModel, tea.Cmd) {
	act := down
	if d < 0 {
		act, d = up, -d
	}
	var cmds []tea.Cmd
	for range d {
		next, c := m.run(act)
		m = next.(triModel)
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

func (m triModel) listMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	rect := m.listRect()
	if d := mouse.Delta(msg); d != 0 {
		if !m.listPane().Contains(msg.X, msg.Y) {
			return m, nil
		}
		return m.steps("list.up", "list.down", d)
	}
	if !mouse.IsLeftPress(msg) {
		return m, nil
	}
	hit := mouse.ListHit(m.list, rect, msg)
	if !hit.HasItem {
		return m, nil
	}
	return m.steps("list.up", "list.down", hit.Index-m.list.Index())
}

func (m triModel) groupMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if d := mouse.Delta(msg); d != 0 {
		return m.steps("group.up", "group.down", d)
	}
	if !mouse.IsLeftPress(msg) {
		return m, nil
	}
	box := m.overlayRect()
	if !box.Contains(msg.X, msg.Y) {
		return m.run("group.cancel")
	}
	gi := m.groupHit(msg)
	if gi < 0 {
		m.clicks = mouse.Clicks{}
		return m, nil
	}
	var double bool
	m.clicks, double = m.clicks.Press("group:"+m.groups[gi], m.clock())
	if double && gi == m.groupCur {
		return m.run("group.pick")
	}
	return m.steps("group.up", "group.down", gi-m.groupCur)
}

func (m triModel) helpMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if d := mouse.Delta(msg); d != 0 {
		return m.steps("help.up", "help.down", d)
	}
	if mouse.IsLeftPress(msg) && !m.overlayRect().Contains(msg.X, msg.Y) {
		return m.run("help.close")
	}
	return m, nil
}

// bodyTop is the first screen line of the body: below the header and, while
// a ? row remains, the band. It mirrors the order View joins its parts in.
func (m triModel) bodyTop() int {
	if m.band() != "" {
		return 2
	}
	return 1
}

// listPane is where body draws the list, More lines included.
func (m triModel) listPane() mouse.Rect {
	if !m.loaded || m.list.Len() == 0 {
		return mouse.Rect{}
	}
	return mouse.Rect{X: 0, Y: m.bodyTop(), W: m.listWidth(), H: m.bodyHeight()}
}

// listRect is the entry rows only: viewList draws one More line above them.
func (m triModel) listRect() mouse.Rect {
	pane := m.listPane()
	start, end := m.list.Window()
	return mouse.Rect{X: pane.X, Y: pane.Y + 1, W: pane.W, H: end - start}
}

// overlayRect is where Center puts the dialog box.
func (m triModel) overlayRect() mouse.Rect {
	box := m.overlayBox()
	if box == "" {
		return mouse.Rect{}
	}
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	return mouse.Rect{X: max(0, (m.w-bw)/2), Y: max(0, (m.h-bh)/2), W: bw, H: bh}
}

// groupHit maps a click inside the group dialog to an index into m.groups,
// or -1. Rows start below the border, padding, the title, the pattern, a
// blank line and, when names are scrolled off the top, the More line.
func (m triModel) groupHit(msg tea.MouseMsg) int {
	box := m.overlayRect()
	start, end, _ := render.Window(len(m.groups), m.groupRows(), m.groupCur, 0)
	top := box.Y + m.th.BoxFocus.GetBorderTopSize() + m.th.BoxFocus.GetPaddingTop() + 3
	if start > 0 {
		top++
	}
	area := mouse.Rect{X: box.X, Y: top, W: box.W, H: end - start}
	hit := mouse.RowHit(area, start, len(m.groups), msg)
	if !hit.HasItem {
		return -1
	}
	return hit.Index
}

func (m triModel) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}
