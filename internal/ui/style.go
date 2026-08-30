// Package ui holds the terminal styling shared by every command.
//
// The point of colour here is scanning, not decoration: a status listing runs
// to hundreds of lines, and the eye needs to find the handful that need action
// without reading each path. So colour is attached to the state markers and
// the headline verdicts, and withheld from everything else — a listing where
// every line is coloured is exactly as flat as one where none is.
package ui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// The palette is the Tailwind scale the sibling tools (ghx, dpx, okx, argx)
// already use, so a terminal running several of them reads as one family.
var (
	red    = lipgloss.Color("#F87171") // needs attention now
	yellow = lipgloss.Color("#FBBF24") // needs a decision, not urgent
	green  = lipgloss.Color("#4ADE80") // healthy
	blue   = lipgloss.Color("#60A5FA") // new, not yet recorded
	purple = lipgloss.Color("#A78BFA") // structural: submodules, templates
	grey   = lipgloss.Color("#6B7280") // present but not the point
	bright = lipgloss.Color("#E5E7EB") // headings
)

// Styles used across commands. Names describe the role, not the colour, so a
// palette change stays in this file.
var (
	// StateModified marks a tracked file whose working copy has drifted.
	StateModified = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	// StateUntracked marks a declared file the store has never seen.
	StateUntracked = lipgloss.NewStyle().Foreground(blue).Bold(true)
	// StateMissing marks a tracked file that has vanished from disk.
	StateMissing = lipgloss.NewStyle().Foreground(red).Bold(true)
	// StateUndeclared marks a tracked path no group claims — prune fodder.
	StateUndeclared = lipgloss.NewStyle().Foreground(grey)
	// StateClean is deliberately unstyled: it is the majority of any listing
	// and colouring it would drown the states that need attention.
	StateClean = lipgloss.NewStyle()
	// StateStructural marks submodules and inactive groups — present by
	// design, never actionable.
	StateStructural = lipgloss.NewStyle().Foreground(purple)
	// StateArtifact marks a compiled binary or oversized file.
	StateArtifact = lipgloss.NewStyle().Foreground(red)

	// OK, Warn and Fail are doctor's verdicts.
	OK   = lipgloss.NewStyle().Foreground(green).Bold(true)
	Warn = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	Fail = lipgloss.NewStyle().Foreground(red).Bold(true)

	// Heading names a group or section.
	Heading = lipgloss.NewStyle().Foreground(bright).Bold(true)
	// Count is the "(120)" after a heading.
	Count = lipgloss.NewStyle().Foreground(grey)
	// Path is a file path in a listing.
	Path = lipgloss.NewStyle()
	// Muted is for elided lines and other asides.
	Muted = lipgloss.NewStyle().Foreground(grey)
	// Fix is the actionable suggestion under a warning.
	Fix = lipgloss.NewStyle().Foreground(blue)
	// Refused marks a file the credential guard turned away.
	Refused = lipgloss.NewStyle().Foreground(red).Bold(true)
)

// init disables colour when the output is not a terminal, so a piped or
// redirected run produces plain text that greps and diffs cleanly. NO_COLOR is
// honoured for the same reason. lipgloss detects both, but only for its own
// default writer; being explicit keeps `dots status | grep` predictable.
func init() {
	if os.Getenv("NO_COLOR") != "" || !term.IsTerminal(int(os.Stdout.Fd())) {
		// termenv.Ascii, not 0 -- 0 is TrueColor, so the obvious-looking
		// literal would force colour on instead of off.
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}
