package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// hint is one keybinding and what it does, held as a pair so the bottom bar
// can render the keystroke and its effect in different styles.
type hint struct{ key, desc string }

// The bottom bar splits into a left group of things to do and a right group of
// ways out. No command palette and no ? help overlay — both are explicitly out
// of scope, even though the reference mockup shows them.
var (
	// Ordered by how much a user would miss it, because a narrow terminal drops
	// them from the end. Paging is last: it is the one action whose absence
	// from the bar costs least, since PgUp/PgDn is what a user tries anyway.
	actionHints = []hint{
		{"^h/j/k/l", "move"},
		{"^/⌘Enter/F5", "run"},
		{"F2", "switch db"},
		{"Enter", "load/expand"},
		{"PgUp/PgDn", "page"},
	}
	exitHints = []hint{
		{"^c", "cancel"},
		{"Esc", "quit"},
	}

	// queryActionHints and queryExitHints are the bar while a modal editor holds
	// the Query pane, where most of the default bar would be a lie: Enter
	// inserts a newline rather than loading anything, PgUp and PgDn scroll the
	// buffer, and Esc leaves a mode rather than the program. What is left is the
	// three session-level actions, and F10 as the way out.
	queryActionHints = []hint{
		{"^h/j/k/l", "move"},
		{"^/⌘Enter/F5", "run"},
		{"F2", "switch db"},
	}
	queryExitHints = []hint{
		{"F10", "quit"},
	}

	// pickerHints is the startup picker's own footer. "type to filter" is the
	// one line that has to be there: the filter has no visible input box until
	// something is typed into it, so without the hint it is a feature nobody
	// finds.
	pickerHints = []hint{
		{"↑/↓", "move"},
		{"type", "filter"},
		{"Enter", "select"},
		{"Esc", "quit"},
	}
)

// bottomBarHint renders the keybinding reference across the bar's full width,
// the exits pushed to the right edge where a user looks for them. modalEditor
// selects the Query pane's own set, since the keys change under it.
//
// A terminal too narrow for every hint drops them from the right of the action
// group, which is ordered least-essential last. The exits are never dropped:
// the bar being clipped is survivable, a user unable to see how to get out is
// not. Without this the whole line was simply truncated, which took the exits
// off screen first — they sit at the far right.
func bottomBarHint(w int, modalEditor bool) string {
	shown, exits := actionHints, exitHints
	if modalEditor {
		shown, exits = queryActionHints, queryExitHints
	}
	right := renderHints(exits)
	for {
		left := renderHints(shown)
		gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
		if gap >= 1 {
			return left + strings.Repeat(" ", gap) + right
		}
		if len(shown) == 0 {
			return right
		}
		shown = shown[:len(shown)-1]
	}
}

// renderHints joins one group into a single line, each keystroke in the accent
// colour against its effect in body text.
func renderHints(hints []hint) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, hintKeyStyle.Render(h.key)+" "+hintDescStyle.Render(h.desc))
	}
	return strings.Join(parts, hintSepStyle.Render(" · "))
}
