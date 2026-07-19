package adapter

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
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

func (postgresAdapter) IntrospectSQL() string {
	return `SELECT table_schema, table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY table_schema, table_name, ordinal_position;`
}
