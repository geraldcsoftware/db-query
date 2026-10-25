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
	t.Run("a positional param binds server-side", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg",
			"--param", "1=Grace",
			"SELECT id FROM people WHERE name = $1")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if res.stdout != "id\n2\n" {
			t.Fatalf("stdout = %q", res.stdout)
		}
	})
	t.Run("the server infers a param's type from where it is used", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg",
			"--param", "1=2", "--param", "2=Edsger",
			"SELECT name FROM people WHERE id > $1 AND name = $2")
		if res.code != 0 || res.stdout != "name\nEdsger\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("hostile param value is data, never SQL", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg",
			"--param", "1=x'; DROP TABLE people; --",
			"SELECT count(*) AS n FROM people WHERE name = $1")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		if res.stdout != "n\n0\n" {
			t.Fatalf("stdout = %q, want zero matches", res.stdout)
		}
		// The table must have survived the attempt.
		res = runTool(t, nil, "query", "--host", "pg", "SELECT count(*) FROM people")
		if res.code != 0 || !strings.Contains(res.stdout, "3") {
			t.Fatalf("people table damaged? code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("an unbound position is refused before connecting", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELECT $1")
		if res.code != 1 || !strings.Contains(res.stderr, "$1 is not bound") {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
	})
}

func TestPostgresScripts(t *testing.T) {
	pgReady(t)
	t.Run("every result set is printed, in order", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "--param", "1=1",
			"SELECT name FROM people WHERE id = $1; SELECT count(*) AS n FROM people; SELECT name FROM people WHERE id = $1 + 1")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		want := "name\nAda\n\nn\n3\n\nname\nGrace\n"
		if res.stdout != want {
			t.Fatalf("stdout = %q, want %q", res.stdout, want)
		}
	})
	t.Run("json nests one array per result set", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "--output", "json",
			"SELECT 1 AS a; SELECT 'x' AS b, NULL AS c")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		var sets [][]map[string]*string
		if err := json.Unmarshal([]byte(res.stdout), &sets); err != nil {
			t.Fatalf("not an array of result sets: %v\n%s", err, res.stdout)
		}
		if len(sets) != 2 || *sets[0][0]["a"] != "1" || *sets[1][0]["b"] != "x" || sets[1][0]["c"] != nil {
			t.Fatalf("sets = %s", res.stdout)
		}
	})
	t.Run("the statements share one transaction", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg",
			"SELECT txid_current() AS a; SELECT txid_current() AS b")
		if res.code != 0 {
			t.Fatalf("code=%d stderr=%s", res.code, res.stderr)
		}
		parts := strings.Split(strings.TrimSpace(res.stdout), "\n\n")
		if len(parts) != 2 || strings.Split(parts[0], "\n")[1] != strings.Split(parts[1], "\n")[1] {
			t.Fatalf("each statement ran in its own transaction: %q", res.stdout)
		}
	})
	t.Run("the first error stops the script and rolls it back", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pgrw",
			"INSERT INTO people (id, name) VALUES (90, 'Rolled back'); SELECT 1/0 AS boom; INSERT INTO people (id, name) VALUES (91, 'Never sent')")
		if res.code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stderr, "statement 2 of 3") || !strings.Contains(res.stderr, "22012") {
			t.Fatalf("stderr = %s, want the failing statement and its SQLSTATE", res.stderr)
		}
		if res.stdout != "" {
			t.Fatalf("a failed script prints nothing, stdout = %q", res.stdout)
		}
		res = runTool(t, nil, "query", "--host", "pg", "SELECT count(*) AS n FROM people WHERE id >= 90")
		if res.code != 0 || res.stdout != "n\n0\n" {
			t.Fatalf("the first INSERT survived the failure: code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("a script that succeeds commits", func(t *testing.T) {
		t.Cleanup(func() { seedPG(t, "DELETE FROM people WHERE id >= 90") })
		res := runTool(t, nil, "query", "--host", "pgrw", "--param", "1=92",
			"INSERT INTO people (id, name) VALUES ($1, 'Committed'); SELECT name FROM people WHERE id = $1")
		if res.code != 0 || res.stdout != "name\nCommitted\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
		res = runTool(t, nil, "query", "--host", "pg", "SELECT name FROM people WHERE id = 92")
		if res.code != 0 || res.stdout != "name\nCommitted\n" {
			t.Fatalf("the script's INSERT was not committed: code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("a read-only host's session refuses writes", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SHOW default_transaction_read_only")
		if res.code != 0 || res.stdout != "default_transaction_read_only\non\n" {
			t.Fatalf("code=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
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
	t.Run("comma-containing value survives intact", func(t *testing.T) {
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
	// anything it cannot parse before a connection is ever opened (next case).
	t.Run("a non-schema SQL error exits 4", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELECT 1/0 AS boom")
		if res.code != 4 {
			t.Fatalf("code = %d, want 4 (other SQL error); stderr=%s", res.code, res.stderr)
		}
	})
	// What the old "non-schema error" case was really covering: SELEC is
	// unparseable, so it is refused as opaque and never sent. Worth
	// keeping, under a name that says so.
	t.Run("unparseable SQL is refused, exit 5", func(t *testing.T) {
		res := runTool(t, nil, "query", "--host", "pg", "SELEC 1")
		if res.code != 5 {
			t.Fatalf("code = %d, want 5 (refused by the safety gate); stderr=%s", res.code, res.stderr)
		}
	})
}
