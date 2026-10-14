package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/tuikit/theme"
)

// Roles maps this package's palette onto the tuikit theme roles, so the
// interactive screens and the plain CLI output give a state the same colour:
// "?" is yellow in `dots triage` and in `dots triage -i`, declare is blue in
// both. The light-background values are the kit's; dots's palette only ever
// had dark values.
func Roles() theme.Roles {
	r := theme.DefaultRoles()
	dark := func(c *lipgloss.AdaptiveColor, v lipgloss.Color) { c.Dark = string(v) }
	dark(&r.Text, bright)
	dark(&r.Muted, grey) // StateUndeclared, Muted: present but not the point
	dark(&r.Info, blue)  // StateUntracked, Fix: new, not yet recorded
	dark(&r.Success, green)
	dark(&r.Warn, yellow) // StateModified, Warn: needs a decision
	dark(&r.Error, red)   // Refused, Fail
	dark(&r.Accent, purple)
	return r
}
