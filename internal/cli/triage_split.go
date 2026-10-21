package cli

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/split"
	"github.com/keyolk/tuikit/toast"
)

// The list | paths boundary is resizable (tuikit split). body draws both
// panes in the rects triPanes returns, and the mouse and the drag selection
// use the same rects.
//
// Keys are the kit's `>` grow, `<` shrink, `=` reset, in the list context;
// none of them is taken there. The ratio survives a restart in
// $XDG_STATE_HOME/dots/ui.json.

// stateApp names the state file: $XDG_STATE_HOME/dots/ui.json.
const stateApp = "dots"

// triUIState is ui.json. Field names are the file's contract.
type triUIState struct {
	TriageSplit split.State `json:"triage_split"`
}

func (m triModel) uiState() triUIState {
	return triUIState{TriageSplit: m.paneSplit().State()}
}

// restoreUI applies saved state. Restore rejects a ratio that is not a
// fraction (missing, a percentage, NaN) and keeps the default.
func (m triModel) restoreUI(st triUIState) triModel {
	m.split, _ = m.paneSplit().Restore(st.TriageSplit)
	return m
}

// paneSplit is m.split configured. The configuration is applied on every use,
// so a test's model lays out like the program. What the fixed layout drew is
// the default: the list three fifths, the paths the rest, ` │ ` between, and
// no paths pane below detailMinWidth. The list keeps room for a 20-cell
// pattern beside its fixed columns; the paths pane keeps 20 cells.
func (m triModel) paneSplit() split.Model {
	s := m.split
	s.Orientation = split.Horizontal
	s.Divider = 3
	s.Collapse = detailMinWidth
	s.Primary = split.A
	s.MinA, s.MinB = 40, 20
	s.Default = 0.6
	return s
}

// bodyRect is where body draws: below the header and the band.
func (m triModel) bodyRect() mouse.Rect {
	return mouse.Rect{X: 0, Y: m.bodyTop(), W: m.w, H: m.bodyHeight()}
}

// splitShown reports that the list and the paths pane are both on screen.
func (m triModel) splitShown() bool {
	return m.loaded && m.list.Len() > 0 && !m.paneSplit().Collapsed(m.bodyRect())
}

// triPanes is the list, the divider and the paths pane. The paths rect is
// zero while the split is collapsed.
func (m triModel) triPanes() (list, div, detail mouse.Rect) {
	return m.paneSplit().Layout(m.bodyRect())
}

// splitBindings are the kit's keys in the list context. The footer names
// them last, as one hint, only while both panes show (offered).
func splitBindings() []keys.Binding {
	// Shrink first, so the grouped hint reads "<> split", left key first.
	order := map[string]int{split.ActionShrink: 0, split.ActionGrow: 1, split.ActionReset: 2}
	bs := split.Bindings(ctxList)
	slices.SortFunc(bs, func(a, b keys.Binding) int { return order[a.Action] - order[b.Action] })
	for i := range bs {
		bs[i].Group = "Layout"
		bs[i].Help = map[string]string{
			split.ActionShrink: "narrow the list (the paths pane grows)",
			split.ActionGrow:   "widen the list (the paths pane shrinks)",
			split.ActionReset:  "reset the list / paths split",
		}[bs[i].Action]
		if bs[i].Action != split.ActionReset {
			bs[i].Footer, bs[i].FooterGroup = 6, "split"
		}
	}
	return bs
}

// runSplit moves the boundary by one key step, or says why it cannot.
func (m triModel) runSplit(action string) (tea.Model, tea.Cmd) {
	if !m.splitShown() {
		return m, m.push(toast.Toast{Source: "split", Level: toast.Info,
			Text: "the paths pane shows from 110 columns"})
	}
	var r split.Result
	m.split, r = m.paneSplit().Handle(action, m.bodyRect())
	if !r.Changed {
		return m, m.push(toast.Toast{Source: "split", Level: toast.Info, Text: "the split is at its limit"})
	}
	var cmd tea.Cmd
	m.saver, cmd = m.saver.Save(m.uiState())
	return m, cmd
}

// splitMouse gives a mouse event to the split before the selection and the
// click handling (the kit's order); consumed is a press on the divider, the
// drag that follows, its release. A dialog owns the mouse, so the split
// sees nothing while one is open.
func (m triModel) splitMouse(msg tea.MouseMsg) (triModel, tea.Cmd, bool) {
	if (m.mode != modeList && m.mode != modeInput) || !m.splitShown() {
		return m, nil, false
	}
	var r split.Result
	m.split, r = m.paneSplit().Update(msg, m.bodyRect())
	if r.Consumed && mouse.IsLeftPress(msg) {
		// The selection never sees this press, so it would keep its
		// highlight over cells the resize is about to move.
		m.drag.Clear()
	}
	var cmd tea.Cmd
	if r.Changed {
		m.saver, cmd = m.saver.Save(m.uiState())
	}
	return m, cmd, r.Consumed
}

// quit writes the latest UI state and quits once it is on disk (savedMsg).
// A model without a Saver (tests) quits at once.
func (m triModel) quit() (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.saver, cmd = m.saver.FlushWith(m.uiState())
	return m, cmd
}

// savedMsg handles the Saver's messages.
func (m triModel) savedMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case prefs.SaveTick:
		var cmd tea.Cmd
		m.saver, cmd = m.saver.Update(msg)
		return m, cmd
	case prefs.Saved:
		if msg.Final {
			// On disk or not, quit: a failed write must not trap the user.
			return m, tea.Quit
		}
		if msg.Err != nil {
			return m, m.push(toast.Toast{Source: "prefs", Level: toast.Error, Text: "could not save the layout: " + msg.Err.Error()})
		}
	}
	return m, nil
}
