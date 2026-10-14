package cli

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/keyolk/tuikit/async"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/render"
	"github.com/keyolk/tuikit/theme"
	"github.com/keyolk/tuikit/toast"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/ui"
)

type (
	triScanMsg struct {
		gen  uint64
		data triData
		err  error
	}
	triApplyMsg struct {
		id      uint64
		written int
		refused []string
		data    triData
		err     error // nothing was written
		scanErr error // written, but the rescan failed
	}
)

type triMode int

const (
	modeList triMode = iota
	modeHelp
	modeGroup
	modeConfirm
	modeInput
)

type triModel struct {
	path, host string
	km         keys.Keymap
	th         theme.Theme
	toasts     toast.Queue
	logf       func(string, ...any)

	loaded   bool
	rows     []triRow
	groups   []string
	declared []string
	orphans  []dotfile.Orphan

	selID  string // the cursor (E2); the row index is derived
	slot   int    // last derived index, kept when selID vanishes
	offset int    // list scroll, kept here and not in View (E1)

	filter   string
	mode     triMode
	input    []rune
	inputFor string // "filter" or "edit"
	before   string // the filter to restore when its input is cancelled
	groupCur int
	helpOff  int

	scanGen  async.Gen
	applying async.Flight
	w, h     int
}

func newTriageModel(path, host string, km keys.Keymap, th theme.Theme, logf func(string, ...any)) triModel {
	m := triModel{path: path, host: host, km: km.Or(triageKeymap()), th: th, logf: logf, toasts: toast.New(3)}
	// Init cannot keep a change to the model, so the first scan's generation
	// is taken here and Init reads it.
	m.scanGen, _ = m.scanGen.Next()
	return m
}

func (m triModel) Init() tea.Cmd { return m.scanCmd(m.scanGen.Current()) }

func (m triModel) scanCmd(gen uint64) tea.Cmd {
	path, host := m.path, m.host
	return func() tea.Msg {
		d, err := scanTriage(path, host)
		return triScanMsg{gen: gen, data: d, err: err}
	}
}

// visible is the rows the filter keeps, as indices into m.rows.
func (m triModel) visible() []int {
	q := strings.ToLower(m.filter)
	var out []int
	for i, r := range m.rows {
		if q == "" || strings.Contains(strings.ToLower(r.pattern+" "+r.reason+" "+r.group), q) {
			out = append(out, i)
		}
	}
	return out
}

// cursor is the selected row's position among the visible rows.
func (m triModel) cursor(vis []int) int {
	for i, ri := range vis {
		if m.rows[ri].id == m.selID {
			return i
		}
	}
	return max(0, min(m.slot, len(vis)-1))
}

// sel is the index into m.rows of the selected row.
func (m triModel) sel() (int, bool) {
	vis := m.visible()
	if len(vis) == 0 {
		return 0, false
	}
	return vis[m.cursor(vis)], true
}

// reselect re-derives the cursor after the rows or the filter changed: the
// same ID if it is still there, else the same slot (E2).
func (m *triModel) reselect() {
	vis := m.visible()
	if len(vis) == 0 {
		return
	}
	m.slot = m.cursor(vis)
	m.selID = m.rows[vis[m.slot]].id
	_, _, m.offset = render.Window(len(vis), m.listRows(), m.slot, m.offset)
}

func (m *triModel) moveTo(pos int) {
	vis := m.visible()
	if len(vis) == 0 {
		return
	}
	m.slot = max(0, min(pos, len(vis)-1))
	m.selID = m.rows[vis[m.slot]].id
	_, _, m.offset = render.Window(len(vis), m.listRows(), m.slot, m.offset)
}

// install replaces the rows with a fresh survey. Decisions that did not reach
// the manifest -- a skip, an edited pattern, a picked group -- stay with
// their row's ID, so a write does not undo what it left alone.
func (m *triModel) install(d triData) {
	old := make(map[string]triRow, len(m.rows))
	for _, r := range m.rows {
		old[r.id] = r
	}
	for i, r := range d.rows {
		o, ok := old[r.id]
		if !ok {
			continue
		}
		if o.verdict == vSkip {
			d.rows[i].verdict = vSkip
		}
		if o.picked {
			d.rows[i].group, d.rows[i].picked = o.group, true
		}
		if o.pattern != o.id {
			d.rows[i].pattern = o.pattern
			d.rows[i].paths = matching(o.pattern, d.orphans)
		}
	}
	m.rows, m.groups, m.declared, m.orphans = d.rows, d.groups, d.declared, d.orphans
	m.loaded = true
	m.reselect()
}

func (m *triModel) push(t toast.Toast) tea.Cmd {
	var cmd tea.Cmd
	m.toasts, cmd = toast.Push(m.toasts, t)
	return cmd
}

