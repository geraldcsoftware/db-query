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
//
// Every value is carried as the text the provider renders it as, so text and
// table output print exactly what the server wrote. Kinds says, column by
// column, what that text stands for, for the formats that have types of their
// own: json prints a number as a number. A column without a kind, including
// every column of a Rows built without Kinds, is text. Kinds is not persisted
// with a Rows (the schema cache stores the catalogue, which is all text), so a
// cache file keeps the shape it always had.
type Rows struct {
	Columns []string
	Kinds   []Kind `json:"-"`
	Rows    [][]*string
}

// Kind is what a column's values are, as far as a typed output format can
// tell them apart. It is deliberately coarse: anything that is not plainly a
// number or a boolean is text, dates and times included, so a value is never
// reinterpreted on its way out.
type Kind uint8

const (
	KindText Kind = iota
	KindNumber
	KindBool
)

// Kind reports the kind of column i, text when none was recorded.
func (r Rows) Kind(i int) Kind {
	if i < len(r.Kinds) {
		return r.Kinds[i]
	}
	return KindText
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

// Adapter is one provider's way of reaching a server and judging what is sent
// to it. Everything that needs the server goes through a Conn.
type Adapter interface {
	Name() string

	// Open connects to host as cred. ctx bounds the connection attempt only;
	// the connection lasts until its Close. A failure is a *QueryError when the
	// server could not be reached or refused the login, a *LocalError when the
	// host's settings could not even be turned into a connection, and an error
	// wrapping ctx.Err() when the attempt was cancelled or timed out.
	Open(ctx context.Context, cred credential.Credential, host config.HostConfig) (Conn, error)

	// Check refuses, with a *LocalError, a submission that could not be sent
	// as given: a parameter that cannot be bound, a client directive. It needs
	// no connection, so a caller runs it before opening one and a refused
	// submission never reaches the server. Conn.Execute applies the same
	// checks; this only moves them ahead of the connection.
	Check(q Query) error

	IntrospectSQL() string          // lists user tables + columns
	ListDatabasesSQL() string       // lists connectable database names
	PreviewSQL(table string) string // "first 100 rows" of one table, provider-native

	// Classify reports what a submission would do (docs/design.md §13.13),
	// with params bound as Execute would bind them. It never runs the
	// submission.
	//
	// How it decides is the adapter's own business, which is what keeps a
	// caller from ever asking which provider it holds. One that decides from
	// the text alone never touches r. One that asks the engine's planner runs
	// on r's connection, compiling without executing, and leaves the session
	// ready to execute; a nil r means no connection may be made, and it
	// returns an error rather than a verdict.
	//
	// On error the verdict carries only the mechanism that could not decide,
	// so a caller can report which.
	Classify(ctx context.Context, sql string, params map[string]string, r Runner) (sqlscan.Verdict, error)

	// Dialect selects the quoting and batching rules the lexical pre-pass
	// applies before either mechanism runs.
	Dialect() sqlscan.Dialect
}

// Conn is one open session on a server. Runs on it share the session, so a
// temporary table, a SET or a transaction one run leaves open is there for the
// next. It is not safe for concurrent use: one run at a time.
type Conn interface {
	// Execute runs q and returns one Rows for each statement that produced a
	// result set, in submission order. A statement that returns no columns
	// (an INSERT, a SET) contributes nothing, so a submission of only such
	// statements returns none.
	//
	// The error says how far the run got, which is what the exit code
	// reports: *LocalError when nothing was sent or the reply could not be
	// read, *QueryError when the server refused the work, and anything else
	// when the run could not happen at all. A cancelled or expired ctx
	// surfaces as an error wrapping ctx.Err().
	Execute(ctx context.Context, q Query) ([]Rows, error)

	// Usable reports whether the session can take another run. It turns false
	// once a run ends in a state the adapter cannot vouch for, such as a
	// cancel mid-statement or a dropped link, and the caller then closes it
	// and opens another. A server error that leaves the session as the
	// statement left it, an unknown column say, does not count.
	Usable() bool

	Close() error
}

// Runner runs f on a connection, opening one if need be. It is how Classify
// reaches the server without owning a connection: internal/session's Session
// is one, so the plan is made on the session the submission then runs on.
type Runner interface {
	With(ctx context.Context, f func(Conn) error) error
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
