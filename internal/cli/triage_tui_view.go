package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/tuikit/input"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/render"
	"github.com/keyolk/tuikit/toast"
)

// detailMinWidth is where the paths pane docks beside the list. Below it the
// list keeps the whole width: a pattern cut to fit a pane is worse than no
// pane (E6, E9).
const detailMinWidth = 110

func verdictLabel(v verdict) string {
	switch v {
	case vDeclare:
		return "declare"
	case vIgnore:
		return "ignore"
	case vSkip:
		return "skip"
	}
	return "?"
}

// verdictStyle gives a verdict the colour the plain report gives it:
// declare is "new, not yet recorded", ignore is "not the point", ? is "needs
// a decision" (E10).
func (m triModel) verdictStyle(v verdict) lipgloss.Style {
	switch v {
	case vDeclare:
		return m.th.Info.Bold(true)
	case vIgnore:
		return m.th.Muted
	case vSkip:
		return m.th.Subtle
	}
	return m.th.Warn.Bold(true)
}

func (m triModel) unsureCount() int {
	n := 0
	for _, r := range m.rows {
		if r.verdict == vUnsure {
			n++
		}
	}
	return n
}

// band is the "needs you" row: drawn only while a ? row remains (E6).
func (m triModel) band() string {
	n := m.unsureCount()
	if !m.loaded || n == 0 {
		return ""
	}
	return m.th.Warn.Render(render.Truncate(fmt.Sprintf("%d pattern(s) need a decision: %s declare · %s ignore · %s skip",
		n, m.keyName(triageStacks[0], "row.declare"), m.keyName(triageStacks[0], "row.ignore"),
		m.keyName(triageStacks[0], "row.skip")), m.w))
}

func (m triModel) toastView() string { return toast.View(m.toasts, m.w, m.th.ToastStyles()) }

// bodyHeight is what is left for the list once every other line is placed;
// one function so Update (scroll offset) and View agree (E9).
func (m triModel) bodyHeight() int {
	h := m.h - 2 // header + footer
	if m.band() != "" {
		h--
	}
	if m.mode == modeInput {
		h--
	}
	if t := m.toastView(); t != "" {
		h -= strings.Count(t, "\n") + 1
	}
	return max(h, 1)
}

// listRows is the rows the list draws entries in, after its two More lines.
func (m triModel) listRows() int { return max(m.bodyHeight()-2, 1) }

func (m triModel) header() string {
	files := 0
	for _, r := range m.rows {
		files += len(r.paths)
	}
	info := fmt.Sprintf(" · %d pattern(s) · %d file(s) · %s", len(m.rows), files, m.path)
	if m.filter != "" {
		info += " · /" + m.filter
	}
	if m.applying.Busy() {
		info += " · writing…"
	}
	return render.Truncate(m.th.Title.Render("dots triage")+m.th.Muted.Render(info), m.w)
}

func (m triModel) View() string {
	if m.w < 40 || m.h < 8 {
		return render.FitLines("terminal too small", m.w, m.h)
	}
	bodyH := m.bodyHeight()
	parts := []string{m.header()}
	if b := m.band(); b != "" {
		parts = append(parts, b)
	}
	parts = append(parts, m.body(bodyH))
	if m.mode == modeInput {
		parts = append(parts, m.inputLine())
	}
	if t := m.toastView(); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, m.km.Footer(m.triActive(), m.w, m.keyOpts(), m.th.FooterStyle()))
	frame := strings.Join(parts, "\n")

	box := m.overlayBox()
	if box == "" {
		return frame
	}
	return render.Center(render.Dim(frame), box, m.w, m.h) // over the list, not instead of it (E8)
}

// overlayBox is the dialog drawn over the list, or "" in the list and input
// modes. View and the mouse hit tests both read it, so a click lands on the
// box that was drawn.
func (m triModel) overlayBox() string {
	switch m.mode {
	case modeHelp:
		boxW, boxH := m.helpBox()
		cw, ch := render.BoxContent(boxW, boxH, m.th.BoxFocus)
		lines, _ := keys.RenderHelp(m.helpSections(), cw, ch, m.helpOff, m.th.HelpStyle())
		return render.Box("", strings.Join(lines, "\n"), boxW, boxH, m.th.BoxFocus)
	case modeGroup:
		return m.groupDialog()
	case modeConfirm:
		return m.confirmDialog()
	}
	return ""
}

