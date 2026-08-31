package adapter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// mssqlSep is the ASCII Unit Separator used as the column delimiter.
// sqlcmd does not quote fields, so a comma or tab inside a value would
// mis-split; 0x1F won't appear in data.
const mssqlSep = "\x1f"

type sqlserverAdapter struct{}

func (sqlserverAdapter) Name() string { return "sqlserver" }

// Env sets every SQLCMD* var explicitly to block inherited leakage; the
// password rides the overlay, never argv (-P would be visible in ps).
func (sqlserverAdapter) Env(cred credential.Credential, host config.HostConfig) map[string]string {
	server := host.Host
	if server == "" {
		server = "localhost"
	}
	if inst := host.Extra["instance"]; inst != "" {
		server = server + `\` + inst
	} else if host.Port != 0 {
		server = fmt.Sprintf("tcp:%s,%d", server, host.Port)
	}
	return map[string]string{
		"SQLCMDSERVER":   server,
		"SQLCMDUSER":     cred.Username,
		"SQLCMDPASSWORD": cred.Password,
		"SQLCMDDBNAME":   host.Database,
	}
}

// sqlcmd's $(name) expansion is a textual macro with no quoting — every
// bound value is untrusted input. v1 mitigation: reject metacharacters
// before they reach -v. The v2 ceiling (engine-level binds via
// go-mssqldb) is out of scope by design.
var mssqlBadValue = regexp.MustCompile(`[;'"` + "`" + `\r\n]|--|\$\(`)

func (sqlserverAdapter) Build(host config.HostConfig, q Query) (executor.Invocation, error) {
	for _, k := range sortedKeys(q.Params) {
		if mssqlBadValue.MatchString(q.Params[k]) {
			// Name only — never echo the value itself.
			return executor.Invocation{}, fmt.Errorf(
				"param %q contains characters unsafe for sqlcmd substitution (quotes, ';', '--', '$(')", k)
		}
	}
	argv := []string{
		"sqlcmd",
		"-b",      // batch errors exit nonzero; without it error detection silently breaks
		"-r", "1", // error messages to stderr, not mixed into stdout rows
		"-s", mssqlSep,
		"-W",          // trim trailing whitespace
		"-w", "65535", // defeat line wrapping at default screen width
		// go-sqlcmd quirk: -y 0 suppresses the header line entirely
		// (verified against the actual binary), so pin the legacy
		// maximum instead; variable-length values beyond 8000 chars
		// truncate on such builds.
		"-y", "8000",
		"-Y", "0",
	}
	for _, k := range sortedKeys(q.Params) {
		argv = append(argv, "-v", k+"="+q.Params[k])
	}
	// SET NOCOUNT ON removes the "(N rows affected)" trailer
	// deterministically; the trailing GO flushes the final batch.
	sql := "SET NOCOUNT ON;\n" + q.SQL + "\nGO\n"
	return executor.Invocation{
		Argv:  argv,
		Stdin: strings.NewReader(sql),
	}, nil
}

// Parse handles Path A coaxed-tabular output: line 0 = column names,
// line 1 = the ---- rule (skipped), data follows. NULL prints as the
// literal string "NULL", indistinguishable from a "NULL" value — v1
// accepts this; every cell is a non-nil *string.
func (sqlserverAdapter) Parse(r executor.RawResult) (Rows, error) {
	if r.ExitCode != 0 {
		return Rows{}, fmt.Errorf("sqlcmd exited %d: %s", r.ExitCode, strings.TrimSpace(string(r.Stderr)))
	}
	out := strings.ReplaceAll(string(r.Stdout), "\r\n", "\n")
	if strings.TrimSpace(out) == "" {
		return Rows{}, nil // statement with no result set
	}
	lines := strings.Split(out, "\n")
	// A blank line is a real data row — a single-column result whose value
	// is the empty string prints as nothing under -W — so empties cannot
	// be blanket-skipped. Drop only the two end artifacts: the empty
	// segment Split yields after the final newline, and the one blank
	// trailer line sqlcmd prints after each result set (verified against
	// the actual binary: output is header, rule, data rows, blank line).
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if n := len(lines); n > 0 && strings.TrimRight(lines[n-1], " ") == "" {
		lines = lines[:n-1]
	}
	if len(lines) < 2 {
		return Rows{}, fmt.Errorf("unexpected sqlcmd output (no header rule): %q", lines[0])
	}
	if !strings.HasPrefix(strings.TrimLeft(lines[1], mssqlSep), "-") {
		return Rows{}, fmt.Errorf("unexpected sqlcmd output: line 2 is not a header rule")
	}
	rows := Rows{Columns: strings.Split(lines[0], mssqlSep)}
	for _, line := range lines[2:] {
		fields := strings.Split(line, mssqlSep)
		row := make([]*string, len(fields))
		for i, f := range fields {
			v := f
			row[i] = &v
		}
		rows.Rows = append(rows.Rows, row)
	}
	return rows, nil
}

// Legacy sqlcmd prefixes engine errors with "Msg 207/208"; go-sqlcmd
// prints only the message text, so match both shapes.
var mssqlSchemaErr = regexp.MustCompile(`\bMsg 20[78]\b|Invalid column name|Invalid object name`)

func (sqlserverAdapter) IsSchemaError(r executor.RawResult) bool {
	return r.ExitCode != 0 && (mssqlSchemaErr.Match(r.Stderr) || mssqlSchemaErr.Match(r.Stdout))
}

// ListDatabasesSQL lists the databases this login can actually connect to, one
// bare name per row. HAS_DBACCESS is the real permission filter and needs only
// public role membership; state_desc excludes RESTORING/SUSPECT/OFFLINE and the
// rest, which matters for an elevated login that would otherwise see them.
// tempdb is excluded by name rather than by database_id: the familiar
// master=1, tempdb=2 mapping is not documented as a stable contract. Note
// sys.databases is row-filtered by permission, so a restricted login sees a
// shorter list rather than an error.
func (sqlserverAdapter) ListDatabasesSQL() string {
	return `SELECT name
FROM sys.databases
WHERE state_desc = 'ONLINE'
  AND name <> 'tempdb'
  AND HAS_DBACCESS(name) = 1
ORDER BY name;`
}

func (sqlserverAdapter) IntrospectSQL() string {
	return `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, DATA_TYPE, IS_NULLABLE
FROM INFORMATION_SCHEMA.COLUMNS
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION;`
}

// PreviewSQL is the TUI Schema pane's "Cmd+Enter on a table" shortcut:
// T-SQL caps a row count with TOP, not LIMIT. The name comes from the cached
// catalogue, so each dot-separated part is bracketed: without quoting, a
// space-containing or reserved-word identifier would produce SQL that does
// not parse.
func (sqlserverAdapter) PreviewSQL(table string) string {
	return fmt.Sprintf("SELECT TOP 100 * FROM %s;", quoteSQLServerIdent(table))
}

// quoteSQLServerIdent brackets each dot-separated part of a qualified name,
// doubling any embedded closing bracket as T-SQL requires.
func quoteSQLServerIdent(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = "[" + strings.ReplaceAll(p, "]", "]]") + "]"
	}
	return strings.Join(parts, ".")
}

// Classify defers to the engine. No importable T-SQL parser exists as a Go
// module, so SQL Server reaches a verdict the way §13.13 specifies: the
// planner compiles each statement without executing it, and the plan says what
// the statement would have done.
func (sqlserverAdapter) Classify(string) (sqlscan.Verdict, error) {
	return sqlscan.Verdict{}, sqlscan.ErrNeedsPlan
}

// PlanInvocation wraps one statement in SHOWPLAN_XML, which compiles a batch
// and returns its plan without running it. The SET must stand alone in its own
// batch, hence the GO separators.
//
// sqlcmd's own -X, which disables the commands that could compromise security,
// is deliberately not passed. It is documented as also disabling environment
// variables, which is precisely how Env delivers the credential, and that
// interaction is unverified here. The lexical pre-pass already refuses every
// directive -X would have blocked, under test, so the flag would add risk
// without adding a control. Revisit once -X is confirmed against a real
// instance.
func (sqlserverAdapter) PlanInvocation(host config.HostConfig, stmt string) (executor.Invocation, error) {
	if strings.TrimSpace(stmt) == "" {
		return executor.Invocation{}, fmt.Errorf("empty statement")
	}
	argv := []string{
		"sqlcmd",
		"-b",
		"-r", "1",
		"-s", mssqlSep,
		"-W",
		"-w", "65535",
		"-y", "0",
		"-Y", "0",
	}
	sql := "SET SHOWPLAN_XML ON;\nGO\n" + stmt + "\nGO\n"
	return executor.Invocation{Argv: argv, Stdin: strings.NewReader(sql)}, nil
}

// mssqlStatementType picks the StatementType attribute out of a showplan
// document. The full plan schema is large and versioned; this one attribute is
// the documented statement-level verb, so matching it directly is both smaller
// and less brittle than binding a struct to the schema.
var mssqlStatementType = regexp.MustCompile(`StatementType="([A-Za-z ]+)"`)

// ParsePlan turns a showplan result into one statement's class.
//
// Everything unrecognised is opaque, deliberately. A statement that did not
// compile is not thereby safe, and a plan carrying no statement type at all is
// a plan this code does not understand.
func (sqlserverAdapter) ParsePlan(r executor.RawResult) (sqlscan.Class, string, error) {
	if r.ExitCode != 0 {
		detail := strings.TrimSpace(string(r.Stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(r.Stdout))
		}
		return sqlscan.ClassOpaque, "did not compile: " + firstLine(detail), nil
	}
	matches := mssqlStatementType.FindAllStringSubmatch(string(r.Stdout), -1)
	if len(matches) == 0 {
		return sqlscan.ClassOpaque, "no statement type in the plan", nil
	}
	worst, why := sqlscan.ClassRead, ""
	for _, m := range matches {
		c := mssqlClassFor(m[1])
		if c > worst || why == "" {
			worst, why = c, m[1]
		}
	}
	return worst, why, nil
}

// mssqlClassFor maps a showplan StatementType onto a class. The mapping is an
// allowlist for the same reason the postgres node set is: an unrecognised verb
// denies rather than passing.
func mssqlClassFor(t string) sqlscan.Class {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	// "SELECT WITHOUT QUERY" is the engine's label for a SELECT that touches
	// no table (SELECT 1, SELECT @@VERSION); observed in a real 2022 showplan
	// by the integration suite, whose readiness probe is exactly that shape.
	case "SELECT", "SELECT WITHOUT QUERY":
		return sqlscan.ClassRead
	case "INSERT", "UPDATE", "MERGE":
		return sqlscan.ClassWrite
	// SELECT INTO sits with the schema changes, not the writes: it creates the
	// target table. Every other DDL verb reaches the default and denies, so the
	// allowlist stays short without leaving DDL classified as a write.
	case "DELETE", "TRUNCATE TABLE", "DROP TABLE", "DROP INDEX", "SELECT INTO":
		return sqlscan.ClassDestructive
	case "GRANT", "REVOKE", "DENY", "SET":
		return sqlscan.ClassAdmin
	default:
		return sqlscan.ClassOpaque
	}
}

func (sqlserverAdapter) Dialect() sqlscan.Dialect { return sqlscan.DialectTSQL }

// firstLine trims a client error down to something a decision can carry
// without dragging a stack of driver noise into a JSON document.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