func (m triModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.reselect()
		m.helpOff = m.clampHelp()
	case toast.Expired:
		m.toasts = toast.Update(m.toasts, msg)
	case triScanMsg:
		if !m.scanGen.Is(msg.gen) {
			return m, nil // superseded by a write's rescan (E5)
		}
		if msg.err != nil {
			// Keep whatever rows there were and say why (E4).
			return m, m.push(toast.Toast{Source: "scan", Text: "scan failed: " + msg.err.Error(), Level: toast.Error})
		}
		m.toasts = toast.Dismiss(m.toasts, "scan")
		m.install(msg.data)
		if n := len(msg.data.refused); n > 0 {
			return m, m.push(toast.Toast{Source: "refused", Level: toast.Warn,
				Text: fmt.Sprintf("%d proposed exclude(s) would also exclude a tracked file; left as ?", n)})
		}
	case triApplyMsg:
		m.applying = m.applying.Done(msg.id)
		if !m.applying.Is(msg.id) {
			return m, nil
		}
		return m.applied(msg)
	case tea.KeyMsg:
		m.toasts = toast.OnKey(m.toasts)
		if m.mode == modeInput {
			return m.inputKey(msg)
		}
		b, ok := m.km.Resolve(m.triActive(), msg, m.keyOpts())
		if !ok {
			return m, nil
		}
		return m.run(b.Action)
	}
	return m, nil
}

// applied reports a write: what landed as a success, what was refused as an
// error that stays until read (E6).
func (m triModel) applied(msg triApplyMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.push(toast.Toast{Source: "apply", Level: toast.Error, Text: "nothing written: " + msg.err.Error()})
	}
	var cmds []tea.Cmd
	text, level := fmt.Sprintf("added %d line(s) to %s", msg.written, m.path), toast.Success
	if msg.written == 0 {
		text, level = "nothing written", toast.Info
	}
	cmds = append(cmds, m.push(toast.Toast{Source: "apply", Level: level, Text: text}))
	if len(msg.refused) > 0 {
		cmds = append(cmds, m.push(toast.Toast{Source: "refused", Level: toast.Error,
			Text: "refused: " + strings.Join(msg.refused, "; ")}))
	}
	if msg.scanErr != nil {
		cmds = append(cmds, m.push(toast.Toast{Source: "scan", Level: toast.Error, Text: "rescan failed: " + msg.scanErr.Error()}))
	} else {
		m.install(msg.data)
	}
	return m, tea.Batch(cmds...)
}

func (m triModel) run(action string) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch action {
	case "quit":
		return m, tea.Quit
	case "help":
		m.mode, m.helpOff = modeHelp, 0
		m.toasts = toast.Hold(m.toasts) // toasts wait while a dialog is up (E6)
	case "help.close", "group.cancel", "confirm.no":
		m.mode = modeList
		m.toasts, cmd = toast.Release(m.toasts)
	case "help.down":
		m.helpOff++
		m.helpOff = m.clampHelp()
	case "help.up":
		m.helpOff = max(0, m.helpOff-1)
	case "list.down", "list.up", "list.pgdown", "list.pgup":
		step := map[string]int{"list.down": 1, "list.up": -1, "list.pgdown": m.listRows(), "list.pgup": -m.listRows()}[action]
		m.moveTo(m.cursor(m.visible()) + step)
	case "row.declare":
		cmd = m.decide(vDeclare)
	case "row.ignore":
		cmd = m.decide(vIgnore)
	case "row.skip":
		cmd = m.decide(vSkip)
	case "row.group":
		i, _ := m.sel()
		m.groupCur = 0
		for gi, g := range m.groups {
			if g == m.rows[i].group {
				m.groupCur = gi
			}
		}
		m.mode = modeGroup
		m.toasts = toast.Hold(m.toasts)
	case "group.down", "group.up":
		d := 1
		if action == "group.up" {
			d = -1
		}
		m.groupCur = max(0, min(m.groupCur+d, len(m.groups)-1))
	case "group.pick":
		if i, ok := m.sel(); ok && m.groupCur < len(m.groups) {
			m.rows[i].group, m.rows[i].picked = m.groups[m.groupCur], true
		}
		m.mode = modeList
		m.toasts, cmd = toast.Release(m.toasts)
	case "row.edit":
		i, _ := m.sel()
		m.mode, m.inputFor, m.input = modeInput, "edit", []rune(m.rows[i].pattern)
	case "filter":
		m.mode, m.inputFor, m.before, m.input = modeInput, "filter", m.filter, []rune(m.filter)
	case "filter.clear":
		m.filter = ""
		m.reselect()
	case "apply":
		if m.decidedCount() == 0 {
			return m, m.push(toast.Toast{Source: "apply", Level: toast.Warn, Text: "nothing decided to write"})
		}
		m.mode = modeConfirm
		m.toasts = toast.Hold(m.toasts)
	case "confirm.yes":
		return m.startApply()
	case "input.submit":
		return m.submitInput()
	case "input.cancel":
		if m.inputFor == "filter" {
			m.filter = m.before
			m.reselect()
		}
		m.mode = modeList
	}
	return m, cmd
}

