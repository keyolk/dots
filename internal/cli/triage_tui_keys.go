package cli

import "github.com/keyolk/tuikit/keys"

// Key contexts of the triage screen. One is active at a time, innermost
// first, over global.
const (
	ctxGlobal  keys.Ctx = "global"
	ctxList    keys.Ctx = "list"
	ctxHelp    keys.Ctx = "help"
	ctxGroup   keys.Ctx = "group"
	ctxConfirm keys.Ctx = "confirm"
	ctxInput   keys.Ctx = "input"
)

// triageStacks is every stack the screen can be in. The conflict test and
// LoadOverrides both judge the table against exactly these.
//
// The text field stands alone: while it owns the keyboard, q and ? are
// characters of a pattern, not quit and help.
var triageStacks = [][]keys.Ctx{
	{ctxList, ctxGlobal},
	{ctxHelp, ctxGlobal},
	{ctxGroup, ctxGlobal},
	{ctxConfirm, ctxGlobal},
	{ctxInput},
}

// triageKeymap is the one table that drives dispatch, the footer and the ?
// help. Users rebind it in ~/.config/dots/keys.yaml.
func triageKeymap() keys.Keymap {
	return keys.New(
		keys.Binding{Action: "quit", Keys: []string{"q", "esc", "ctrl+c"}, Ctx: ctxGlobal, Help: "quit", Pinned: true, Footer: 2},
		keys.Binding{Action: "help", Keys: []string{"?"}, Ctx: ctxGlobal, Help: "help", Pinned: true, Footer: 1},

		keys.Binding{Action: "list.down", Keys: []string{"j", "down"}, Ctx: ctxList, Group: "Move", Help: "down", Footer: 1, FooterGroup: "move"},
		keys.Binding{Action: "list.up", Keys: []string{"k", "up"}, Ctx: ctxList, Group: "Move", Help: "up", Footer: 1, FooterGroup: "move"},
		keys.Binding{Action: "list.pgdown", Keys: []string{"ctrl+d", "pgdown"}, Ctx: ctxList, Group: "Move", Help: "page down"},
		keys.Binding{Action: "list.pgup", Keys: []string{"ctrl+u", "pgup"}, Ctx: ctxList, Group: "Move", Help: "page up"},

		keys.Binding{Action: "row.declare", Keys: []string{"d"}, Ctx: ctxList, Group: "Decide", Help: "declare: include in the group", Short: "declare", Footer: 2},
		keys.Binding{Action: "row.ignore", Keys: []string{"i"}, Ctx: ctxList, Group: "Decide", Help: "ignore: exclude from the group", Short: "ignore", Footer: 2},
		keys.Binding{Action: "row.skip", Keys: []string{"s"}, Ctx: ctxList, Group: "Decide", Help: "skip: leave it out of the write", Short: "skip", Footer: 3},
		keys.Binding{Action: "row.group", Keys: []string{"g"}, Ctx: ctxList, Group: "Edit", Help: "pick the target group", Short: "group", Footer: 5},
		keys.Binding{Action: "row.edit", Keys: []string{"e"}, Ctx: ctxList, Group: "Edit", Help: "edit the pattern", Short: "edit", Footer: 5},
		keys.Binding{Action: "apply", Keys: []string{"a"}, Ctx: ctxList, Group: "Write", Help: "write decisions to the manifest", Short: "apply", Footer: 3},
		keys.Binding{Action: "filter", Keys: []string{"/"}, Ctx: ctxList, Group: "View", Help: "filter rows", Short: "filter", Footer: 4},
		// esc clears a filter before it quits; only enabled while one is set.
		keys.Binding{Action: "filter.clear", Keys: []string{"esc"}, Ctx: ctxList, Group: "View", Help: "clear the filter", Short: "unfilter", Footer: 1, Shadows: []string{"quit"}},

		keys.Binding{Action: "help.close", Keys: []string{"esc", "q", "?"}, Ctx: ctxHelp, Help: "close help", Footer: 1, Shadows: []string{"quit", "help"}},
		keys.Binding{Action: "help.down", Keys: []string{"j", "down"}, Ctx: ctxHelp, Help: "scroll down", Footer: 2, FooterGroup: "scroll"},
		keys.Binding{Action: "help.up", Keys: []string{"k", "up"}, Ctx: ctxHelp, Help: "scroll up", Footer: 2, FooterGroup: "scroll"},

		keys.Binding{Action: "group.down", Keys: []string{"j", "down"}, Ctx: ctxGroup, Help: "next group", Footer: 2, FooterGroup: "move"},
		keys.Binding{Action: "group.up", Keys: []string{"k", "up"}, Ctx: ctxGroup, Help: "previous group", Footer: 2, FooterGroup: "move"},
		keys.Binding{Action: "group.pick", Keys: []string{"enter"}, Ctx: ctxGroup, Help: "use this group", Short: "pick", Footer: 1},
		keys.Binding{Action: "group.cancel", Keys: []string{"esc", "q"}, Ctx: ctxGroup, Help: "keep the group", Short: "cancel", Footer: 1, Shadows: []string{"quit"}},

		// y only: a stray enter must not write the manifest (E13).
		keys.Binding{Action: "confirm.yes", Keys: []string{"y"}, Ctx: ctxConfirm, Help: "write", Footer: 1},
		keys.Binding{Action: "confirm.no", Keys: []string{"n", "esc", "q"}, Ctx: ctxConfirm, Help: "cancel", Footer: 1, Shadows: []string{"quit"}},

		keys.Binding{Action: "input.submit", Keys: []string{"enter"}, Ctx: ctxInput, Help: "accept", Footer: 1},
		keys.Binding{Action: "input.cancel", Keys: []string{"esc", "ctrl+c"}, Ctx: ctxInput, Help: "cancel", Footer: 1},
	)
}

// triActive is the context stack for the screen's current mode.
func (m triModel) triActive() []keys.Ctx {
	switch m.mode {
	case modeHelp:
		return triageStacks[1]
	case modeGroup:
		return triageStacks[2]
	case modeConfirm:
		return triageStacks[3]
	case modeInput:
		return triageStacks[4]
	}
	return triageStacks[0]
}

// keyOpts is the model's view of its actions, passed to every keys call.
func (m triModel) keyOpts() keys.Opts {
	return keys.Opts{Enabled: m.enabled, Visible: m.offered}
}

// enabled gates dispatch. A decision needs a row to land on, and nothing
// changes while a write is in flight: its rescan replaces the rows.
func (m triModel) enabled(action string) bool {
	switch action {
	case "row.declare", "row.ignore", "row.skip", "row.group", "row.edit":
		_, ok := m.sel()
		return ok && !m.applying.Busy()
	case "apply":
		return m.loaded && !m.applying.Busy()
	case "filter.clear":
		return m.filter != ""
	case "help":
		return m.mode == modeList
	}
	return true
}

// offered narrows the footer only.
func (m triModel) offered(action string) bool {
	switch action {
	case "list.down", "list.up":
		return m.list.Len() > 1
	case "row.group":
		return len(m.groups) > 1
	}
	return true
}

// keyName names the key that runs action in the stack, as the theme displays
// it, so the band and dialogs name the key the user actually rebound. It
// asks with no Enabled gate: the band names the decision keys even while a
// write is in flight, when they are briefly disabled.
func (m triModel) keyName(active []keys.Ctx, action string) string {
	k, ok := m.km.KeyFor(active, action, keys.Opts{})
	if !ok {
		return "?"
	}
	return m.th.KeyNames().Name(k)
}
