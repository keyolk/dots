package ui

import "testing"

// TestRolesFollowThePalette: the interactive screens take their colours from
// Roles, so a verdict must read the same there as in the plain report.
func TestRolesFollowThePalette(t *testing.T) {
	r := Roles()
	for name, c := range map[string][2]string{
		"Warn": {r.Warn.Dark, string(yellow)}, "Info": {r.Info.Dark, string(blue)},
		"Error": {r.Error.Dark, string(red)}, "Muted": {r.Muted.Dark, string(grey)},
	} {
		if c[0] != c[1] {
			t.Errorf("%s dark = %s, palette says %s", name, c[0], c[1])
		}
	}
}
