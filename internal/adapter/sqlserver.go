package adapter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
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
// T-SQL caps a row count with TOP, not LIMIT.
func (sqlserverAdapter) PreviewSQL(table string) string {
	return fmt.Sprintf("SELECT TOP 100 * FROM %s;", table)
}
