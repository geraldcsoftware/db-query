package tui

import "strings"

// The rules are the layout's only structural chrome: two columns divided by a
// vertical rule, each split by a horizontal one, framed top and bottom by
// full-width rules. The junction runes are what make them read as one frame
// instead of as unrelated line fragments, and each costs a single character
// substitution at a column the layout already knows.
const (
	ruleHorizontal = "─"
	ruleVertical   = "│"
	ruleTeeDown    = "┬" // the top full-width rule crossing the vertical rule
	ruleTeeUp      = "┴" // the bottom one
	ruleTeeLeft    = "┤" // the sidebar's horizontal rule meeting it from the left
	ruleTeeRight   = "├" // the main column's meeting it from the right
	ruleCross      = "┼" // both, on the same row
)

// hRule is n cells of horizontal rule.
func hRule(n int) string {
	if n <= 0 {
		return ""
	}
	return ruleStyle.Render(strings.Repeat(ruleHorizontal, n))
}

// fullRule draws a rule the whole width of the terminal, putting junction at
// the vertical rule's column. crossX below zero means there is no vertical
// rule to cross.
func fullRule(w, crossX int, junction string) string {
	if w <= 0 {
		return ""
	}
	if crossX < 0 || crossX >= w {
		return hRule(w)
	}
	return hRule(crossX) + ruleStyle.Render(junction) + hRule(w-crossX-1)
}

// junctionAt picks the rune for the vertical rule's cell on one body row,
// given which of the two columns draws its own horizontal rule there.
func junctionAt(sidebarRule, mainRule bool) string {
	switch {
	case sidebarRule && mainRule:
		return ruleCross
	case sidebarRule:
		return ruleTeeLeft
	case mainRule:
		return ruleTeeRight
	default:
		return ruleVertical
	}
}
