package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// execute runs sql against r's host through the shared adapter/executor
// pipeline, returning the parsed rows, whether a failure is a schema error
// (adapter.IsSchemaError), and the error itself. Unlike session.RunOnce, it
// never writes to a Writer or returns a process exit code — every result
// here becomes pane content or a cancellation, never a process exit. ctx is
// caller-owned: cancelling it surfaces as an error wrapping
// context.Canceled or context.DeadlineExceeded, which the caller checks
// with errors.Is before treating it as a real failure.
func execute(ctx context.Context, r session.Resolved, sql string) (adapter.Rows, bool, error) {
	inv, err := r.Adapter.Build(r.Host, adapter.Query{SQL: sql})
	if err != nil {
		return adapter.Rows{}, false, err
	}
	inv.Env = r.Adapter.Env(r.Cred, r.Host)

	res, err := executor.Run(ctx, inv)
	if err != nil {
		return adapter.Rows{}, false, err
	}
	if res.ExitCode != 0 {
		detail := strings.TrimSpace(string(res.Stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(res.Stdout))
		}
		err := fmt.Errorf("%s exited %d: %s", inv.Argv[0], res.ExitCode, detail)
		return adapter.Rows{}, r.Adapter.IsSchemaError(res), err
	}
	rows, err := r.Adapter.Parse(res)
	return rows, false, err
}
