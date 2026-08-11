package adapter

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
)

func TestSqlserverEnv(t *testing.T) {
	a := sqlserverAdapter{}
	cred := credential.Credential{Username: "sa", Password: "pw"}

	t.Run("all provider vars set explicitly", func(t *testing.T) {
		env := a.Env(cred, config.HostConfig{Host: "sql01", Database: "reports"})
		want := map[string]string{
			"SQLCMDSERVER": "sql01", "SQLCMDUSER": "sa",
			"SQLCMDPASSWORD": "pw", "SQLCMDDBNAME": "reports",
		}
		if !reflect.DeepEqual(env, want) {
			t.Fatalf("env = %v, want %v", env, want)
		}
	})
	t.Run("port becomes tcp server string", func(t *testing.T) {
		env := a.Env(cred, config.HostConfig{Host: "sql01", Port: 11433})
		if env["SQLCMDSERVER"] != "tcp:sql01,11433" {
			t.Fatalf("server = %q", env["SQLCMDSERVER"])
		}
	})
	t.Run("instance wins over port", func(t *testing.T) {
		env := a.Env(cred, config.HostConfig{
			Host: "sql01", Extra: map[string]string{"instance": "SQLEXPRESS"},
		})
		if env["SQLCMDSERVER"] != `sql01\SQLEXPRESS` {
			t.Fatalf("server = %q", env["SQLCMDSERVER"])
		}
	})
}

func TestSqlserverBuild(t *testing.T) {
	a := sqlserverAdapter{}

	t.Run("path A coaxing flags present", func(t *testing.T) {
		inv, err := a.Build(config.HostConfig{}, Query{SQL: "SELECT 1"})
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Join(inv.Argv, " ")
		for _, needed := range []string{"-b", "-r 1", "-s \x1f", "-W", "-y 8000", "-Y 0"} {
			if !strings.Contains(argv, needed) {
				t.Fatalf("argv %q missing %q", argv, needed)
			}
		}
		sql, _ := io.ReadAll(inv.Stdin)
		if !strings.HasPrefix(string(sql), "SET NOCOUNT ON;") {
			t.Fatalf("stdin = %q, want NOCOUNT prefix", sql)
		}
		if !strings.Contains(string(sql), "SELECT 1") {
			t.Fatalf("stdin = %q", sql)
		}
	})

	t.Run("params bind via -v", func(t *testing.T) {
		inv, err := a.Build(config.HostConfig{}, Query{
			SQL: "SELECT * FROM t WHERE id = $(id)", Params: map[string]string{"id": "42"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(inv.Argv, " "), "-v id=42") {
			t.Fatalf("argv = %v", inv.Argv)
		}
	})

	t.Run("unsafe param values rejected before execution", func(t *testing.T) {
		bad := []string{"x'y", `x"y`, "1;DROP TABLE t", "a--b", "$(oops)", "line\nbreak"}
		for _, v := range bad {
			_, err := a.Build(config.HostConfig{}, Query{
				SQL: "SELECT $(p)", Params: map[string]string{"p": v},
			})
			if err == nil {
				t.Errorf("value %q must be rejected", v)
			} else if strings.Contains(err.Error(), v) {
				t.Errorf("error must not echo the value %q", v)
			}
		}
	})

	t.Run("benign values pass", func(t *testing.T) {
		for _, v := range []string{"42", "svc_reports", "2024-01-01", "some words"} {
			if _, err := a.Build(config.HostConfig{}, Query{
				SQL: "SELECT $(p)", Params: map[string]string{"p": v},
			}); err != nil {
				t.Errorf("value %q wrongly rejected: %v", v, err)
			}
		}
	})
}

func TestSqlserverParse(t *testing.T) {
	a := sqlserverAdapter{}
	sep := "\x1f"

	t.Run("headers, dash rule skipped, data parsed", func(t *testing.T) {
		out := strings.Join([]string{
			"id" + sep + "name" + sep + "nickname",
			"--" + sep + "----" + sep + "--------",
			"1" + sep + "Ada" + sep + "NULL",
			"2" + sep + "Grace" + sep + "Gigi",
		}, "\n") + "\n"
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rows.Columns, []string{"id", "name", "nickname"}) {
			t.Fatalf("columns = %v", rows.Columns)
		}
		if len(rows.Rows) != 2 {
			t.Fatalf("rows = %d", len(rows.Rows))
		}
		// v1 accepts: Path A NULL is the literal string "NULL", never nil.
		if rows.Rows[0][2] == nil || *rows.Rows[0][2] != "NULL" {
			t.Fatalf("cell = %v", rows.Rows[0][2])
		}
	})

	t.Run("value containing comma survives 0x1F split", func(t *testing.T) {
		out := "note\n----\na, b, c\n"
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if *rows.Rows[0][0] != "a, b, c" {
			t.Fatalf("cell = %q", *rows.Rows[0][0])
		}
	})

	t.Run("crlf normalized", func(t *testing.T) {
		out := "id\r\n--\r\n7\r\n"
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if *rows.Rows[0][0] != "7" {
			t.Fatalf("cell = %q", *rows.Rows[0][0])
		}
	})

	t.Run("single-column empty-string rows survive", func(t *testing.T) {
		// Real go-sqlcmd shape: header, rule, data rows (an empty-string
		// value in a single-column result prints as a blank line under
		// -W), then one blank trailer line after the result set.
		out := "a\n-\n\nx\n\n"
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Rows) != 2 {
			t.Fatalf("rows = %d, want 2 (empty-string row must not vanish)", len(rows.Rows))
		}
		if rows.Rows[0][0] == nil || *rows.Rows[0][0] != "" {
			t.Fatalf("cell = %v, want non-nil empty string", rows.Rows[0][0])
		}
		if *rows.Rows[1][0] != "x" {
			t.Fatalf("cell = %q", *rows.Rows[1][0])
		}
	})

	t.Run("trailing empty-string row survives trailer strip", func(t *testing.T) {
		out := "a\n-\nx\n\n\n" // rows "x", ""; then trailer blank line
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(rows.Rows))
		}
		if rows.Rows[1][0] == nil || *rows.Rows[1][0] != "" {
			t.Fatalf("cell = %v, want non-nil empty string", rows.Rows[1][0])
		}
	})

	t.Run("zero-row result set is empty", func(t *testing.T) {
		out := "a\n-\n\n" // header, rule, trailer blank line, no data
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Rows) != 0 {
			t.Fatalf("rows = %d, want 0", len(rows.Rows))
		}
	})

	t.Run("empty output is empty rows", func(t *testing.T) {
		rows, err := a.Parse(executor.RawResult{Stdout: []byte("\n")})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Columns) != 0 {
			t.Fatalf("rows = %+v", rows)
		}
	})

	t.Run("missing header rule is an error", func(t *testing.T) {
		if _, err := a.Parse(executor.RawResult{Stdout: []byte("id\n7\n")}); err == nil {
			t.Fatal("want header-rule error")
		}
	})

	t.Run("nonzero exit is a parse error", func(t *testing.T) {
		_, err := a.Parse(executor.RawResult{ExitCode: 1, Stderr: []byte("Msg 102, Level 15")})
		if err == nil {
			t.Fatal("want error")
		}
	})
}

