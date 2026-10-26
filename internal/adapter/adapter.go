// Package adapter quarantines everything provider-specific: how SQL reaches
// the server, how credentials are presented, what an error means and what the
// output looks like. Every adapter hands back the same neutral Rows.
//
// Both providers speak their wire protocol directly, so no client binary is
// needed: postgres through pgx, SQL Server through go-mssqldb.
package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// Query is provider-native SQL plus its parameter values. Params are
// written in native placeholder syntax in the SQL and are never substituted
// into the text by Go: both providers bind them server-side. Postgres
// parameters are positional, $1 and $2, so its keys are the positions ("1",
// "2"); SQL Server parameters are named, @name, so its keys are the names.
type Query struct {
	SQL    string
	Params map[string]string
}

// Rows is the neutral rowset every adapter parses into and every
// renderer renders from. A nil *string is SQL NULL; &"" is the empty
// string.
type Rows struct {
	Columns []string
	Rows    [][]*string
}

// First returns the first result set of a run, or an empty Rows when the run
// produced none. Catalogue queries are a single statement, so this is the
// whole of their output.
func First(sets []Rows) Rows {
	if len(sets) == 0 {
		return Rows{}
	}
	return sets[0]
}

// Adapter is one provider's way of running a submission.
type Adapter interface {
	Name() string

	// Execute runs q against host as cred and returns one Rows for each
	// statement that produced a result set, in submission order. A statement
	// that returns no columns (an INSERT, a SET) contributes nothing, so a
	// submission of only such statements returns none.
	//
	// The error says how far the run got, which is what the exit code
	// reports: *LocalError when nothing was sent or the reply could not be
	// read, *QueryError when the server or its client refused the work, and
	// anything else when the run could not happen at all. A cancelled or
	// expired ctx surfaces as an error wrapping ctx.Err().
	Execute(ctx context.Context, cred credential.Credential, host config.HostConfig, q Query) ([]Rows, error)

	IntrospectSQL() string          // lists user tables + columns
	ListDatabasesSQL() string       // lists connectable database names
	PreviewSQL(table string) string // "first 100 rows" of one table, provider-native

	// Classify reports what a submission would do (docs/design.md §13.13).
	// An adapter that can decide from the text alone returns a verdict. One
	// that needs the engine returns sqlscan.ErrNeedsPlan, and the caller then
	// calls Plan.
	//
	// Callers switch on that error, never on a provider name, so a third
	// provider chooses its own mechanism without touching any call site.
	Classify(sql string) (sqlscan.Verdict, error)

	// Plan classifies each statement through the engine's planner, compiling
	// it without running it, with params bound as Execute would bind them. It
	// returns one Statement per statement, in order. An error means the
	// planner could not be asked at all, which is not the same as a statement
	// it refused to compile: that one comes back opaque. Unused by an adapter
	// whose Classify succeeds.
	Plan(ctx context.Context, cred credential.Credential, host config.HostConfig, statements []string, params map[string]string) ([]sqlscan.Statement, error)

	// Dialect selects the quoting and batching rules the lexical pre-pass
	// applies before either mechanism runs.
	Dialect() sqlscan.Dialect
}

// LocalError is a failure on this side of the connection: the submission
// could not be sent as given (a parameter that is not bound, say), or what
// came back could not be read. Nothing about the database is implied.
type LocalError struct{ Msg string }

func (e *LocalError) Error() string { return e.Msg }

// QueryError is a submission the database refused, or could not be reached
// to run. Schema reports an undefined table or column, the signal that the
// schema cache may be stale.
type QueryError struct {
	Msg    string
	Schema bool
}

func (e *QueryError) Error() string { return e.Msg }

// interrupted words a run stopped by its context. A deadline names the client
// that ran out of time; an explicit cancel is passed through as it is, since
// the caller asked for it and checks for it with errors.Is.
func interrupted(client string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out: %w", client, err)
	}
	return err
}

var adapters = map[string]Adapter{
	"postgres":  postgresAdapter{},
	"sqlserver": sqlserverAdapter{},
}

// For returns the adapter for a provider name.
func For(provider string) (Adapter, error) {
	a, ok := adapters[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (supported: postgres, sqlserver)", provider)
	}
	return a, nil
}

// sortedKeys gives a deterministic order over map-carried params, so the
// first bad name reported is the same one every run.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
