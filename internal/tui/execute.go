package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// execute runs sql on s's connection, returning every result set, whether a
// failure is a schema error, and the error itself.
// Unlike session.RunOnce, it never writes to a Writer or returns a process
// exit code — every result here becomes pane content or a cancellation, never
// a process exit. ctx is caller-owned: cancelling it surfaces as an error
// wrapping context.Canceled or context.DeadlineExceeded, which the caller
// checks with errors.Is before treating it as a real failure.
func execute(ctx context.Context, s *session.Session, sql string) ([]adapter.Rows, bool, error) {
	sets, err := s.Execute(ctx, adapter.Query{SQL: sql})
	if err != nil {
		var query *adapter.QueryError
		return nil, errors.As(err, &query) && query.Schema, err
	}
	return sets, false, nil
}

// executeOnce is execute on a connection of its own, closed after. The
// catalogue runs use it: a listing may run while a query holds the session's
// connection, and an introspection targets another database altogether.
func executeOnce(ctx context.Context, r session.Resolved, sql string) ([]adapter.Rows, bool, error) {
	s := session.New(r)
	defer s.Close()
	return execute(ctx, s, sql)
}

// closeSession closes a session the model has let go of, off the event loop:
// Close waits for a run still finishing on it, such as one just cancelled.
func closeSession(s *session.Session) tea.Cmd {
	if s == nil {
		return nil
	}
	return func() tea.Msg {
		_ = s.Close() // nothing is waiting on this session any more
		return nil
	}
}
