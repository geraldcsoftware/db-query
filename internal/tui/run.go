package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// queryResultMsg is delivered when a dispatched run finishes, however it
// finished: success, a schema error, another SQL error, or a cancellation
// (err wraps context.Canceled or context.DeadlineExceeded in that case —
// checked in Update via errors.Is, not by string-matching).
type queryResultMsg struct {
	rows      adapter.Rows
	schemaErr bool
	err       error

	// gen is the session generation the run was dispatched under. A database
	// switch bumps the model's, so a result that outlives the database it was
	// asked of arrives stamped with a generation that no longer matches and is
	// discarded instead of being rendered as though it came from the new one.
	gen int
}

// queryTextMsg is the Query pane's answer to being asked what to run: the SQL,
// whether it came from a visual selection rather than the whole buffer, and the
// failure if the editor could not be asked at all.
type queryTextMsg struct {
	sql       string
	selection bool
	err       error
}

// startRun begins a query run if none is already in flight (single-flight).
// It clears the Results pane and marks the model running immediately — before
// the tea.Cmd it returns has even been scheduled, so the next frame already
// shows the running indicator in the bottom bar — and stores a CancelFunc so
// Ctrl+C can cancel it. A second call while one is already running does not
// start anything; it sets a transient status message instead.
// source names where the SQL came from, for the Results pane's label row; the
// empty string is the whole buffer, which needs no explaining.
func (m *model) startRun(sql, source string) tea.Cmd {
	if m.running {
		return m.setStatus("query already running — Ctrl+C to cancel")
	}
	m.runSource = source
	ctx, cancel := context.WithTimeout(context.Background(), m.flags.Timeout)
	m.cancel = cancel
	m.running = true
	m.results.clear()
	run := m.runner
	sess := m.session
	gen := m.runGen
	return func() tea.Msg {
		rows, schemaErr, err := run(ctx, sess, sql)
		return queryResultMsg{rows: rows, schemaErr: schemaErr, err: err, gen: gen}
	}
}

// cancelRunning calls the held CancelFunc (a no-op if none is set) and
// returns focus to the Query pane. The actual running=false transition
// happens when the resulting queryResultMsg (carrying a
// context.Canceled/DeadlineExceeded error) arrives — cancelRunning itself
// only requests cancellation, since the in-flight goroutine may take a
// moment to observe ctx.Done().
func (m model) cancelRunning() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	m.focus = paneQuery
	return m, nil
}

// clearStatusMsg clears the transient status-bar text. gen guards against a
// late timer from an older status message clearing a newer one: Update
// only clears when gen matches the model's current statusGen.
type clearStatusMsg struct{ gen int }

func clearStatusAfter(gen int) tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
		return clearStatusMsg{gen: gen}
	})
}
