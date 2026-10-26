//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

var mssqlOnce sync.Once

// mssqlReady waits for SQL Server and seeds the dbqtest database using
// the tool itself (the container has no init-script mount point).
func mssqlReady(t testing.TB) {
	mssqlOnce.Do(func() {
		waitReady(t, "mssql-master", 180*time.Second)

		// The fixture is loaded through the driver rather than with db-query, which
		// refuses to build it: CREATE and DROP classify as destructive and
		// CREATE DATABASE as admin, and sqlscan.Class.Permitted allows only
		// reads on a read-only host and only reads and writes on a writable
		// one, so no config setting opens this path. What would is the
		// operator challenge, and that wants a /dev/tty no runner has.
		// Postgres meets the same wall and answers it the same way, with a
		// script the container runs for itself
		// (integration/init/postgres/01-init.sql); the SQL Server image has
		// no equivalent mount point, so its fixture is loaded here instead.
		// Building it with the subject under test would mean weakening that
		// gate or leaving this suite permanently red.
		seedMSSQL(t, "master", "IF DB_ID('dbqtest') IS NULL CREATE DATABASE dbqtest")
		seedMSSQL(t, "dbqtest", "IF OBJECT_ID('people') IS NOT NULL DROP TABLE people")
		seedMSSQL(t, "dbqtest", `CREATE TABLE people (
			id int PRIMARY KEY,
			name nvarchar(100) NOT NULL,
			nickname nvarchar(100) NULL,
			note nvarchar(200) NULL)`)
		seedMSSQL(t, "dbqtest", `INSERT INTO people (id, name, nickname, note) VALUES
			(1, 'Ada', NULL, 'first programmer'),
			(2, 'Grace', '', 'compiler pioneer'),
			(3, 'Edsger', 'EWD', 'structured, humble, precise')`)
	})
}

func TestSQLServerBasicQuery(t *testing.T) {
	mssqlReady(t)
	res := runTool(t, nil, "query", "--host", "mssql", "SELECT id, name FROM people ORDER BY id")
	if res.code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", res.code, res.stderr, res.stdout)
	}
	want := "id\tname\n1\tAda\n2\tGrace\n3\tEdsger\n"
	if res.stdout != want {
		t.Fatalf("stdout = %q, want %q", res.stdout, want)
	}
}

func TestSQLServerCredentialFromEnv(t *testing.T) {
	mssqlReady(t)
	t.Run("missing env var is a clear error", func(t *testing.T) {
		res := runTool(t, envWithout("DBQ_MSSQL_PASSWORD"), "query", "--host", "mssql", "SELECT 1")
		if res.code == 0 {
			t.Fatal("must fail without the credential env var")
		}
		if !strings.Contains(res.stderr, "DBQ_MSSQL_PASSWORD") {
			t.Fatalf("error must name the missing variable, got: %s", res.stderr)
		}
	})
	t.Run("wrong password is rejected by the server", func(t *testing.T) {
		env := append(envWithout("DBQ_MSSQL_PASSWORD"), "DBQ_MSSQL_PASSWORD=Wrong-Password1")
		res := runTool(t, env, "query", "--host", "mssql", "SELECT 1")
		if res.code == 0 {
			t.Fatal("wrong password must fail")
		}
		if !strings.Contains(strings.ToLower(res.stderr), "login") {
			t.Fatalf("stderr = %s", res.stderr)
		}
	})
	t.Run("correct password from env works", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "SELECT SUSER_SNAME() AS who")
		if res.code != 0 || !strings.Contains(res.stdout, "sa") {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
}

func TestSQLServerParams(t *testing.T) {
	mssqlReady(t)
	t.Run("named param binds server-side", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "who=Grace",
			"SELECT id FROM people WHERE name = @who")
		if res.code != 0 || res.stdout != "id\n2\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("a value compared with an int column converts", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "min=2",
			"SELECT count(*) AS n FROM people WHERE id >= @min")
		if res.code != 0 || res.stdout != "n\n2\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("a hostile value is data, not SQL", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "who=x'; DROP TABLE people; --",
			"SELECT id FROM people WHERE name = @who")
		if res.code != 0 || res.stdout != "id\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
		check := runTool(t, nil, "query", "--host", "mssql", "SELECT count(*) AS n FROM people")
		if check.code != 0 || check.stdout != "n\n3\n" {
			t.Fatalf("people table damaged? %+v", check)
		}
	})
	t.Run("a batch's own variable is not a param", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "who=Ada",
			"DECLARE @n int = 1; SELECT name FROM people WHERE id = @n AND name = @who")
		// Planned statement by statement, the SELECT does not compile without
		// the DECLARE before it, so the pre-check refuses it: exit 5.
		if res.code != 5 {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("the old $(name) spelling is a plain literal now", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "who=Grace",
			"SELECT id FROM people WHERE name = '$(who)'")
		// Nothing substitutes it any more, so the query runs and matches
		// nothing. Saved queries written for sqlcmd need rewriting.
		if res.code != 0 || res.stdout != "id\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("an @-prefixed --param name is pointed at the right spelling", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "@who=Grace",
			"SELECT id FROM people WHERE name = @who")
		if res.code == 0 || !strings.Contains(res.stderr, "--param who=value") {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
	})
}

