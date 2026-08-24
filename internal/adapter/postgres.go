package adapter

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// pgNullSentinel is what psql prints for NULL (-P null=...). CSV quoting
// alone can't round-trip NULL through encoding/csv (it hides whether a
// field was quoted), so we print NULL as a control byte that cannot
// appear in normal data and map it back to nil.
const pgNullSentinel = "\x01"

type postgresAdapter struct{}

func (postgresAdapter) Name() string { return "postgres" }

// Env sets every PG* var the client reads, even to defaults, so an
// inherited PGDATABASE/PGHOST in the shell can't silently redirect the
// query. The password rides this overlay, never argv.
func (postgresAdapter) Env(cred credential.Credential, host config.HostConfig) map[string]string {
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
		db = cred.Username // psql's own default, made explicit
	}
	env := map[string]string{
		"PGHOST":     h,
		"PGPORT":     strconv.Itoa(port),
		"PGDATABASE": db,
		"PGUSER":     cred.Username,
		"PGPASSWORD": cred.Password,
		"PGAPPNAME":  "db-query",
	}
	// sslmode is a provider var this adapter cares about, so pin it even
	// when host config omits it — otherwise an inherited shell PGSSLMODE
	// would silently change connection behavior (e.g. downgrade or force
	// SSL). "prefer" is libpq's own default, made explicit.
	ssl := host.Extra["sslmode"]
	if ssl == "" {
		ssl = "prefer"
	}
	env["PGSSLMODE"] = ssl

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
	// a clean early error.
	if host.ReadOnly {
		env["PGOPTIONS"] = "-c default_transaction_read_only=on"
	}
	return env
}

func (postgresAdapter) Build(host config.HostConfig, q Query) (executor.Invocation, error) {
	argv := []string{
		"psql",
		"-X", // no psqlrc — a user's rc must not change parse behavior
		"-q",
		"--csv",
		"-v", "ON_ERROR_STOP=1",
		"-v", "VERBOSITY=verbose", // stderr carries SQLSTATE for schema-error detection
		"-P", "null=" + pgNullSentinel,
	}
	// psql interpolates :name as raw text, so a bound value can close the
	// statement and open another: with `WHERE id = :id`, a value of
	// `1; DROP TABLE t` runs the DROP, and the SQL text stays clean, which
	// puts it out of reach of any classifier. Refusing the unquoted form is
	// the fix; :'name' and :"name" are quoted and escaped by psql itself.
	if err := rejectUnquotedInterpolation(q.SQL, q.Params); err != nil {
		return executor.Invocation{}, err
	}
	// User params bind only through psql's own -v; emitted only when the
	// query has at least one param.
	for _, k := range sortedKeys(q.Params) {
		argv = append(argv, "-v", k+"="+q.Params[k])
	}
	return executor.Invocation{
		Argv:  argv,
		Stdin: strings.NewReader(q.SQL),
	}, nil
}

func (postgresAdapter) Parse(r executor.RawResult) (Rows, error) {
	if r.ExitCode != 0 {
		return Rows{}, fmt.Errorf("psql exited %d: %s", r.ExitCode, strings.TrimSpace(string(r.Stderr)))
	}
	if len(bytes.TrimSpace(r.Stdout)) == 0 {
		return Rows{}, nil // statement with no result set
	}
	reader := csv.NewReader(bytes.NewReader(restoreEmptyRows(r.Stdout)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return Rows{}, fmt.Errorf("parsing psql csv output: %w", err)
	}
	if len(records) == 0 {
		return Rows{}, nil
	}
	rows := Rows{Columns: records[0]}
	for _, rec := range records[1:] {
		row := make([]*string, len(rec))
		for i, field := range rec {
			if field == pgNullSentinel {
				row[i] = nil
			} else {
				f := field
				row[i] = &f
			}
		}
		rows.Rows = append(rows.Rows, row)
	}
	return rows, nil
}

// restoreEmptyRows rewrites each fully blank line — how psql --csv prints
// a single-column row whose value is the empty string — as a quoted empty
// field (`""`). encoding/csv documents that a blank line is not a record,
// so without this the row would silently vanish, breaking the locked
// &""-empty-string fidelity (§8). Blank lines inside a quoted multi-line
// field are left untouched: quote state is tracked across the scan.
func restoreEmptyRows(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inQuote := false
	lineLen := 0
	for _, c := range in {
		if c == '"' {
			inQuote = !inQuote
		}
		if c == '\n' && !inQuote {
			if lineLen == 0 {
				out = append(out, '"', '"')
			}
			out = append(out, '\n')
			lineLen = 0
			continue
		}
		out = append(out, c)
		lineLen++
	}
	return out
}

var pgSchemaErr = regexp.MustCompile(`\b42703\b|\b42P01\b|column .* does not exist|relation .* does not exist`)

func (postgresAdapter) IsSchemaError(r executor.RawResult) bool {
	return r.ExitCode != 0 && pgSchemaErr.Match(r.Stderr)
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

// PlanInvocation and ParsePlan are unreachable for postgres, whose Classify
// always succeeds. They return errors rather than panicking so that a future
// caller wiring them by mistake fails closed and visibly.
func (postgresAdapter) PlanInvocation(config.HostConfig, string) (executor.Invocation, error) {
	return executor.Invocation{}, fmt.Errorf("postgres classifies offline; no planner probe exists")
}

func (postgresAdapter) ParsePlan(executor.RawResult) (sqlscan.Class, string, error) {
	return sqlscan.ClassOpaque, "", fmt.Errorf("postgres classifies offline; no planner probe exists")
}

func (postgresAdapter) Dialect() sqlscan.Dialect { return sqlscan.DialectPostgres }

// rejectUnquotedInterpolation refuses a bound parameter referenced as :name
// rather than :'name'. Only bound names are checked, because psql interpolates
// nothing else, and because a colon has other meanings in SQL: a cast, and an
// array slice such as arr[1:3].
//
// Literals, quoted identifiers, dollar-quoted bodies and comments are skipped
// through sqlscan.SkipSpan, the same helper the classifier uses, so the two
// cannot disagree about where code stops and text begins. psql does not
// interpolate inside them either, so flagging a colon there would refuse
// legitimate SQL such as to_char(t, 'HH:MM:SS').
//
// The error names the parameter and never its value (§9).
func rejectUnquotedInterpolation(sql string, params map[string]string) error {
	if len(params) == 0 {
		return nil
	}
	src := []rune(sql)
	for i := 0; i < len(src); {
		if end, isSpan := sqlscan.SkipSpan(src, i, sqlscan.DialectPostgres); isSpan {
			i = end
			continue
		}
		if src[i] != ':' {
			i++
			continue
		}
		if i+1 < len(src) && src[i+1] == ':' {
			i += 2 // a cast, not an interpolation
			continue
		}
		if i+1 < len(src) && (src[i+1] == '\'' || src[i+1] == '"') {
			i++ // the quoted forms, which psql escapes
			continue
		}
		j := i + 1
		for j < len(src) && isNameRune(src[j]) {
			j++
		}
		name := string(src[i+1 : j])
		if _, bound := params[name]; bound {
			return fmt.Errorf(
				"parameter %q is interpolated as :%s, which psql substitutes as raw text; "+
					"write :'%s' so the value is quoted", name, name, name)
		}
		i = j
	}
	return nil
}

func isNameRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
