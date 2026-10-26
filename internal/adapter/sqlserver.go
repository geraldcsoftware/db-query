package adapter

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// mssqlSchemaNumbers are the engine error numbers that mean the statement
// named a column or a table the database does not have: the signal that the
// schema cache is stale.
var mssqlSchemaNumbers = map[int32]bool{
	207: true, // Invalid column name
	208: true, // Invalid object name
}

type sqlserverAdapter struct{}

func (sqlserverAdapter) Name() string { return "sqlserver" }

// dsn builds the connection URL from host config and the credential.
//
// Encryption is on and the server certificate is verified unless the host
// says otherwise, which is what Microsoft's own ODBC Driver 18 and its sqlcmd
// do. The driver's default is weaker: without an explicit setting it encrypts
// only the login and trusts any certificate, so the setting is always written.
// A host on a self-signed certificate sets trustservercertificate = true, and
// one that cannot do TLS at all sets encrypt = "disable".
func (sqlserverAdapter) dsn(cred credential.Credential, host config.HostConfig) string {
	h := host.Host
	if h == "" {
		h = "localhost"
	}
	u := url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(cred.Username, cred.Password),
	}
	if inst := host.Extra["instance"]; inst != "" {
		// A named instance is found through the SQL Browser service, which
		// answers with its port, so a configured port does not apply.
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		u.Host = h
		u.Path = inst
	} else {
		port := host.Port
		if port == 0 {
			port = 1433
		}
		u.Host = net.JoinHostPort(h, strconv.Itoa(port))
	}
	q := url.Values{}
	if host.Database != "" {
		q.Set("database", host.Database)
	}
	q.Set("app name", "db-query")
	encrypt := host.Extra["encrypt"]
	if encrypt == "" {
		encrypt = "true"
	}
	q.Set("encrypt", encrypt)
	if v := host.Extra["trustservercertificate"]; v != "" {
		q.Set("trustservercertificate", v)
	}
	if v := host.Extra["hostnameincertificate"]; v != "" {
		q.Set("hostnameincertificate", v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Check refuses a submission whose parameter names cannot be bound, or that
// holds a client directive, without connecting.
func (a sqlserverAdapter) Check(q Query) error {
	_, err := a.plan(q)
	return err
}

// Open connects to host as cred. ctx bounds the connection attempt only; the
// connection lasts until Close.
//
// It is one session, not a pool: every batch run on it shares it, so a
// temporary table or a SET made by one batch is still there for the next, as
// it was when one sqlcmd process ran them all, and still there for the next
// run on the same connection.
//
// Except in a batch that binds a parameter. That batch is sent through
// sp_executesql, the only way TDS binds one, and what it creates is scoped to
// the call: a #temporary table it makes, a SET or a USE it runs, are gone when
// it returns. A batch without parameters is sent as it is and keeps them.
func (a sqlserverAdapter) Open(ctx context.Context, cred credential.Credential, host config.HostConfig) (Conn, error) {
	connector, err := mssql.NewConnector(a.dsn(cred, host))
	if err != nil {
		// The driver's message can quote the connection string, password and
		// all, so only the fact of the failure is reported.
		return nil, &LocalError{Msg: "invalid sqlserver connection settings for host " + host.Name}
	}
	db := sql.OpenDB(connector)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, mssqlFailure(ctx, err, "")
	}
	return &mssqlConn{db: db, conn: conn}, nil
}

// mssqlConn is one SQL Server session. It is not safe for concurrent use;
// callers serialise runs on it, as internal/session does.
type mssqlConn struct {
	db   *sql.DB
	conn *sql.Conn
	// broken is set when a run ended in a state this code cannot vouch for:
	// cancelled mid-batch, a dropped link, or a plan-only session that could
	// not be switched back. The session is then replaced rather than reused.
	broken bool
}

func (c *mssqlConn) Usable() bool { return !c.broken }

func (c *mssqlConn) Ping(ctx context.Context) error { return c.conn.PingContext(ctx) }

func (c *mssqlConn) Close() error {
	err := c.conn.Close()
	if dbErr := c.db.Close(); err == nil {
		err = dbErr
	}
	return err
}

// fail records whether err leaves the session in doubt. An engine error is
// the server answering, which leaves the session as the batch left it; a
// cancel, a timeout or a transport failure does not.
func (c *mssqlConn) fail(ctx context.Context, err error) {
	var me mssql.Error
	if ctx.Err() != nil || !errors.As(err, &me) {
		c.broken = true
	}
}

// mssqlBatch is one GO-separated batch of a submission, ready to send: its
// text, the parameters it references and how many times it runs.
type mssqlBatch struct {
	sql   string
	args  []any
	count int
}

// plan splits a submission into batches and binds each one's parameters. It
// runs before anything is sent, so a submission with a mistake in its last
// batch runs none of them.
//
// Parameters are named and bound by the server through sp_executesql, so the
// value of @name never becomes SQL text: there is nothing to quote and nothing
// to inject. A batch is sent only the parameters it references, because a
// parameter it does not use would still be declared, and would collide with a
// DECLARE of the same name inside the batch. A reference with no bound value is
// left alone: it is a local variable the batch declares for itself, or a
// mistake the server reports in its own words.
func (sqlserverAdapter) plan(q Query) ([]mssqlBatch, error) {
	if err := checkMSSQLParamNames(q.Params); err != nil {
		return nil, err
	}
	if _, directives := sqlscan.Scan(q.SQL, sqlscan.DialectTSQL); len(directives) > 0 {
		return nil, &LocalError{Msg: fmt.Sprintf(
			"client directive %q is not SQL; db-query sends SQL to the server, not commands to sqlcmd", directives[0])}
	}
	var out []mssqlBatch
	for _, b := range sqlscan.Batches(q.SQL) {
		out = append(out, mssqlBatch{sql: b.SQL, args: mssqlArgs(b.SQL, q.Params), count: b.Count})
	}
	return out, nil
}

// checkMSSQLParamNames refuses a --param name that could never be referenced
// as @name, and two names a case-insensitive collation would read as one.
func checkMSSQLParamNames(params map[string]string) error {
	seen := map[string]string{}
	for _, k := range sortedKeys(params) {
		if !isMSSQLParamName(k) {
			// The name, never the value (§9).
			hint := ""
			if strings.HasPrefix(k, "@") {
				hint = fmt.Sprintf("; drop the @ and pass --param %s=value", strings.TrimPrefix(k, "@"))
			}
			return &LocalError{Msg: fmt.Sprintf(
				"--param %q: sqlserver parameters are named; write @name in the SQL and bind it with --param name=value%s", k, hint)}
		}
		if prev, ok := seen[strings.ToLower(k)]; ok {
			return &LocalError{Msg: fmt.Sprintf("--param %q and --param %q name the same @parameter", prev, k)}
		}
		seen[strings.ToLower(k)] = k
	}
	return nil
}

// isMSSQLParamName reports whether k is a regular identifier that can follow
// @: a letter or underscore, then letters, digits and underscores, within the
// 128 characters a T-SQL name allows.
func isMSSQLParamName(k string) bool {
	if k == "" || len([]rune(k)) > 127 {
		return false
	}
	for i, r := range k {
		if !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return true
}

// mssqlArgs returns the bound parameters one batch or statement references, as
// the driver takes them.
//
// A value of plain ASCII is sent as varchar, anything else as nvarchar. ASCII
// is byte for byte the same in every SQL Server code page, so nothing is lost,
// and it keeps an index seek on a varchar column, which an nvarchar parameter
// would turn into a scan by converting the column. Against an nvarchar column
// the varchar parameter is the one converted, by type precedence, so nothing
// is lost there either. Names match case-insensitively, as variable names do
// under the default collations, and are sent as the --param spelt them.
func mssqlArgs(text string, params map[string]string) []any {
	if len(params) == 0 {
		return nil
	}
	var args []any
	for _, ref := range sqlscan.NamedParams(text) {
		for k, v := range params {
			if strings.EqualFold(k, ref) {
				if isASCII(v) {
					args = append(args, sql.Named(k, mssql.VarChar(v)))
				} else {
					args = append(args, sql.Named(k, v))
				}
				break
			}
		}
	}
	return args
}

// Execute runs a submission over TDS, one GO-separated batch at a time, each
// repeated as many times as its GO asked. The first batch to
// fail stops the run and nothing after it is sent. As under sqlcmd there is no
// implicit transaction: a batch that fails part-way keeps whatever its earlier
// statements did, unless the batch manages its own transaction. Every result
// set is kept, one per statement that returned columns.
func (c *mssqlConn) Execute(ctx context.Context, q Query) ([]Rows, error) {
	if c.broken {
		return nil, &LocalError{Msg: "the sqlserver connection is no longer usable"}
	}
	batches, err := sqlserverAdapter{}.plan(q)
	if err != nil {
		return nil, err
	}
	var sets []Rows
	for i, b := range batches {
		for range b.count {
			got, err := runBatch(ctx, c.conn, b)
			if err != nil {
				c.fail(ctx, err)
				where := ""
				if len(batches) > 1 {
					where = fmt.Sprintf("batch %d of %d", i+1, len(batches))
				}
				return nil, mssqlFailure(ctx, err, where)
			}
			sets = append(sets, got...)
		}
	}
	return sets, nil
}

// runBatch sends one batch and reads every result set it produces. A
// statement with no result columns (an INSERT, a SET) produces none.
func runBatch(ctx context.Context, conn *sql.Conn, b mssqlBatch) ([]Rows, error) {
	rs, err := conn.QueryContext(ctx, b.sql, b.args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var sets []Rows
	for {
		types, err := rs.ColumnTypes()
		if err != nil {
			return nil, err
		}
		if len(types) > 0 {
			set := Rows{Columns: make([]string, len(types)), Kinds: make([]Kind, len(types)), Rows: [][]*string{}}
			for i, t := range types {
				set.Columns[i] = t.Name()
				set.Kinds[i] = mssqlKind(t.DatabaseTypeName())
			}
			vals := make([]any, len(types))
			ptrs := make([]any, len(types))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			for rs.Next() {
				if err := rs.Scan(ptrs...); err != nil {
					return nil, err
				}
				row := make([]*string, len(vals))
				for i, v := range vals {
					row[i] = mssqlText(v, types[i])
				}
				set.Rows = append(set.Rows, row)
			}
			sets = append(sets, set)
		}
		if !rs.NextResultSet() {
			break
		}
	}
	// An engine error part-way through a batch arrives here, after the result
	// sets before it.
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return sets, nil
}

// mssqlKind maps a column's type to its Kind. A sql_variant column can hold
// anything row by row, so it stays text.
func mssqlKind(typeName string) Kind {
	switch typeName {
	case "TINYINT", "SMALLINT", "INT", "BIGINT", "REAL", "FLOAT",
		"DECIMAL", "MONEY", "SMALLMONEY":
		return KindNumber
	case "BIT":
		return KindBool
	default:
		return KindText
	}
}

// columnType is the part of *sql.ColumnType that rendering needs, so the
// renderer can be tested without a server.
type columnType interface {
	DatabaseTypeName() string
	DecimalSize() (precision, scale int64, ok bool)
}

// mssqlText renders one value the way sqlcmd printed it, so output does not
// change shape for anyone moving from the client to the driver. A nil is SQL
// NULL, which sqlcmd could only print as the word NULL.
func mssqlText(v any, t columnType) *string {
	var s string
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		s = x
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		bits := 64
		if t.DatabaseTypeName() == "REAL" {
			bits = 32
		}
		s = strconv.FormatFloat(x, 'g', -1, bits)
	case bool:
		s = "0"
		if x {
			s = "1"
		}
	case []byte:
		switch t.DatabaseTypeName() {
		case "DECIMAL", "MONEY", "SMALLMONEY":
			s = string(x) // the driver already renders these as text
		case "UNIQUEIDENTIFIER":
			var g mssql.UniqueIdentifier
			if err := g.Scan(x); err != nil {
				s = "0x" + strings.ToUpper(hex.EncodeToString(x))
			} else {
				s = g.String()
			}
		default:
			s = "0x" + strings.ToUpper(hex.EncodeToString(x))
		}
	case time.Time:
		s = x.Format(mssqlTimeLayout(t))
	default:
		s = fmt.Sprint(x)
	}
	return &s
}

// mssqlTimeLayout picks the layout for a temporal column, with as many
// fractional digits as the column's scale declares.
func mssqlTimeLayout(t columnType) string {
	frac := func() string {
		_, scale, ok := t.DecimalSize()
		if !ok || scale <= 0 {
			return ""
		}
		return "." + strings.Repeat("0", int(scale))
	}
	switch t.DatabaseTypeName() {
	case "DATE":
		return "2006-01-02"
	case "TIME":
		return "15:04:05" + frac()
	case "SMALLDATETIME":
		return "2006-01-02 15:04:05"
	case "DATETIME":
		return "2006-01-02 15:04:05.000"
	case "DATETIME2":
		return "2006-01-02 15:04:05" + frac()
	case "DATETIMEOFFSET":
		return "2006-01-02 15:04:05" + frac() + " -07:00"
	default:
		// A temporal value inside a sql_variant carries no column scale.
		return "2006-01-02 15:04:05.9999999"
	}
}

// mssqlFailure words a failed batch. An engine error keeps its number,
// severity, state and line, as sqlcmd printed them; where names the batch
// within a script, when there is more than one.
func mssqlFailure(ctx context.Context, err error, where string) error {
	if ctx.Err() != nil {
		return interrupted("sqlserver", ctx.Err())
	}
	prefix := ""
	if where != "" {
		prefix = where + ": "
	}
	var me mssql.Error
	if !errors.As(err, &me) {
		msg := prefix + err.Error()
		if strings.Contains(msg, "failed to verify certificate") {
			msg += "; a server on a self-signed certificate needs trustservercertificate = true on its host"
		}
		return &QueryError{Msg: msg}
	}
	return &QueryError{
		Msg:    prefix + mssqlErrorText(me),
		Schema: mssqlSchemaNumbers[me.Number],
	}
}

// mssqlErrorText formats an engine error the way SQL Server's own tools do.
func mssqlErrorText(e mssql.Error) string {
	loc := fmt.Sprintf("Msg %d, Level %d, State %d", e.Number, e.Class, e.State)
	if e.ProcName != "" {
		loc += ", Procedure " + e.ProcName
	}
	if e.LineNo > 0 {
		loc += fmt.Sprintf(", Line %d", e.LineNo)
	}
	return loc + ": " + e.Message
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
// submission is split into statements by the lexical pre-pass, the planner
// compiles each one without executing it on r's connection, and the plan says
// what the statement would have done. A plan is reported per statement, which
// is why the submission is split rather than planned whole.
//
// Parameter names are checked before r is touched, so a submission refused
// for one never connects.
func (sqlserverAdapter) Classify(ctx context.Context, sql string, params map[string]string, r Runner) (sqlscan.Verdict, error) {
	none := sqlscan.Verdict{Mechanism: sqlscan.MechanismPlanner}
	if r == nil {
		return none, fmt.Errorf("this provider classifies through the engine, and no connection is available")
	}
	if err := checkMSSQLParamNames(params); err != nil {
		return none, err
	}
	statements, _ := sqlscan.Scan(sql, sqlscan.DialectTSQL)
	if len(statements) == 0 {
		return none, fmt.Errorf("no statements found")
	}
	v := sqlscan.Verdict{Mechanism: sqlscan.MechanismPlanner, Version: "sqlserver engine planner"}
	err := r.With(ctx, func(c Conn) error {
		mc, ok := c.(*mssqlConn)
		if !ok {
			// A runner handing over another provider's connection is a wiring
			// mistake; planning on it would mean nothing, so nothing passes.
			return fmt.Errorf("sqlserver cannot plan on a %T", c)
		}
		var err error
		v.Statements, err = mc.plan(ctx, statements, params)
		return err
	})
	if err != nil {
		return none, err
	}
	v.Reduce()
	return v, nil
}

// plan compiles each statement under SHOWPLAN_XML, which returns a plan
// without running anything, and classifies it from the plan.
//
// It leaves the connection unusable, always, so whoever owns it opens another
// before running anything. SHOWPLAN_XML does not execute the statements it
// plans, but the parse-time settings take effect as the text is parsed:
// verified against SQL Server 2022, a planned SET PARSEONLY ON leaves every
// later query on the session returning nothing, and SET QUOTED_IDENTIFIER OFF
// changes how it parses, neither with an error to notice. No cleanup can name
// every such setting, so the session that planned is never the session that
// runs.
//
// The statements share one connection, so the SET is made again before each
// of them. SHOWPLAN_XML still executes a SET SHOWPLAN_XML OFF it is handed,
// and with the SET made once, a submission of "SET SHOWPLAN_XML OFF; DELETE
// FROM t" would have run its DELETE during the pre-check. The SET has to stand
// alone in its batch, so a statement can turn plan-only off only for what
// follows it, and what follows it is always preceded by a fresh SET.
//
// A statement is planned as plain text rather than through sp_executesql,
// because the engine returns no plan at all for a parameterised call under
// SHOWPLAN_XML. Its bound parameters are declared in front of it instead, with
// the type the driver declares when it binds them for real, so the statement
// compiles exactly as it will run.
//
// A statement that does not compile is opaque, which is not an error: the
// engine answered. An error is a failure to ask, such as a refused login.
func (c *mssqlConn) plan(ctx context.Context, statements []string, params map[string]string) ([]sqlscan.Statement, error) {
	if c.broken {
		return nil, &LocalError{Msg: "the sqlserver connection is no longer usable"}
	}
	c.broken = true // spent, whatever happens below; see above

	out := make([]sqlscan.Statement, 0, len(statements))
	for i, stmt := range statements {
		// If this fails, the statement must not be sent: it would execute.
		if _, err := c.conn.ExecContext(ctx, "SET SHOWPLAN_XML ON"); err != nil {
			c.fail(ctx, err)
			return nil, mssqlFailure(ctx, err, "requesting a plan-only session")
		}
		plan, err := readPlan(ctx, c.conn, mssqlPlanText(stmt, params))
		var class sqlscan.Class
		var why string
		var me mssql.Error
		switch {
		case err == nil:
			class, why = mssqlShowplanClass(plan)
		case errors.As(err, &me) && ctx.Err() == nil:
			class, why = sqlscan.ClassOpaque, "did not compile: "+firstLine(mssqlErrorText(me))
		default:
			c.fail(ctx, err)
			return nil, mssqlFailure(ctx, err, "")
		}
		out = append(out, sqlscan.Statement{Index: i + 1, Class: class, DecidedBy: why})
	}
	return out, nil
}

// mssqlPlanText puts a DECLARE for each bound parameter a statement references
// in front of it, typed as the driver will declare it: varchar of the value's
// length for ASCII (varchar(max) when empty or over 8000 characters), nvarchar
// otherwise (nvarchar(max) over 4000 UTF-16 units). Only the declaration
// matters to the compiler, so no value is assigned.
func mssqlPlanText(stmt string, params map[string]string) string {
	args := mssqlArgs(stmt, params)
	if len(args) == 0 {
		return stmt
	}
	decls := make([]string, len(args))
	for i, a := range args {
		n := a.(sql.NamedArg)
		var typ string
		switch v := n.Value.(type) {
		case mssql.VarChar:
			typ = "varchar(max)"
			if len(v) > 0 && len(v) <= 8000 {
				typ = fmt.Sprintf("varchar(%d)", len(v))
			}
		case string:
			typ = "nvarchar(max)"
			if units := len(utf16.Encode([]rune(v))); units > 0 && units <= 4000 {
				typ = fmt.Sprintf("nvarchar(%d)", units)
			}
		}
		decls[i] = "@" + n.Name + " " + typ
	}
	return "DECLARE " + strings.Join(decls, ", ") + ";\n" + stmt
}

// isASCII reports whether s is entirely 7-bit ASCII.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// mssqlShowplanColumn is the name SQL Server gives the one column of a
// SHOWPLAN_XML result.
const mssqlShowplanColumn = "Microsoft SQL Server 2005 XML Showplan"

// readPlan sends one batch under SHOWPLAN_XML and returns the plan documents
// it produced, concatenated. A result set that is not a plan yields nothing,
// so it can only make the statement opaque: a batch that answers with rows
// was not planned, whatever it was.
func readPlan(ctx context.Context, conn *sql.Conn, text string) (string, error) {
	rs, err := conn.QueryContext(ctx, text)
	if err != nil {
		return "", err
	}
	defer rs.Close()
	var b strings.Builder
	for {
		cols, err := rs.Columns()
		if err != nil {
			return "", err
		}
		if len(cols) != 1 || cols[0] != mssqlShowplanColumn {
			if !rs.NextResultSet() {
				break
			}
			continue
		}
		for rs.Next() {
			var doc sql.NullString
			if err := rs.Scan(&doc); err != nil {
				return "", err
			}
			b.WriteString(doc.String)
		}
		if !rs.NextResultSet() {
			break
		}
	}
	return b.String(), rs.Err()
}

// mssqlStatementType picks the StatementType attribute out of a showplan
// document. The full plan schema is large and versioned; this one attribute is
// the documented statement-level verb, so matching it directly is both smaller
// and less brittle than binding a struct to the schema.
var mssqlStatementType = regexp.MustCompile(`StatementType="([^"]*)"`)

// mssqlStatementElement matches the opening of every statement element a
// showplan can hold (StmtSimple, StmtCond, StmtCursor, StmtReceive,
// StmtUseDb and any later addition), so a statement whose type attribute is
// missing is counted rather than skipped.
var mssqlStatementElement = regexp.MustCompile(`<Stmt[A-Za-z]*[\s/>]`)

// mssqlShowplanClass turns a showplan document into one statement's class.
//
// Everything unrecognised is opaque, deliberately. A plan carrying no
// statement type at all is a plan this code does not understand.
func mssqlShowplanClass(plan string) (sqlscan.Class, string) {
	matches := mssqlStatementType.FindAllStringSubmatch(plan, -1)
	if len(matches) == 0 {
		return sqlscan.ClassOpaque, "no statement type in the plan"
	}
	// Every statement in the plan must be judged. One whose type could not be
	// read would otherwise be left out of the worst class, which is a statement
	// passing because it was not understood. The user's SQL inside the plan is
	// an escaped attribute value, so it cannot add to either count.
	if n := len(mssqlStatementElement.FindAllString(plan, -1)); n > len(matches) {
		return sqlscan.ClassOpaque, fmt.Sprintf("%d of %d statements in the plan carry no type", n-len(matches), n)
	}
	worst, why := sqlscan.ClassRead, ""
	for _, m := range matches {
		c := mssqlClassFor(m[1])
		if c > worst || why == "" {
			worst, why = c, m[1]
		}
	}
	return worst, why
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

// firstLine trims an engine error down to something a decision can carry
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
