package ui

import "github.com/charmbracelet/lipgloss"

// Pad renders s in st and pads it to w terminal cells, for a column that the
// next field has to line up against.
//
// Style.Width alone is the wrong tool for this: it is a wrap width, so a
// group name one character longer than the column folds onto a second line
// and breaks the row in two. Inline keeps the text on one line -- a value
// longer than the column overflows, which leaves it whole and readable -- and
// the padding is measured in cells, so a CJK name (two cells per rune) lines
// up where fmt's %-Ns, which counts runes, would not.
func Pad(st lipgloss.Style, s string, w int) string {
	return st.Inline(true).Width(w).Render(s)
}
