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
	"github.com/jackc/pgx/v5/pgtype"

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
// It runs before anything is sent, so a submission with a mistake in its last
// statement runs none of them.
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

// Check refuses a submission whose parameters cannot be bound, or that holds a
// client directive, without connecting.
func (a postgresAdapter) Check(q Query) error {
	_, err := a.plan(q)
	return err
}

// Open connects to host as cred. ctx bounds the connection attempt only; the
// connection lasts until Close.
func (a postgresAdapter) Open(ctx context.Context, cred credential.Credential, host config.HostConfig) (Conn, error) {
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
	return &pgConn{conn: conn}, nil
}

// pgConn is one postgres session. It is not safe for concurrent use; callers
// serialise runs on it, as internal/session does.
type pgConn struct {
	conn *pgconn.PgConn
	// broken is set when a run ended in a state this code cannot vouch for:
	// cancelled mid-statement, a dropped link, a rollback that did not go
	// through. The session is then replaced rather than reused.
	broken bool
}

func (c *pgConn) Usable() bool { return !c.broken && !c.conn.IsClosed() }

func (c *pgConn) Ping(ctx context.Context) error { return c.conn.Ping(ctx) }

func (c *pgConn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.conn.Close(ctx)
}

// Execute runs a submission. A single statement runs on its own. Several run
// as one unit, in order, and the first to fail stops the run: nothing after it
// is sent and the unit is rolled back, so a script either applies whole or not
// at all. Every result set is kept, one per statement that returned columns.
//
// The unit is a transaction when the session is idle. When the session is
// already inside a transaction the caller opened (a BEGIN run earlier on the
// same connection), it is a savepoint instead, so a failing script undoes its
// own work and leaves the caller's transaction, and what was done in it,
// exactly as it was. Statements that cannot run inside a transaction (VACUUM,
// CREATE DATABASE, CREATE INDEX CONCURRENTLY) therefore work only when
// submitted alone.
func (c *pgConn) Execute(ctx context.Context, q Query) ([]Rows, error) {
	if !c.Usable() {
		return nil, &LocalError{Msg: "the postgres connection is no longer usable"}
	}
	stmts, err := postgresAdapter{}.plan(q)
	if err != nil {
		return nil, err
	}
	if len(stmts) == 0 {
		return nil, nil
	}
	return c.run(ctx, stmts)
}

// failed words err and records whether it leaves the session in doubt. A
// server error is the server answering, which leaves the session as the
// statement left it; a cancel, a timeout or a transport failure does not.
func (c *pgConn) failed(ctx context.Context, err error, where string) error {
	var pe *pgconn.PgError
	if ctx.Err() != nil || !errors.As(err, &pe) {
		c.broken = true
	}
	return pgFailure(ctx, err, where)
}

// pgSavepoint names the savepoint a script runs under inside a caller's
// transaction.
const pgSavepoint = "db_query_script"

func (c *pgConn) run(ctx context.Context, stmts []pgStatement) ([]Rows, error) {
	script := len(stmts) > 1
	begin, commit, rollback := "BEGIN", "COMMIT", "ROLLBACK"
	starting, committing := "starting the transaction", "committing the transaction"
	if script && c.conn.TxStatus() != 'I' {
		// Inside the caller's transaction. When that transaction has already
		// failed, the SAVEPOINT is refused and nothing of the script is sent;
		// the server's message says the transaction is aborted.
		starting, committing = "starting the script's savepoint", "releasing the script's savepoint"
		begin = "SAVEPOINT " + pgSavepoint
		commit = "RELEASE SAVEPOINT " + pgSavepoint
		rollback = "ROLLBACK TO SAVEPOINT " + pgSavepoint + "; RELEASE SAVEPOINT " + pgSavepoint
	}
	if script {
		if err := c.conn.Exec(ctx, begin).Close(); err != nil {
			return nil, c.failed(ctx, err, starting)
		}
	}
	var sets []Rows
	for i, st := range stmts {
		rows, err := runStatement(ctx, c.conn, st)
		if err != nil {
			where := ""
			if script {
				where = fmt.Sprintf("statement %d of %d", i+1, len(stmts))
				// Closing the connection would roll back too, but the
				// connection outlives the run now, so the rollback has to be
				// said. One that does not go through leaves the session in a
				// state nothing can vouch for.
				if ctx.Err() != nil || c.conn.Exec(ctx, rollback).Close() != nil {
					c.broken = true
				}
			}
			return nil, c.failed(ctx, err, where)
		}
		if rows != nil {
			sets = append(sets, *rows)
		}
	}
	if script {
		// A deferred constraint is checked here, so the commit can fail on
		// its own after every statement succeeded.
		if err := c.conn.Exec(ctx, commit).Close(); err != nil {
			return nil, c.failed(ctx, err, committing)
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
		rows = &Rows{Columns: make([]string, len(fields)), Kinds: make([]Kind, len(fields))}
		for i, f := range fields {
			rows.Columns[i] = f.Name
			rows.Kinds[i] = pgKind(f.DataTypeOID)
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

// pgKind maps a column's type to its Kind. Only the built-in numeric and
// boolean types qualify; money prints with a currency symbol and a domain
// carries its own OID, so both stay text.
func pgKind(oid uint32) Kind {
	switch oid {
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID,
		pgtype.Float4OID, pgtype.Float8OID, pgtype.NumericOID:
		return KindNumber
	case pgtype.BoolOID:
		return KindBool
	default:
		return KindText
	}
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
// touches the runner, so a postgres pre-check holds when the database is
// unreachable and costs no round trip. Parameters are $1, $2, which the
// grammar parses as they stand.
func (postgresAdapter) Classify(_ context.Context, sql string, _ map[string]string, _ Runner) (sqlscan.Verdict, error) {
	return sqlscan.ClassifyPostgres(sql), nil
}

func (postgresAdapter) Dialect() sqlscan.Dialect { return sqlscan.DialectPostgres }
