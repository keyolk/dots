package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/tuikit/render"
	"github.com/keyolk/tuikit/theme"
	"github.com/keyolk/tuikit/toast"

	"github.com/keyolk/dots/internal/ui"
)

// The screen is driven the way Bubble Tea drives it: messages into Update,
// commands run by hand. Every test works on a temporary work tree, store and
// manifest; HOME and the XDG dirs point into the test's temp dir so no real
// keys.yaml or state file is read.

func isolate(t *testing.T, e *env) {
	t.Helper()
	t.Setenv("HOME", e.root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.root, "xdg-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(e.root, "xdg-state"))
}

// hooksEnv is triageEnv with one tracked hook and three orphans, one per
// verdict: README.md (declare), daemon.log (ignore), state (?).
func hooksEnv(t *testing.T) *env {
	t.Helper()
	e := triageEnv(t)
	isolate(t, e)
	e.write(".claude/hooks/guard.py", "x")
	if out, err := e.run("add", "--commit"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	e.write(".claude/hooks/daemon.log", "noise")
	e.write(".claude/hooks/README.md", "docs")
	e.write(".claude/hooks/state", "opaque")
	return e
}

// startTUI builds the model, sizes it and lands its first scan.
func startTUI(t *testing.T, e *env, w, h int) triModel {
	t.Helper()
	th := theme.NewWith(theme.NewRenderer(io.Discard), ui.Roles(), theme.Unicode())
	m := newTriageModel(e.manifest, hostname(), triageKeymap(), th, t.Logf)
	m = send(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range runCmd(t, m.Init()) {
		m = send(t, m, msg)
	}
	if !m.loaded {
		t.Fatal("the first scan did not land")
	}
	return m
}

func send(t *testing.T, m triModel, msgs ...tea.Msg) triModel {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(triModel)
	}
	return m
}

// press sends one key and returns the command it produced.
func press(t *testing.T, m triModel, k string) (triModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(keyMsg(k))
	return next.(triModel), cmd
}

func keyMsg(k string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "ctrl+c": tea.KeyCtrlC,
		"up": tea.KeyUp, "down": tea.KeyDown, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"ctrl+d": tea.KeyCtrlD, "ctrl+u": tea.KeyCtrlU, "backspace": tea.KeyBackspace,
		" ": tea.KeySpace,
	}
	if kt, ok := named[k]; ok {
		return tea.KeyMsg{Type: kt}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func typeText(t *testing.T, m triModel, s string) triModel {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, string(r))
	}
	return m
}

// runCmd runs a command and every command a batch holds, returning their
// messages. Toast ticks sleep for seconds, so each leaf gets a short deadline
// and one that misses it is dropped: the tests never need a tick to fire.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, runCmd(t, c)...)
			}
			return out
		}
		return []tea.Msg{msg}
	case <-time.After(2 * time.Second):
		return nil
	}
}

