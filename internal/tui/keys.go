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
	actionHints = []hint{
		{"^h/j/k/l", "move"},
		{"^/⌘Enter/F5", "run"},
		{"Enter", "load/expand"},
		{"PgUp/PgDn", "page"},
	}
	exitHints = []hint{
		{"^c", "cancel"},
		{"Esc", "quit"},
	}
)

// bottomBarHint renders the keybinding reference across the bar's full width,
// the exits pushed to the right edge where a user looks for them.
func bottomBarHint(w int) string {
	left, right := renderHints(actionHints), renderHints(exitHints)
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
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
