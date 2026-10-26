package adapter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// pgSchemaCodes are the SQLSTATEs that mean the statement named a column or a
// table the database does not have: the signal that the schema cache is stale.
var pgSchemaCodes = map[string]bool{
	"42703": true, // undefined_column
	"42P01": true, // undefined_table
}

// textOID declares a parameter the statement does not use. The server infers
// the type of every parameter a statement references, but must be told the
// type of one it skips, or $2 alone fails with "could not determine data type
// of parameter $1".
const textOID = 25

type postgresAdapter struct{}

func (postgresAdapter) Name() string { return "postgres" }

// connConfig builds the connection from host config and the credential.
//
// Every setting the host decides is written into the connection string, even
// at its default, so an inherited PGHOST or PGDATABASE in the shell cannot
// silently redirect the query. The rest of the PG* environment still applies,
// as it did under psql: PGSSLROOTCERT and the like keep working.
func (postgresAdapter) connConfig(cred credential.Credential, host config.HostConfig) (*pgconn.Config, error) {
	h := host.Host
	if h == "" {
		h = "localhost"
	}
	port := host.Port
	if port == 0 {
		port = 5432
	}
	db := host.Database
	if db == "" {
		db = cred.Username // libpq's own default, made explicit
	}
	// sslmode is pinned for the same reason: an inherited PGSSLMODE would
	// otherwise downgrade or force TLS. "prefer" is libpq's default.
	ssl := host.Extra["sslmode"]
	if ssl == "" {
		ssl = "prefer"
	}
	settings := [][2]string{
		{"host", h},
		{"port", strconv.Itoa(port)},
		{"dbname", db},
		{"user", cred.Username},
		{"password", cred.Password},
		{"sslmode", ssl},
		{"application_name", "db-query"},
	}
	var dsn strings.Builder
	for _, kv := range settings {
		dsn.WriteString(kv[0] + "=" + quoteConnValue(kv[1]) + " ")
	}
	cfg, err := pgconn.ParseConfig(dsn.String())
	if err != nil {
		// pgconn's message can quote the connection string, password and all,
		// so only the fact of the failure is reported.
		return nil, &LocalError{Msg: "invalid postgres connection settings for host " + host.Name}
	}

	// A read-only host asks the engine to refuse writes, which holds against
	// statements generated at runtime that no text scan can see: a DROP built
	// inside a DO block is refused here even though the submission reads as a
	// harmless call.
	//
	// This is a second layer, not the guarantee. The setting is a default
	// rather than a constraint, and `SELECT set_config('default_transaction_
	// read_only','off',false)` turns it off from inside a submission. The
	// classifier refuses that call and grants are the actual control
	// (docs/design.md §13.12); this catches the accidents and turns them into
	// a clean early error. It replaces any inherited PGOPTIONS outright, so an
	// inherited setting cannot switch it back off.
	if host.ReadOnly {
		cfg.RuntimeParams["options"] = "-c default_transaction_read_only=on"
	}

	// Cancelling a run tells the server to stop the statement, rather than
	// only dropping the connection and leaving it running until the server
	// notices.
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 2 * time.Second}
	}
	return cfg, nil
}

// quoteConnValue quotes one value for a keyword/value connection string, which
// escapes a quote or a backslash with a backslash.
func quoteConnValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// pgStatement is one statement of a submission, ready to send: its text, and a
// value and declared type for each parameter position up to the highest it
// references.
type pgStatement struct {
	sql    string
	values [][]byte
	oids   []uint32
}

