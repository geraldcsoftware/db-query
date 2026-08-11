// Package tui implements db-query's no-args interactive mode: a four-pane
// terminal UI (Schema, Saved, Query, Results) for the browse -> write ->
// run -> view loop, built on Bubble Tea. It resolves its session's
// host/credential once at startup through internal/session — the same
// Setup used by every CLI command — and never imports internal/cli, so
// internal/cli can safely import this package without an import cycle.
package tui

import (
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// Run resolves the session's host and database (prompting with a picker for
// whichever did not resolve from flags/env/config), then runs the interactive
// program. A startup failure — a credential that cannot be resolved, or a
// host with no database to offer — is fatal: it is reported on stderr and
// Run returns a non-zero code before any Bubble Tea program starts, exactly
// like every other command's credential-error path. version is displayed in
// the top bar; the caller supplies it because internal/cli, which owns the
// build info, imports this package and so cannot be imported back.
func Run(c session.CommonFlags, version string, stdout, stderr io.Writer) int {
	r, code := bootstrap(c, stderr)
	if code != 0 {
		return code
	}
	if !shouldLaunch(r) {
		return 0
	}
	m := newModel(r, c, version, stdout)
	// The alternate screen and mouse reporting are declared by the model's
	// View, not by program options, so the program itself takes none.
	if _, err := tea.NewProgram(m).Run(); err != nil {
		io.WriteString(stderr, "db-query: "+err.Error()+"\n")
		return 1
	}
	return 0
}

// shouldLaunch reports whether bootstrap resolved a session worth launching
// the interactive program against. bootstrap returns a zero-value
// session.Resolved alongside a 0 exit code when the user quits a startup
// picker without choosing (Esc/Ctrl+C) — that is "nothing to do", not an
// error, so it can't be distinguished by exit code alone. session.Setup's
// only success path sets Adapter via adapter.For, which never returns a nil
// Adapter on success, so a nil Adapter here unambiguously means bootstrap
// had nothing to launch.
func shouldLaunch(r session.Resolved) bool {
	return r.Adapter != nil
}
