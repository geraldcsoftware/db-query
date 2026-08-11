package tui

// bottomBarHint is the always-visible keybinding reference (design §5, §7).
// No command palette, no ? help overlay, no multiple result tabs, no
// per-table row counts — all explicitly out of v1 scope (design §2), even
// though the reference mockup shows them.
const bottomBarHint = "^h/j/k/l move · ^Enter/F5 run · Enter load/expand · PgUp/PgDn page · Esc quit · ^c cancel/quit"
