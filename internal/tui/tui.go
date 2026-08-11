// Package tui implements db-query's no-args interactive mode: a four-pane
// terminal UI (Schema, Saved, Query, Results) for the browse -> write ->
// run -> view loop, built on Bubble Tea. It resolves its session's
// host/credential once at startup through internal/session — the same
// Setup used by every CLI command — and never imports internal/cli, so
// internal/cli can safely import this package without an import cycle.
package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// Run resolves the session's host (prompting with a picker first if none
// resolved from flags/env/config), then runs the interactive program. A
// startup failure — no host chosen, or a credential that cannot be
// resolved — is fatal: it is reported on stderr and Run returns 1 before
// any Bubble Tea program starts, exactly like every other command's
// credential-error path.
func Run(c session.CommonFlags, stdout, stderr io.Writer) int {
	r, code := bootstrap(c, stderr)
	if code != 0 {
		return code
	}
	m := newModel(r, c, stdout)
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		io.WriteString(stderr, "db-query: "+err.Error()+"\n")
		return 1
	}
	return 0
}
