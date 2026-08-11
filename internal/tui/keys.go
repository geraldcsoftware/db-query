package tui

import "strings"

// keyHints is the always-visible keybinding reference (design §5, §7), held as
// key/effect pairs so the bottom bar can render the keystroke and its effect in
// different styles. No command palette, no ? help overlay, no multiple result
// tabs, no per-table row counts — all explicitly out of v1 scope (design §2),
// even though the reference mockup shows them.
var keyHints = []struct{ key, desc string }{
	{"^h/j/k/l", "move"},
	{"^Enter/F5", "run"},
	{"Enter", "load/expand"},
	{"PgUp/PgDn", "page"},
	{"Esc", "quit"},
	{"^c", "cancel/quit"},
}

// bottomBarHint renders the keybinding reference as a single line, each
// keystroke in the accent colour against its effect in muted text.
func bottomBarHint() string {
	parts := make([]string, 0, len(keyHints))
	for _, h := range keyHints {
		parts = append(parts, hintKeyStyle.Render(h.key)+" "+hintDescStyle.Render(h.desc))
	}
	return strings.Join(parts, hintSepStyle.Render(" · "))
}