func TestSQLServerScripts(t *testing.T) {
	mssqlReady(t)
	t.Run("every result set is returned", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"SELECT 1 AS a; SELECT 2 AS b")
		if res.code != 0 || res.stdout != "a\n1\n\nb\n2\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("a failing batch is named", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"SELECT 1 AS a\nGO\nSELECT 1/0 AS boom")
		if res.code == 0 || !strings.Contains(res.stderr, "batch 2 of 2") || !strings.Contains(res.stderr, "Msg 8134") {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
	})
}

func TestSQLServerValues(t *testing.T) {
	mssqlReady(t)
	res := runTool(t, nil, "query", "--host", "mssql", "--output", "json", `SELECT
		CAST(1.50 AS decimal(10,2)) AS d,
		CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier) AS g,
		0x0AFF AS b,
		CAST(1 AS bit) AS bt,
		CAST('2026-10-26T09:05:07.1234567' AS datetime2) AS dt2,
		CAST('2026-10-26T09:05:07.123' AS datetime) AS dt,
		CAST('2026-10-26' AS date) AS dd,
		REPLICATE(CAST('x' AS nvarchar(max)), 9000) AS long_text`)
	if res.code != 0 {
		t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
	}
	var rows []map[string]*string
	if err := json.Unmarshal([]byte(res.stdout), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("not one JSON row: %v\n%s", err, res.stdout)
	}
	want := map[string]string{
		"d":   "1.50",
		"g":   "6F9619FF-8B86-D011-B42D-00C04FC964FF",
		"b":   "0x0AFF",
		"bt":  "1",
		"dt2": "2026-10-26 09:05:07.1234567",
		"dt":  "2026-10-26 09:05:07.123",
		"dd":  "2026-10-26",
	}
	for k, v := range want {
		if rows[0][k] == nil || *rows[0][k] != v {
			t.Errorf("%s = %v, want %q", k, rows[0][k], v)
		}
	}
	// sqlcmd truncated variable-length values at 8000 characters.
	if rows[0]["long_text"] == nil || len(*rows[0]["long_text"]) != 9000 {
		t.Errorf("long_text was truncated")
	}
}

