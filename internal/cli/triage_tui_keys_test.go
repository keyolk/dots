package cli

import (
	"strings"
	"testing"

	"github.com/keyolk/tuikit/keys"
)

func TestTriageTUIKeymapHasNoConflicts(t *testing.T) {
	for _, c := range triageKeymap().Conflicts(triageStacks...) {
		t.Error(c)
	}
}

// TestTriageTUIEveryHelpActionIsReachable: help is built from the table, so it
// can only lie if a listed key never resolves to its action in any stack.
func TestTriageTUIEveryHelpActionIsReachable(t *testing.T) {
	km := triageKeymap()
	var m triModel
	for _, sec := range km.Help(keys.Opts{}, triageStacks...) {
		for _, en := range sec.Entries {
			reached := false
			for _, k := range en.Keys {
				for _, st := range triageStacks {
					resolve := km.Resolve
					if st[0] == ctxInput {
						resolve = km.ResolveRaw
					}
					if b, ok := resolve(st, keyMsg(k), keys.Opts{}); ok && b.Action == en.Action {
						reached = true
					}
				}
			}
			if !reached {
				t.Errorf("help lists %q (%v) but no key reaches it", en.Action, en.Keys)
			}
		}
	}
	// And the screen's own stack per mode is one of the stacks checked.
	for _, mode := range []triMode{modeList, modeHelp, modeGroup, modeConfirm, modeInput} {
		m.mode = mode
		found := false
		for _, st := range triageStacks {
			if strings.Join(ctxNames(st), ",") == strings.Join(ctxNames(m.triActive()), ",") {
				found = true
			}
		}
		if !found {
			t.Errorf("mode %d uses a stack the conflict check never sees: %v", mode, m.triActive())
		}
	}
}

func ctxNames(cs []keys.Ctx) []string {
	var out []string
	for _, c := range cs {
		out = append(out, string(c))
	}
	return out
}
