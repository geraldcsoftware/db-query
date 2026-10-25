package tui

import (
	"context"
	"errors"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// execute runs sql against r's host through the adapter, returning every
// result set, whether a failure is a schema error, and the error itself.
// Unlike session.RunOnce, it never writes to a Writer or returns a process
// exit code — every result here becomes pane content or a cancellation, never
// a process exit. ctx is caller-owned: cancelling it surfaces as an error
// wrapping context.Canceled or context.DeadlineExceeded, which the caller
// checks with errors.Is before treating it as a real failure.
func execute(ctx context.Context, r session.Resolved, sql string) ([]adapter.Rows, bool, error) {
	sets, err := r.Adapter.Execute(ctx, r.Cred, r.Host, adapter.Query{SQL: sql})
	if err != nil {
		var query *adapter.QueryError
		return nil, errors.As(err, &query) && query.Schema, err
	}
	return sets, false, nil
}
