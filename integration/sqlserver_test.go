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

		seed := []struct{ host, sql string }{
			{"mssql-master", "IF DB_ID('dbqtest') IS NULL CREATE DATABASE dbqtest"},
			{"mssql", "IF OBJECT_ID('people') IS NOT NULL DROP TABLE people"},
			{"mssql", `CREATE TABLE people (
				id int PRIMARY KEY,
				name nvarchar(100) NOT NULL,
				nickname nvarchar(100) NULL,
				note nvarchar(200) NULL)`},
			{"mssql", `INSERT INTO people (id, name, nickname, note) VALUES
				(1, 'Ada', NULL, 'first programmer'),
				(2, 'Grace', '', 'compiler pioneer'),
				(3, 'Edsger', 'EWD', 'structured, humble, precise')`},
		}
		for _, s := range seed {
			res := runTool(t, nil, "query", "--host", s.host, "--timeout", "30s", s.sql)
			if res.code != 0 {
				t.Fatalf("seeding %q failed (code=%d): %s", s.sql, res.code, res.stderr)
			}
		}
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

func TestSQLServerVariableResolution(t *testing.T) {
	mssqlReady(t)
	t.Run("param binds via sqlcmd -v scripting variable", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "who=Grace",
			"SELECT id FROM people WHERE name = '$(who)'")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "2") {
			t.Fatalf("stdout = %q", res.stdout)
		}
	})
	t.Run("numeric param", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "min=2",
			"SELECT count(*) AS n FROM people WHERE id >= $(min)")
		if res.code != 0 || !strings.Contains(res.stdout, "2") {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("unsafe param value is rejected before reaching sqlcmd", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql",
			"--param", "who=x'; DROP TABLE people; --",
			"SELECT id FROM people WHERE name = '$(who)'")
		if res.code == 0 {
			t.Fatal("unsafe value must be rejected")
		}
		if !strings.Contains(res.stderr, "unsafe") {
			t.Fatalf("stderr = %s", res.stderr)
		}
		// Rejection must happen client-side: the table is untouched.
		check := runTool(t, nil, "query", "--host", "mssql", "SELECT count(*) AS n FROM people")
		if check.code != 0 || !strings.Contains(check.stdout, "3") {
			t.Fatalf("people table damaged? %+v", check)
		}
	})
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
		// v1 accepted limitation: Path A prints NULL as the literal
		// string "NULL" — never a JSON null.
		if rows[0]["nickname"] == nil || *rows[0]["nickname"] != "NULL" {
			t.Fatalf("nickname = %v (v1 Path A renders NULL literally)", rows[0]["nickname"])
		}
		if *rows[2]["nickname"] != "EWD" {
			t.Fatalf("nickname = %v", rows[2]["nickname"])
		}
	})
	t.Run("comma-containing value survives the 0x1F separator", func(t *testing.T) {
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
	t.Run("schema error carries an introspection hint", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "mssql", "SELECT no_such_column FROM people")
		if res.code == 0 {
			t.Fatal("must fail")
		}
		if !strings.Contains(res.stderr, "introspect") {
			t.Fatalf("schema error should hint at introspection: %s", res.stderr)
		}
	})
}
