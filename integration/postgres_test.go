//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

var pgOnce sync.Once

func pgReady(t testing.TB) {
	pgOnce.Do(func() { waitReady(t, "pg", 90*time.Second) })
}

func TestPostgresBasicQuery(t *testing.T) {
	pgReady(t)
	res := runTool(t, nil, "query", "--host", "pg", "SELECT id, name FROM people ORDER BY id")
	if res.code != 0 {
		t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
	}
	want := "id\tname\n1\tAda\n2\tGrace\n3\tEdsger\n"
	if res.stdout != want {
		t.Fatalf("stdout = %q, want %q", res.stdout, want)
	}
}

func TestPostgresCredentialFromEnv(t *testing.T) {
	pgReady(t)
	t.Run("missing env var is a clear error", func(t *testing.T) {
		res := runTool(t, envWithout("DBQ_PG_PASSWORD"), "query", "--host", "pg", "SELECT 1")
		if res.code == 0 {
			t.Fatal("must fail without the credential env var")
		}
		if !strings.Contains(res.stderr, "DBQ_PG_PASSWORD") {
			t.Fatalf("error must name the missing variable, got: %s", res.stderr)
		}
	})
	t.Run("wrong password is rejected by the server", func(t *testing.T) {
		env := append(envWithout("DBQ_PG_PASSWORD"), "DBQ_PG_PASSWORD=not-the-password")
		res := runTool(t, env, "query", "--host", "pg", "SELECT 1")
		if res.code == 0 {
			t.Fatal("wrong password must fail")
		}
		if !strings.Contains(res.stderr, "authentication") && !strings.Contains(res.stderr, "password") {
			t.Fatalf("stderr = %s", res.stderr)
		}
	})
	t.Run("correct password from env works", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELECT current_user")
		if res.code != 0 || !strings.Contains(res.stdout, "dbq") {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
}

func TestPostgresVariableResolution(t *testing.T) {
	pgReady(t)
	t.Run("param binds via psql -v", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg",
			"--param", "who=Grace",
			"SELECT id FROM people WHERE name = :'who'")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "2") {
			t.Fatalf("stdout = %q", res.stdout)
		}
	})
	t.Run("hostile param value is quoted safely, not executed", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg",
			"--param", "who=x'; DROP TABLE people; --",
			"SELECT count(*) AS n FROM people WHERE name = :'who'")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "0") {
			t.Fatalf("stdout = %q, want zero matches", res.stdout)
		}
		// The table must have survived the attempt.
		res = runTool(t, nil, "query", "--host", "pg", "SELECT count(*) FROM people")
		if res.code != 0 || !strings.Contains(res.stdout, "3") {
			t.Fatalf("people table damaged? code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
}

func TestPostgresOutputFormats(t *testing.T) {
	pgReady(t)
	t.Run("json with faithful NULL and empty string", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "--output", "json",
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
		if rows[0]["nickname"] != nil {
			t.Fatal("Ada's nickname must be JSON null")
		}
		if rows[1]["nickname"] == nil || *rows[1]["nickname"] != "" {
			t.Fatal("Grace's nickname must be \"\", not null")
		}
		if *rows[2]["nickname"] != "EWD" {
			t.Fatalf("nickname = %v", rows[2]["nickname"])
		}
	})
	t.Run("comma-containing value survives csv parsing", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "--output", "json",
			"SELECT note FROM people WHERE id = 3")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "structured, humble, precise") {
			t.Fatalf("stdout = %q", res.stdout)
		}
	})
	t.Run("json error path is structured", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "--output", "json", "SELECT * FROM ghosts")
		if res.code == 0 {
			t.Fatal("query against missing table must fail")
		}
		var doc map[string]string
		if err := json.Unmarshal([]byte(res.stderr), &doc); err != nil {
			t.Fatalf("json-mode error must be structured, got: %s", res.stderr)
		}
	})
	t.Run("text is the default", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELECT 42 AS answer")
		if res.code != 0 || res.stdout != "answer\n42\n" {
			t.Fatalf("stdout = %q stderr=%s", res.stdout, res.stderr)
		}
	})
}

func TestPostgresIntrospection(t *testing.T) {
	pgReady(t)
	t.Run("introspect lists seeded schema", func(t *testing.T) {
		res := runTool(t, nil, "introspect", "--host", "pg")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		for _, want := range []string{"people", "id", "name", "nickname", "note", "integer", "text"} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("introspection output missing %q:\n%s", want, res.stdout)
			}
		}
	})
	// The next three assert exit codes rather than message text. The 3/4/5
	// split is the contract: internal/cli/cli.go lists it and
	// internal/cli/cli_test.go pins each code against a stubbed client,
	// while the wording that travels alongside a code is free to move, and
	// did. The first two of these once asserted a hint reading "introspect";
	// the schema cache reworded it to --refresh-schema and left both cases
	// red against a tool that was behaving correctly.
	t.Run("a schema error exits 3", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELECT no_such_column FROM people")
		if res.code != 3 {
			t.Fatalf("code = %d, want 3 (schema error); stderr=%s", res.code, res.stderr)
		}
	})
	// Division by zero rather than a syntax error: proving the 3-vs-4 split
	// needs a statement that reaches the server, and the safety gate stops
	// anything it cannot parse before psql is ever started (next case).
	t.Run("a non-schema SQL error exits 4", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELECT 1/0 AS boom")
		if res.code != 4 {
			t.Fatalf("code = %d, want 4 (other SQL error); stderr=%s", res.code, res.stderr)
		}
	})
	// What the old "non-schema error" case was really covering: SELEC is
	// unparseable, so it is refused as opaque and psql never runs. Worth
	// keeping, under a name that says so.
	t.Run("unparseable SQL is refused, exit 5", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELEC 1")
		if res.code != 5 {
			t.Fatalf("code = %d, want 5 (refused by the safety gate); stderr=%s", res.code, res.stderr)
		}
	})
}