// TestSQLServerPrecheckStaysPlanOnly guards the planner's one hard promise:
// it compiles and never runs. SHOWPLAN_XML executes a SET SHOWPLAN_XML OFF it
// is handed, so a planner that set plan-only once per connection would have
// run every statement after one, during a check meant to run nothing.
func TestSQLServerPrecheckStaysPlanOnly(t *testing.T) {
	mssqlReady(t)
	seedMSSQL(t, "dbqtest", "IF OBJECT_ID('planner_breach') IS NOT NULL DROP TABLE planner_breach")
	res := runTool(t, nil, "query", "--host", "mssql", "--dry-run",
		"SET SHOWPLAN_XML OFF; SELECT * INTO planner_breach FROM people")
	if res.code != 0 {
		t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, `"SELECT INTO"`) {
		t.Errorf("the SELECT INTO was not planned: %s", res.stdout)
	}
	check := runTool(t, nil, "query", "--host", "mssql",
		"SELECT count(*) AS n FROM sys.tables WHERE name = 'planner_breach'")
	if check.code != 0 || check.stdout != "n\n0\n" {
		seedMSSQL(t, "dbqtest", "IF OBJECT_ID('planner_breach') IS NOT NULL DROP TABLE planner_breach")
		t.Fatalf("the pre-check executed a statement: %+v", check)
	}
}

func TestSQLServerOutputFormats(t *testing.T) {
	mssqlReady(t)
	t.Run("json output", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "--output", "json",
			"SELECT name, nickname FROM people ORDER BY id")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		var rows []map[string]*string
		if err := json.Unmarshal([]byte(res.stdout), &rows); err != nil {
			t.Fatalf("not valid JSON: %v\n%s", err, res.stdout)
		}
		if len(rows) != 3 {
			t.Fatalf("rows = %d", len(rows))
		}
		// NULL is a JSON null, and an empty string stays an empty string.
		if rows[0]["nickname"] != nil {
			t.Fatalf("nickname = %q, want null", *rows[0]["nickname"])
		}
		if rows[1]["nickname"] == nil || *rows[1]["nickname"] != "" {
			t.Fatalf("nickname = %v, want the empty string", rows[1]["nickname"])
		}
		if *rows[2]["nickname"] != "EWD" {
			t.Fatalf("nickname = %v", rows[2]["nickname"])
		}
	})
	t.Run("comma-containing value survives", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "--output", "json",
			"SELECT note FROM people WHERE id = 3")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "structured, humble, precise") {
			t.Fatalf("stdout = %q", res.stdout)
		}
	})
	t.Run("json error path is structured", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "--output", "json", "SELECT * FROM ghosts")
		if res.code == 0 {
			t.Fatal("query against missing table must fail")
		}
		var doc map[string]string
		if err := json.Unmarshal([]byte(res.stderr), &doc); err != nil {
			t.Fatalf("json-mode error must be structured, got: %s", res.stderr)
		}
	})
	t.Run("text is the default", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "SELECT 42 AS answer")
		if res.code != 0 || res.stdout != "answer\n42\n" {
			t.Fatalf("stdout = %q stderr=%s", res.stdout, res.stderr)
		}
	})
}

func TestSQLServerIntrospection(t *testing.T) {
	mssqlReady(t)
	t.Run("introspect lists seeded schema", func(t *testing.T) {
		res := runTool(t, nil, "introspect", "--host", "mssql")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		for _, want := range []string{"people", "id", "name", "nickname", "note", "int", "nvarchar"} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("introspection output missing %q:\n%s", want, res.stdout)
			}
		}
	})
	// Exit 5, where the Postgres suite asserts 3 for the same query, and the
	// difference is structural rather than a quirk. SQL Server has no local
	// grammar, so every statement is classified by asking the engine for a
	// plan, and SHOWPLAN_XML cannot compile one naming a column that does
	// not exist. ParsePlan reads that failure as opaque ("did not compile"),
	// opaque is permitted on no host, and the query is refused before it
	// runs for real. The invalid column is still caught, one layer earlier
	// than on Postgres, which is why exit 3 is not reachable here.
	//
	// This case asserted a hint reading "introspect" until the schema cache
	// reworded it, and the wording it moved to was never the reason it
	// failed: the code is the stable claim.
	t.Run("an invalid column is caught by the pre-check, exit 5", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "SELECT no_such_column FROM people")
		if res.code != 5 {
			t.Fatalf("code = %d, want 5 (refused: the plan does not compile); stderr=%s", res.code, res.stderr)
		}
	})
}