func (m triModel) body(h int) string {
	switch {
	case !m.loaded:
		return render.FitLines(m.th.Muted.Render("scanning…"), m.w, h)
	case len(m.rows) == 0:
		return render.FitLines(m.th.Success.Render("every file beside a tracked one is accounted for"), m.w, h)
	case m.list.Len() == 0:
		return render.FitLines(m.th.Muted.Render("no row matches /"+m.filter), m.w, h)
	}
	if m.w < detailMinWidth {
		return render.Safe("list", m.w, h, m.logf, func() string { return m.viewList(m.w, h) })
	}
	listW := m.listWidth()
	detailW := m.w - listW - 3
	list := render.Safe("list", listW, h, m.logf, func() string { return m.viewList(listW, h) })
	detail := render.Safe("detail", detailW, h, m.logf, func() string { return m.viewDetail(detailW, h) })
	sep := m.th.Subtle.Render(strings.TrimSuffix(strings.Repeat(" │ \n", h), "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, render.FitLines(list, listW, h), sep, detail)
}

// listWidth is the list pane's width: all of it below detailMinWidth, else
// the left three fifths beside the paths pane.
func (m triModel) listWidth() int {
	if m.w < detailMinWidth {
		return m.w
	}
	return m.w*3/5 - 1
}

// cols are the list's column widths in cells, measured over the rows shown.
type cols struct{ pattern, group, reason int }

const (
	cursorW  = 2
	verdictW = 7 // "declare"
	countW   = 5
)

func (m triModel) columns(vis []rowRef, w int) cols {
	var c cols
	for _, ref := range vis {
		c.group = max(c.group, render.Width(m.rows[ref.idx].group))
		c.reason = max(c.reason, render.Width(m.rows[ref.idx].reason))
	}
	c.group, c.reason = min(c.group, 14), min(c.reason, 30)
	fixed := cursorW + verdictW + 1 + countW + 1
	// The pattern is the point of the row; the reason goes first, then the
	// group, before the pattern drops under 20 cells.
	for _, drop := range []*int{nil, &c.reason, &c.group} {
		if drop != nil {
			*drop = 0
		}
		c.pattern = w - fixed - gap(c.group) - gap(c.reason)
		if c.pattern >= 20 {
			break
		}
	}
	c.pattern = max(c.pattern, 1)
	return c
}

func gap(w int) int {
	if w == 0 {
		return 0
	}
	return w + 1
}

func (m triModel) viewList(w, h int) string {
	start, end := m.list.Window()
	above, below := m.list.Hidden()
	top, bottom := render.More(above, below, h)
	vis := m.list.Items()
	c := m.columns(vis, w)
	cur := m.list.Index()
	lines := []string{m.th.Subtle.Render(top)}
	for p := start; p < end; p++ {
		lines = append(lines, m.rowLine(m.rows[vis[p].idx], w, c, p == cur))
	}
	return strings.Join(append(lines, m.th.Subtle.Render(bottom)), "\n")
}

// rowLine pads every cell in terminal cells, so a Hangul path
// does not push the next column (E7). The selected row is drawn plain and then
// highlighted whole, so the highlight is one band and not a patchwork.
func (m triModel) rowLine(r triRow, w int, c cols, selected bool) string {
	cell := func(st lipgloss.Style, s string, cw int) string {
		if cw == 0 {
			return ""
		}
		if selected {
			st = m.th.Text.UnsetForeground()
		}
		return st.Render(render.PadRight(render.Truncate(s, cw), cw)) + " "
	}
	lead := "  "
	if selected {
		lead = render.Pad(m.th.Glyphs.Cursor, cursorW)
	}
	line := lead +
		cell(m.verdictStyle(r.verdict), verdictLabel(r.verdict), verdictW) +
		cell(m.th.Muted, fmt.Sprintf("%*d", countW, len(r.paths)), countW) +
		cell(m.th.Text, r.pattern, c.pattern) +
		cell(m.th.Muted, r.group, c.group) +
		cell(m.th.Muted, r.reason, c.reason)
	line = render.Truncate(strings.TrimRight(line, " "), w)
	if selected {
		return m.th.Selected.Render(render.Pad(line, w))
	}
	return line
}

// viewDetail lists the real paths behind the selected pattern.
func (m triModel) viewDetail(w, h int) string {
	i, ok := m.sel()
	if !ok {
		return ""
	}
	r := m.rows[i]
	lines := []string{
		m.th.Accent.Render(render.Truncate(r.pattern, w)),
		m.verdictStyle(r.verdict).Render(verdictLabel(r.verdict)) +
			m.th.Muted.Render(render.Truncate(fmt.Sprintf(" · group %s · %d file(s)", r.group, len(r.paths)), w-verdictW)),
		m.th.Muted.Render(render.Truncate(r.reason, w)),
		"",
	}
	room := h - len(lines)
	shown := r.paths
	if len(shown) > room {
		shown = shown[:max(room-1, 0)]
	}
	for _, p := range shown {
		lines = append(lines, render.Truncate(p, w))
	}
	if _, bottom := render.More(0, len(r.paths)-len(shown), room); bottom != "" {
		lines = append(lines, m.th.Subtle.Render(bottom))
	}
	return render.FitLines(strings.Join(lines, "\n"), w, h)
}

func (m triModel) inputLine() string {
	prompt := "/"
	if m.inputFor == "edit" {
		prompt = "pattern: "
	}
	return m.th.Key.Render(prompt) + m.input.View(max(m.w-render.Width(prompt), 1), true, input.Style{Text: m.th.Text})
}

// helpBox is the help box's outer size: at most 64×22 and the frame less a
// margin, and no taller than the help needs, so a short table is a short box.
func (m triModel) helpBox() (int, int) {
	w, h := min(64, m.w-4), min(22, m.h-2)
	cw, ch := render.BoxContent(w, h, m.th.BoxFocus)
	if need := keys.HelpLineCount(m.helpSections(), cw); need < ch {
		h -= ch - need
	}
	return w, h
}

// helpSections documents the list first, then every dialog's keys: Help lists
// each action once however many stacks reach it.
func (m triModel) helpSections() []keys.Section {
	return m.km.Help(m.keyOpts(), triageStacks[0], triageStacks[2], triageStacks[3], triageStacks[4], triageStacks[1])
}

// clampHelp stores the offset RenderHelp would draw, so holding j does not
// grow it past the last page.
func (m triModel) clampHelp() int {
	if m.mode != modeHelp {
		return m.helpOff
	}
	boxW, boxH := m.helpBox()
	cw, ch := render.BoxContent(boxW, boxH, m.th.BoxFocus)
	_, off := keys.RenderHelp(m.helpSections(), cw, ch, m.helpOff, keys.HelpStyle{})
	return off
}

// groupRows is how many group names the dialog draws at once.
func (m triModel) groupRows() int { return max(m.h-10, 1) }

func (m triModel) groupDialog() string {
	i, _ := m.sel()
	innerW := min(48, m.w-8)
	lines := []string{
		m.th.Title.Render(render.Truncate("target group for", innerW)),
		render.Truncate(m.rows[i].pattern, innerW),
		"",
	}
	rows := m.groupRows()
	start, end, _ := render.Window(len(m.groups), rows, m.groupCur, 0)
	top, bottom := render.More(start, len(m.groups)-end, rows+2)
	if top != "" {
		lines = append(lines, m.th.Subtle.Render(top))
	}
	for gi := start; gi < end; gi++ {
		g := m.groups[gi]
		if gi == m.groupCur {
			lines = append(lines, m.th.Selected.Render(render.Pad(m.th.Glyphs.Cursor+" "+render.Truncate(g, innerW-2), innerW)))
			continue
		}
		lines = append(lines, "  "+render.Truncate(g, innerW-2))
	}
	if bottom != "" {
		lines = append(lines, m.th.Subtle.Render(bottom))
	}
	return m.th.BoxFocus.Render(strings.Join(lines, "\n"))
}

func (m triModel) confirmDialog() string {
	var inc, exc int
	for _, r := range m.rows {
		switch r.verdict {
		case vDeclare:
			inc++
		case vIgnore:
			exc++
		}
	}
	innerW := min(56, m.w-8)
	lines := []string{
		m.th.Title.Render(render.Truncate(fmt.Sprintf("write %d line(s) to the manifest?", inc+exc), innerW)),
		m.th.Muted.Render(render.Truncate(m.path, innerW)),
		"",
		fmt.Sprintf("%s include  %s exclude",
			m.verdictStyle(vDeclare).Render(fmt.Sprint(inc)), m.verdictStyle(vIgnore).Render(fmt.Sprint(exc))),
		"",
		m.th.Key.Render(m.keyName(triageStacks[3], "confirm.yes")) + " write   " +
			m.th.Key.Render(m.keyName(triageStacks[3], "confirm.no")) + " cancel",
	}
	return m.th.BoxFocus.Render(strings.Join(lines, "\n"))
}