func (m triModel) rowByID(t *testing.T, id string) triRow {
	t.Helper()
	for _, r := range m.rows {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("no row %q in %v", id, m.ids())
	return triRow{}
}

func (m triModel) ids() []string {
	var out []string
	for _, r := range m.rows {
		out = append(out, r.id)
	}
	return out
}

// selectID moves the cursor with j until it sits on id.
func selectID(t *testing.T, m triModel, id string) triModel {
	t.Helper()
	for range m.rows {
		if m.selID == id {
			return m
		}
		m, _ = press(t, m, "j")
	}
	if m.selID != id {
		t.Fatalf("could not reach %q; cursor on %q", id, m.selID)
	}
	return m
}

func toastTexts(m triModel, level toast.Level) []string {
	var out []string
	for _, it := range m.toasts.Items() {
		if it.Level == level {
			out = append(out, it.Text)
		}
	}
	return out
}

func readManifest(t *testing.T, e *env) string {
	t.Helper()
	b, err := os.ReadFile(e.manifest)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTriageTUIDecideThenApplyWritesTheManifest(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 140, 30)

	if got := m.ids(); len(got) != 3 {
		t.Fatalf("rows = %v, want three proposals", got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "1 pattern(s) need a decision") {
		t.Fatalf("the band is missing while a ? row remains:\n%s", m.View())
	}

	m = selectID(t, m, ".claude/hooks/state")
	m, _ = press(t, m, "d")
	if v := m.rowByID(t, ".claude/hooks/state").verdict; v != vDeclare {
		t.Fatalf("d left the verdict at %s", verdictLabel(v))
	}
	if strings.Contains(ansi.Strip(m.View()), "need a decision") {
		t.Fatal("the band stayed after the last ? row was decided")
	}

	// The confirm dialog takes y only: n and enter write nothing.
	before := readManifest(t, e)
	m, _ = press(t, m, "a")
	if m.mode != modeConfirm {
		t.Fatal("a did not open the confirm dialog")
	}
	m, cmd := press(t, m, "enter")
	if cmd != nil || m.mode != modeConfirm {
		t.Fatal("enter acted in the confirm dialog")
	}
	m, _ = press(t, m, "n")
	if m.mode != modeList || readManifest(t, e) != before {
		t.Fatal("n did not cancel cleanly")
	}

	m, _ = press(t, m, "a")
	m, cmd = press(t, m, "y")
	if !m.applying.Busy() {
		t.Fatal("y did not start the write")
	}
	for _, msg := range runCmd(t, cmd) {
		m = send(t, m, msg)
	}

	body := readManifest(t, e)
	for _, want := range []string{`".claude/hooks/*.md"`, `".claude/hooks/*.log"`, `".claude/hooks/state"`,
		"# compiled, regenerated on every run"} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest is missing %s:\n%s", want, body)
		}
	}
	if got := toastTexts(m, toast.Success); len(got) != 1 || !strings.Contains(got[0], "added 3 line(s)") {
		t.Fatalf("success toasts = %v", got)
	}
	if len(m.rows) != 0 {
		t.Fatalf("the rescan still reports %v", m.ids())
	}
}

// TestTriageTUICursorSurvivesReclassification: the cursor is an ID. A write
// that removes the rows above it, and a filter that hides rows, must leave it
// on the same pattern.
func TestTriageTUICursorSurvivesReclassification(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 100, 30)
	m = selectID(t, m, ".claude/hooks/state")
	m, _ = press(t, m, "s") // skipped: not written, must survive the rescan
	m = selectID(t, m, ".claude/hooks/state")

	m, _ = press(t, m, "a")
	m, cmd := press(t, m, "y")
	for _, msg := range runCmd(t, cmd) {
		m = send(t, m, msg)
	}
	if got := m.ids(); len(got) != 1 || got[0] != ".claude/hooks/state" {
		t.Fatalf("rows after the write = %v", got)
	}
	if m.selID != ".claude/hooks/state" {
		t.Fatalf("cursor moved to %q", m.selID)
	}
	if v := m.rowByID(t, ".claude/hooks/state").verdict; v != vSkip {
		t.Fatalf("the skip was lost on rescan: %s", verdictLabel(v))
	}
}

// TestTriageTUIDropsASupersededScan: a scan that was in flight when a write
// started must not paint the rows from before the write (E5).
func TestTriageTUIDropsASupersededScan(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 100, 30)
	stale := m.scanGen.Current()
	m, _ = press(t, m, "a") // nothing decided yet except the proposals
	m, cmd := press(t, m, "y")
	for _, msg := range runCmd(t, cmd) {
		m = send(t, m, msg)
	}
	after := m.ids()
	m = send(t, m, triScanMsg{gen: stale, data: triData{rows: []triRow{{id: "ghost", pattern: "ghost"}}}})
	if strings.Join(m.ids(), ",") != strings.Join(after, ",") {
		t.Fatalf("a superseded scan replaced the rows: %v", m.ids())
	}
}

func spinEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, `[store]
config    = "{{root}}/config.repo"
work_tree = "{{work}}"

[secrets]
identity = "{{root}}/id.age"
vault    = "vault.age"

[[dotfiles]]
name    = "spin"
include = [".spin/config"]

[[dotfiles]]
name    = "other"
include = [".other/rc"]
`)
	isolate(t, e)
	e.write(".spin/config", "real config")
	e.write(".other/rc", "rc")
	if out, err := e.run("add", "--commit"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	e.write(".spin/notes", "opaque")
	return e
}

// TestTriageTUIRefusesAnExcludeOverATrackedFile: an exclude that reaches a
// tracked path is refused as a sticky error, on the key and again at write
// time, and the manifest is not touched.
func TestTriageTUIRefusesAnExcludeOverATrackedFile(t *testing.T) {
	e := spinEnv(t)
	m := startTUI(t, e, 100, 30)
	m = selectID(t, m, ".spin/notes")

	// Widen the pattern while it is still a question, then try to ignore it.
	m, _ = press(t, m, "e")
	m, _ = press(t, m, "ctrl+u")
	m = typeText(t, m, ".spin/*")
	m, _ = press(t, m, "enter")
	if p := m.rowByID(t, ".spin/notes").pattern; p != ".spin/*" {
		t.Fatalf("edit did not take: %q", p)
	}
	m, _ = press(t, m, "i")
	errs := toastTexts(m, toast.Error)
	if len(errs) != 1 || !strings.Contains(errs[0], "would also exclude tracked .spin/config") {
		t.Fatalf("error toasts = %v", errs)
	}
	if v := m.rowByID(t, ".spin/notes").verdict; v == vIgnore {
		t.Fatal("the refused verdict was recorded")
	}

	// The write path refuses on its own, against a fresh read of the store.
	before := readManifest(t, e)
	msg := applyTriage(e.manifest, hostname(), []triRow{{pattern: ".spin/*", group: "spin", verdict: vIgnore}})
	if msg.err != nil || msg.written != 0 || len(msg.refused) != 1 {
		t.Fatalf("applyTriage = written %d, refused %v, err %v", msg.written, msg.refused, msg.err)
	}
	if readManifest(t, e) != before {
		t.Fatal("a refused exclude reached the manifest")
	}
	m, _ = press(t, m, "a") // nothing decided: a warning, not a dialog
	if m.mode != modeList || len(toastTexts(m, toast.Warn)) == 0 {
		t.Fatal("apply with nothing decided opened the dialog")
	}
	m.applying, msg.id, _ = m.applying.Start()
	m = send(t, m, msg)
	if got := toastTexts(m, toast.Error); len(got) == 0 || !strings.Contains(strings.Join(got, "|"), "refused") {
		t.Fatalf("the write's refusal was not an error toast: %v", got)
	}
}

func TestTriageTUIPicksAGroupInADialog(t *testing.T) {
	e := spinEnv(t)
	m := startTUI(t, e, 100, 30)
	m = selectID(t, m, ".spin/notes")
	m, _ = press(t, m, "g")
	if m.mode != modeGroup {
		t.Fatal("g did not open the group dialog")
	}
	// The dialog paints over the list; the list is still on screen.
	if v := ansi.Strip(m.View()); !strings.Contains(v, "target group for") || !strings.Contains(v, "dots triage") {
		t.Fatalf("the dialog replaced the frame:\n%s", v)
	}
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "enter")
	if g := m.rowByID(t, ".spin/notes").group; g != "other" {
		t.Fatalf("group = %q, want other", g)
	}

	m, _ = press(t, m, "d")
	m, _ = press(t, m, "a")
	m, cmd := press(t, m, "y")
	for _, msg := range runCmd(t, cmd) {
		m = send(t, m, msg)
	}
	body := readManifest(t, e)
	other := body[strings.Index(body, `name    = "other"`):]
	if !strings.Contains(other, `".spin/notes"`) {
		t.Fatalf("the include did not land in the picked group:\n%s", body)
	}
}