func (m triModel) decidedCount() int {
	n := 0
	for _, r := range m.rows {
		if r.decided() {
			n++
		}
	}
	return n
}

// decide records a verdict and steps to the next row, so a run of decisions
// is a run of keys. An exclude that would reach a tracked file is refused
// here as well as at write time.
func (m *triModel) decide(v verdict) tea.Cmd {
	i, ok := m.sel()
	if !ok {
		return nil
	}
	if v == vIgnore {
		if hit := covers(m.rows[i].pattern, m.declared); hit != "" {
			return m.push(toast.Toast{Source: "refused", Level: toast.Error,
				Text: fmt.Sprintf("refused: %s would also exclude tracked %s", m.rows[i].pattern, hit)})
		}
	}
	m.rows[i].verdict = v
	m.moveTo(m.cursor(m.visible()) + 1)
	return nil
}

func (m triModel) startApply() (tea.Model, tea.Cmd) {
	m.mode = modeList
	var release tea.Cmd
	m.toasts, release = toast.Release(m.toasts)
	f, id, ok := m.applying.Start()
	if !ok {
		return m, release
	}
	m.applying = f
	// A scan still in flight would land after the write and show the rows
	// from before it; the write's own rescan supersedes it.
	m.scanGen, _ = m.scanGen.Next()
	path, host := m.path, m.host
	rows := append([]triRow(nil), m.rows...)
	return m, tea.Batch(release, func() tea.Msg {
		msg := applyTriage(path, host, rows)
		msg.id = id
		return msg
	})
}

// inputKey feeds the text field. Only the input context's own keys act;
// every other printable key is text, never a command (E13).
func (m triModel) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if b, ok := m.km.ResolveRaw(m.triActive(), k, m.keyOpts()); ok {
		return m.run(b.Action)
	}
	switch k.Type {
	case tea.KeyRunes:
		m.input = append(m.input, k.Runes...)
	case tea.KeySpace:
		m.input = append(m.input, ' ')
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeyCtrlU:
		m.input = nil
	default:
		return m, nil
	}
	if m.inputFor == "filter" {
		m.filter = string(m.input)
		m.reselect()
	}
	return m, nil
}

func (m triModel) submitInput() (tea.Model, tea.Cmd) {
	m.mode = modeList
	if m.inputFor == "filter" {
		return m, nil
	}
	i, ok := m.sel()
	if !ok {
		return m, nil
	}
	pat := strings.TrimSpace(string(m.input))
	warn := func(text string) (tea.Model, tea.Cmd) {
		return m, m.push(toast.Toast{Source: "edit", Level: toast.Warn, Text: text})
	}
	switch {
	case pat == "" || pat == m.rows[i].pattern:
		return m, nil
	case !doublestar.ValidatePattern(pat):
		return warn(pat + " is not a valid glob")
	}
	for j, r := range m.rows {
		if j != i && r.pattern == pat {
			return warn(pat + " is already another row")
		}
	}
	if m.rows[i].verdict == vIgnore {
		if hit := covers(pat, m.declared); hit != "" {
			return m, m.push(toast.Toast{Source: "refused", Level: toast.Error,
				Text: fmt.Sprintf("refused: %s would also exclude tracked %s", pat, hit)})
		}
	}
	m.rows[i].pattern = pat
	m.rows[i].paths = matching(pat, m.orphans)
	if len(m.rows[i].paths) == 0 {
		return warn(pat + " matches none of the unclaimed files")
	}
	return m, nil
}

// runTriageTUI runs the screen. It refuses to start off a terminal: a pipe
// cannot answer a confirm prompt, and the plain report is the tool for that.
func runTriageTUI(path, host string) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("triage -i needs a terminal on stdout; run `dots triage` for the plain report")
	}
	// A pane that panics logs here instead of onto the alternate screen; the
	// log is printed once the terminal is back.
	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", log.LstdFlags)

	// A bad keys.yaml must not stop triage: keep the defaults and say why.
	km, kerr := triageKeymap().LoadOverrides(keys.ConfigPath("dots"), triageStacks...)
	th := theme.NewWith(theme.NewRenderer(os.Stdout), ui.Roles(), theme.DetectGlyphs("dots"))
	m := newTriageModel(path, host, km, th, logger.Printf)
	if kerr != nil {
		// Errors are sticky, so there is no tick to run.
		m.toasts, _ = toast.Push(m.toasts, toast.Toast{Source: "keys", Level: toast.Error, Text: "keys.yaml ignored: " + kerr.Error()})
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if logBuf.Len() > 0 {
		fmt.Fprint(ui.Stderr, logBuf.String())
	}
	return err
}
