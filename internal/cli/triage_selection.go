package cli

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/selection"
	"github.com/keyolk/tuikit/toast"
)

// Drag selection, the way Claude Code's mod engine selects: a press picks the
// pane under it, a drag selects text inside that pane only (never the │
// divider, the More lines or the other pane), releasing copies, and the
// highlight stays until the next press or key. Mouse capture turned the
// terminal's own drag-select off, so without this a pattern or a path could
// not be copied out of triage.
//
// Selection never decides anything: the gestures that write stay on their
// keys (mouse.go), and a drag only reads the frame.
//
// Panes:
//   - the list's entry rows: List, so a second click stays what a click is
//     there (it moves the cursor; the list has no Enter action).
//   - the paths pane beside it: Text, read from the frame. It has no scroll
//     of its own (it cuts the paths to the room and says "… N more"), so
//     there is no whole content to hand over and a drag cannot scroll it.
//   - the filter/edit line: Text, read from the frame. A drag there selects
//     and nothing else; the input never sees the mouse.
//   - help and the confirm dialog: Text; the group dialog: List, since its
//     second click is group.pick. While one is open only it is selectable.

const (
	paneTriList   = "list"
	paneTriDetail = "detail"
	paneTriInput  = "input"
	paneTriDialog = "dialog"
)

// selectionPanes lists what a drag can select on the screen View draws, from
// the rects View and the mouse hit tests use.
func (m triModel) selectionPanes() []selection.Pane {
	if m.w < 40 || m.h < 8 {
		return nil
	}
	switch m.mode {
	case modeHelp, modeConfirm:
		return []selection.Pane{{ID: paneTriDialog, Kind: selection.Text, Rect: m.dialogInner()}}
	case modeGroup:
		return []selection.Pane{{ID: paneTriDialog, Kind: selection.List, Rect: m.dialogInner()}}
	}
	var panes []selection.Pane
	if pane := m.listPane(); pane.H > 0 {
		panes = append(panes, selection.Pane{ID: paneTriList, Kind: selection.List, Rect: m.listRect()})
		if m.w >= detailMinWidth {
			x := pane.X + pane.W + 3 // the " │ " divider body puts between them
			panes = append(panes, selection.Pane{ID: paneTriDetail, Kind: selection.Text,
				Rect: mouse.Rect{X: x, Y: pane.Y, W: max(m.w-x, 0), H: pane.H}})
		}
	}
	if m.mode == modeInput {
		panes = append(panes, selection.Pane{ID: paneTriInput, Kind: selection.Text,
			Rect: mouse.Rect{X: 0, Y: m.bodyTop() + m.bodyHeight(), W: m.w, H: 1}})
	}
	return panes
}

// dialogInner is the open dialog's box inside its border and padding: the
// frame of the box is never selected.
func (m triModel) dialogInner() mouse.Rect {
	return m.overlayRect().InsetStyle(m.th.BoxFocus)
}

// selectMouse feeds a mouse event (or an auto-scroll tick) to the selection
// before any click handling. handled means the selection consumed it and it
// is not also a click. No pane here hands over Lines, so a drag never
// scrolls a pane and Result.Scroll is never set; the tick is routed anyway
// so a pane that gains Lines scrolls without rewiring.
func (m triModel) selectMouse(msg tea.Msg) (triModel, tea.Cmd, bool) {
	// view is the frame before the highlight, so a highlight style never
	// reaches a copy. It is pure (a value receiver), so calling it from
	// Update changes nothing.
	r, cmd := m.drag.Update(msg, m.selectionPanes, m.view, m.clock())
	return m, cmd, r.Consumed
}

// noteCopied says how a copy went: its length, or why it failed. The error
// stays until read (E6): a copy that silently failed pastes the old
// clipboard.
func (m triModel) noteCopied(msg selection.CopiedMsg) (tea.Model, tea.Cmd) {
	lvl := toast.Success
	if msg.Err != nil {
		lvl = toast.Error
	}
	return m, m.push(toast.Toast{Source: "copy", Level: lvl, Text: msg.Note()})
}