// plan splits a submission into statements and binds each one's parameters.
// It runs before a connection is opened, so a submission with a mistake in its
// last statement runs none of them.
//
// Parameters are positional and bound by the server, so the value of $1 never
// becomes SQL text: there is nothing to quote and nothing to inject. Each
// statement is sent on its own and so numbers its parameters on its own, but
// they all draw on the one set of bound values, so $1 means the same value in
// every statement that uses it.
func (postgresAdapter) plan(q Query) ([]pgStatement, error) {
	for k := range q.Params {
		if !isParamPosition(k) {
			// The name, never the value (§9).
			return nil, &LocalError{Msg: fmt.Sprintf(
				"--param %q: postgres parameters are positional; write $1, $2 in the SQL and bind them with --param 1=value", k)}
		}
	}
	stmts, directives := sqlscan.Scan(q.SQL, sqlscan.DialectPostgres)
	if len(directives) > 0 {
		return nil, &LocalError{Msg: fmt.Sprintf(
			"client directive %q is not SQL; db-query sends SQL to the server, not commands to psql", directives[0])}
	}
	out := make([]pgStatement, 0, len(stmts))
	for _, sql := range stmts {
		st := pgStatement{sql: sql}
		refs := sqlscan.PositionalParams(sql)
		if len(refs) > 0 {
			highest := refs[len(refs)-1]
			st.values = make([][]byte, highest)
			st.oids = make([]uint32, highest)
			for i := range st.oids {
				st.oids[i] = textOID
			}
			for _, n := range refs {
				v, ok := q.Params[strconv.Itoa(n)]
				if !ok {
					return nil, &LocalError{Msg: fmt.Sprintf("$%d is not bound; pass --param %d=value", n, n)}
				}
				st.values[n-1] = []byte(v)
				st.oids[n-1] = 0 // the server infers it from where $n is used
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// isParamPosition reports whether a --param name is a parameter position:
// a positive decimal with no leading zero, within the protocol's limit.
func isParamPosition(k string) bool {
	if k == "" || k[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(k)
	return err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == k
}

// Execute runs a submission over the wire protocol. A single statement runs on
// its own. Several run inside one transaction, in order, and the first to fail
// stops the run: nothing after it is sent and the transaction is rolled back,
// so a script either applies whole or not at all. Every result set is kept, one
// per statement that returned columns.
//
// Statements that cannot run inside a transaction (VACUUM, CREATE DATABASE,
// CREATE INDEX CONCURRENTLY) therefore work only when submitted alone.
func (a postgresAdapter) Execute(ctx context.Context, cred credential.Credential, host config.HostConfig, q Query) ([]Rows, error) {
	stmts, err := a.plan(q)
	if err != nil {
		return nil, err
	}
	if len(stmts) == 0 {
		return nil, nil
	}
	cfg, err := a.connConfig(cred, host)
	if err != nil {
		return nil, err
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil, interrupted("postgres", ctx.Err())
		}
		return nil, &QueryError{Msg: err.Error()}
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn.Close(closeCtx)
	}()

	script := len(stmts) > 1
	if script {
		if err := conn.Exec(ctx, "BEGIN").Close(); err != nil {
			return nil, pgFailure(ctx, err, "starting the transaction")
		}
	}
	var sets []Rows
	for i, st := range stmts {
		rows, err := runStatement(ctx, conn, st)
		if err != nil {
			where := ""
			if script {
				where = fmt.Sprintf("statement %d of %d", i+1, len(stmts))
				// Closing the connection would roll back too; saying so
				// explicitly does not leave it to the server to notice.
				if ctx.Err() == nil {
					_ = conn.Exec(ctx, "ROLLBACK").Close()
				}
			}
			return nil, pgFailure(ctx, err, where)
		}
		if rows != nil {
			sets = append(sets, *rows)
		}
	}
	if script {
		// A deferred constraint is checked here, so the commit can fail on
		// its own after every statement succeeded.
		if err := conn.Exec(ctx, "COMMIT").Close(); err != nil {
			return nil, pgFailure(ctx, err, "committing the transaction")
		}
	}
	return sets, nil
}

// runStatement sends one statement through the extended protocol and reads its
// result as text, the server's own rendering of every value, so a date or a
// numeric prints exactly as the server writes it. It returns nil Rows for a
// statement with no result columns.
func runStatement(ctx context.Context, conn *pgconn.PgConn, st pgStatement) (*Rows, error) {
	rr := conn.ExecParams(ctx, st.sql, st.values, st.oids, nil, nil)
	fields := rr.FieldDescriptions()
	var rows *Rows
	if len(fields) > 0 {
		rows = &Rows{Columns: make([]string, len(fields))}
		for i, f := range fields {
			rows.Columns[i] = f.Name
		}
	}
	for rr.NextRow() {
		vals := rr.Values()
		row := make([]*string, len(vals))
		for i, v := range vals {
			if v != nil { // nil is SQL NULL; an empty slice is ''
				s := string(v)
				row[i] = &s
			}
		}
		if rows != nil {
			rows.Rows = append(rows.Rows, row)
		}
	}
	if _, err := rr.Close(); err != nil {
		return nil, err
	}
	return rows, nil
}

// pgFailure words a failed statement. A server error keeps its severity,
// message and SQLSTATE, with any detail and hint the server offered; where
// names the statement within a script, when there is more than one.
func pgFailure(ctx context.Context, err error, where string) error {
	if ctx.Err() != nil {
		return interrupted("postgres", ctx.Err())
	}
	prefix := ""
	if where != "" {
		prefix = where + ": "
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return &QueryError{Msg: prefix + err.Error()}
	}
	msg := prefix + pe.Error()
	if pe.Detail != "" {
		msg += "\nDETAIL: " + pe.Detail
	}
	if pe.Hint != "" {
		msg += "\nHINT: " + pe.Hint
	}
	return &QueryError{Msg: msg, Schema: pgSchemaCodes[pe.Code]}
}

// ListDatabasesSQL lists the databases this login can actually connect to, one
// bare name per row. datallowconn is the connect-level gate that protects
// template0; NOT datistemplate is needed in addition because template1 allows
// connections and would otherwise pass. has_database_privilege drops databases
// the login holds no CONNECT grant on. pg_database is a shared catalog readable
// by any role, so this needs no privilege an ordinary login lacks — a
// restricted login sees a shorter list rather than an error.
func (postgresAdapter) ListDatabasesSQL() string {
	return `SELECT datname
FROM pg_database
WHERE datallowconn
  AND NOT datistemplate
  AND has_database_privilege(current_user, datname, 'CONNECT')
ORDER BY datname;`
}

func (postgresAdapter) IntrospectSQL() string {
	return `SELECT table_schema, table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY table_schema, table_name, ordinal_position;`
}

// PreviewSQL is the TUI Schema pane's "Cmd+Enter on a table" shortcut:
// postgres caps a row count with LIMIT. The name comes from the cached
// catalogue, so each dot-separated part is double-quoted: without quoting, a
// mixed-case, space-containing or reserved-word identifier — all legal in
// postgres — would produce SQL that does not parse or that folds to the wrong
// table.
func (postgresAdapter) PreviewSQL(table string) string {
	return fmt.Sprintf("SELECT * FROM %s LIMIT 100;", quotePostgresIdent(table))
}

// quotePostgresIdent double-quotes each dot-separated part of a qualified
// name, doubling any embedded quote character as postgres requires.
func quotePostgresIdent(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// Classify decides offline, against PostgreSQL's own grammar. It never
// connects, so a postgres pre-check holds when the database is unreachable and
// costs no round trip.
func (postgresAdapter) Classify(sql string) (sqlscan.Verdict, error) {
	return sqlscan.ClassifyPostgres(sql), nil
}

// Plan is unreachable for postgres, whose Classify always succeeds. It returns
// an error rather than panicking so that a future caller wiring it by mistake
// fails closed and visibly.
func (postgresAdapter) Plan(context.Context, credential.Credential, config.HostConfig, []string, map[string]string) ([]sqlscan.Statement, error) {
	return nil, fmt.Errorf("postgres classifies offline; no planner probe exists")
}

func (postgresAdapter) Dialect() sqlscan.Dialect { return sqlscan.DialectPostgres }