// TestTriageTUIFilterOwnsTheKeyboard: while the filter field is open q is a
// letter; esc clears a filter before it quits (E13).
func TestTriageTUIFilterOwnsTheKeyboard(t *testing.T) {
	e := hooksEnv(t)
	e.write(".claude/hooks/q.log", "q")
	m := startTUI(t, e, 100, 30)

	m, _ = press(t, m, "/")
	m = typeText(t, m, "q?")
	if m.mode != modeInput || m.filter != "q?" {
		t.Fatalf("typing into the filter acted as keys: mode %d filter %q", m.mode, m.filter)
	}
	m, _ = press(t, m, "backspace")
	m, _ = press(t, m, "enter")
	if m.filter != "q" {
		t.Fatalf("filter = %q", m.filter)
	}
	m, _ = press(t, m, "esc")
	if m.filter != "" {
		t.Fatal("esc did not clear the filter")
	}
	_, cmd := press(t, m, "esc")
	if cmd == nil {
		t.Fatal("esc with no filter did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("esc with no filter did not quit")
	}
}

// TestTriageTUIDetailDocksOnlyWhenWide: the paths pane is beside the list from
// detailMinWidth up and absent below it.
func TestTriageTUIDetailDocksOnlyWhenWide(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 140, 30)
	m = selectID(t, m, ".claude/hooks/*.log")
	if v := ansi.Strip(m.View()); !strings.Contains(v, "│") || !strings.Contains(v, ".claude/hooks/daemon.log") {
		t.Fatalf("wide view has no detail pane:\n%s", v)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 90, Height: 30})
	if v := ansi.Strip(m.View()); strings.Contains(v, "│") || strings.Contains(v, ".claude/hooks/daemon.log") {
		t.Fatalf("narrow view docked the detail pane:\n%s", v)
	}
}

// TestTriageTUIAlignsByCellsAndSaysWhatIsCut: a Hangul pattern must not push
// the next column, and a list taller than the screen says how much is below.
func TestTriageTUIAlignsByCellsAndSaysWhatIsCut(t *testing.T) {
	e := hooksEnv(t)
	e.write(".claude/hooks/메모", "한글")
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		e.write(".claude/hooks/"+n+".json", "{}")
	}
	m := startTUI(t, e, 100, 10)

	v := ansi.Strip(m.View())
	if !strings.Contains(v, "more") || !strings.Contains(v, "↓") {
		t.Fatalf("a cut list does not say what is below:\n%s", v)
	}
	// Every row's group column starts at the same cell.
	col := -1
	for _, line := range strings.Split(v, "\n") {
		i := strings.Index(line, " claude ")
		if i < 0 {
			continue
		}
		w := render.Width(line[:i])
		if col >= 0 && w != col {
			t.Fatalf("group column at cell %d, earlier rows at %d:\n%s", w, col, v)
		}
		col = w
	}
	if col < 0 {
		t.Fatalf("no row shows its group:\n%s", v)
	}
}

func TestTriageTUIFooterAndHelpComeFromTheTable(t *testing.T) {
	e := hooksEnv(t)
	m := startTUI(t, e, 140, 30)
	v := ansi.Strip(m.View())
	for _, want := range []string{"d declare", "i ignore", "q quit", "? help"} {
		if !strings.Contains(v, want) {
			t.Fatalf("footer is missing %q:\n%s", want, v)
		}
	}
	m, _ = press(t, m, "?")
	v = ansi.Strip(m.View())
	if !strings.Contains(v, "pick the target group") || !strings.Contains(v, "dots triage") {
		t.Fatalf("help is not over the frame:\n%s", v)
	}
	m, _ = press(t, m, "q")
	if m.mode != modeList {
		t.Fatal("q in help did not close it")
	}
}

func TestTriageInteractiveNeedsATerminal(t *testing.T) {
	e := hooksEnv(t)
	out, err := e.run("triage", "-i")
	if err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("triage -i off a terminal: err %v\n%s", err, out)
	}
	if _, err := e.run("triage", "-i", "--apply"); err == nil {
		t.Fatal("-i with --apply was accepted")
	}
}