func TestSqlserverIsSchemaError(t *testing.T) {
	a := sqlserverAdapter{}
	cases := []struct {
		out  string
		exit int
		want bool
	}{
		{"Msg 207, Level 16, State 1, Server x\nInvalid column name 'nope'.", 1, true},
		{"Msg 208, Level 16, State 1\nInvalid object name 'ghosts'.", 1, true},
		{"Invalid column name 'nope'.", 1, true}, // go-sqlcmd: no Msg prefix
		{"Invalid object name 'ghosts'.", 1, true},
		{"Msg 102, Level 15, State 1\nIncorrect syntax near 'SELEC'.", 1, false},
		{"Msg 207, ...", 0, false},
	}
	for _, c := range cases {
		got := a.IsSchemaError(executor.RawResult{ExitCode: c.exit, Stderr: []byte(c.out)})
		if got != c.want {
			t.Errorf("IsSchemaError(%q) = %v, want %v", c.out, got, c.want)
		}
	}
	// Some sqlcmd builds print errors to stdout when -r isn't honored.
	if !a.IsSchemaError(executor.RawResult{ExitCode: 1, Stdout: []byte("Msg 207, Level 16")}) {
		t.Error("schema errors on stdout must also be detected")
	}
}

func TestSQLServerPreviewSQL(t *testing.T) {
	cases := map[string]string{
		"dbo.orders":     `SELECT TOP 100 * FROM [dbo].[orders];`,
		"orders":         `SELECT TOP 100 * FROM [orders];`,
		"dbo.Order Item": `SELECT TOP 100 * FROM [dbo].[Order Item];`, // mixed case and a space survive quoting
		"dbo.we]ird":     `SELECT TOP 100 * FROM [dbo].[we]]ird];`,    // an embedded bracket is doubled
	}
	for table, want := range cases {
		if got := (sqlserverAdapter{}).PreviewSQL(table); got != want {
			t.Errorf("PreviewSQL(%q) = %q, want %q", table, got, want)
		}
	}
}

// TestSQLServerListDatabasesSQL pins the candidate-list predicates. tempdb is
// excluded by name rather than by database_id: the familiar master=1, tempdb=2
// mapping is not documented by Microsoft as a stable contract (design.md §13.9).
func TestSQLServerListDatabasesSQL(t *testing.T) {
	sql := sqlserverAdapter{}.ListDatabasesSQL()
	for _, want := range []string{
		"sys.databases",
		"state_desc = 'ONLINE'",
		"name <> 'tempdb'",
		"HAS_DBACCESS(name) = 1",
		"ORDER BY name",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("list-databases sql missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "database_id") {
		t.Fatalf("tempdb must be excluded by name, not database_id:\n%s", sql)
	}
	if strings.Count(sql, "SELECT") != 1 || strings.Contains(sql, ",\n") {
		t.Fatalf("list-databases sql must select exactly one column:\n%s", sql)
	}
}
