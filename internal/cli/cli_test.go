package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakePsql puts a stub psql on PATH that records its argv/stdin/env and
// prints canned CSV, so the full pipeline runs without a server.
func fakePsql(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "psql", "#!/bin/sh\n"+script, 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func testConfig(t *testing.T) string {
	t.Helper()
	return writeFile(t, t.TempDir(), "config.toml", `
[hosts.testpg]
provider   = "postgres"
host       = "localhost"
port       = 5432
database   = "testdb"
username   = "app"
credential = "env:DBQ_TEST_PW"
`, 0o600)
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestHelpAndUsage(t *testing.T) {
	code, out, _ := run(t, "help")
	if code != 0 || !strings.Contains(out, "db-query") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
	code, _, errb := run(t, "frobnicate")
	if code != 1 || !strings.Contains(errb, "unknown command") {
		t.Fatalf("unknown command: code=%d err=%q", code, errb)
	}
	if code, _, _ := run(t); code != 1 {
		t.Fatal("no args must exit 1")
	}
}

func TestHostsCommand(t *testing.T) {
	cfg := testConfig(t)
	code, out, errb := run(t, "hosts", "--config", cfg)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "testpg") || !strings.Contains(out, "postgres") {
		t.Fatalf("out = %q", out)
	}
}

func TestQueryHappyPath(t *testing.T) {
	dir := fakePsql(t, `
cat > "$TMPDIR_CAPTURE/stdin"
env > "$TMPDIR_CAPTURE/env"
printf 'id,name\n1,Ada\n'
`)
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	t.Setenv("DBQ_TEST_PW", "sekrit")
	_ = dir

	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT id, name FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "id\tname\n1\tAda\n" {
		t.Fatalf("out = %q", out)
	}

	stdin, _ := os.ReadFile(filepath.Join(capture, "stdin"))
	if string(stdin) != "SELECT id, name FROM people" {
		t.Fatalf("sql over stdin = %q", stdin)
	}
	envDump, _ := os.ReadFile(filepath.Join(capture, "env"))
	for _, want := range []string{"PGPASSWORD=sekrit", "PGUSER=app", "PGDATABASE=testdb", "PGHOST=localhost"} {
		if !strings.Contains(string(envDump), want) {
			t.Errorf("child env missing %q", want)
		}
	}
}

func TestQueryJSONOutput(t *testing.T) {
	fakePsql(t, `printf 'id,nick\n1,\001\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg, "--output", "json", "SELECT 1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	var rows []map[string]*string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if rows[0]["nick"] != nil {
		t.Fatal("sentinel must become JSON null")
	}
}

func TestQueryMissingCredential(t *testing.T) {
	fakePsql(t, `printf 'x\n'`)
	t.Setenv("DBQ_TEST_PW", "x") // register restore, then unset for real
	os.Unsetenv("DBQ_TEST_PW")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT 1")
	if code != 1 || !strings.Contains(errb, "DBQ_TEST_PW") {
		t.Fatalf("code=%d err=%q (must name the missing variable)", code, errb)
	}
}

func TestQuerySchemaErrorHint(t *testing.T) {
	fakePsql(t, `echo 'ERROR:  42703: column "nope" does not exist' >&2; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT nope FROM people")
	if code != 1 {
		t.Fatalf("code = %d, want client's exit code", code)
	}
	if !strings.Contains(errb, "introspect") {
		t.Fatalf("schema error must hint at introspection, err=%q", errb)
	}
}

func TestQueryJSONErrorIsStructured(t *testing.T) {
	fakePsql(t, `echo 'ERROR: boom' >&2; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "--output", "json", "SELECT 1")
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	var doc map[string]string
	if err := json.Unmarshal([]byte(errb), &doc); err != nil {
		t.Fatalf("json-mode error must be structured JSON, got %q", errb)
	}
	if !strings.Contains(doc["error"], "boom") {
		t.Fatalf("doc = %v", doc)
	}
}

func TestQueryClientNotFound(t *testing.T) {
	// Empty PATH: psql cannot start → distinct exit code 2.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT 1")
	if code != 2 || !strings.Contains(errb, "starting") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestIntrospectUsesAdapterSQL(t *testing.T) {
	fakePsql(t, `
cat > "$TMPDIR_CAPTURE/stdin"
printf 'table_name,column_name\npeople,id\n'
`)
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "introspect", "--host", "testpg", "--config", cfg)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "people") {
		t.Fatalf("out = %q", out)
	}
	stdin, _ := os.ReadFile(filepath.Join(capture, "stdin"))
	if !strings.Contains(string(stdin), "information_schema.columns") {
		t.Fatalf("introspect must send the adapter's catalog query, sent %q", stdin)
	}
}

func TestQueryParamPassing(t *testing.T) {
	fakePsql(t, `
printf '%s ' "$@" > "$TMPDIR_CAPTURE/argv"
printf 'ok\n1\n'
`)
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--param", "name=Ada", "--param", "min=2", "SELECT :'name', :'min'")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	argv, _ := os.ReadFile(filepath.Join(capture, "argv"))
	if !strings.Contains(string(argv), "-v min=2") || !strings.Contains(string(argv), "-v name=Ada") {
		t.Fatalf("argv = %q", argv)
	}
}

func TestReadSQLSources(t *testing.T) {
	t.Run("positional", func(t *testing.T) {
		sql, err := readSQL([]string{"SELECT 1"}, "")
		if err != nil || sql != "SELECT 1" {
			t.Fatalf("sql=%q err=%v", sql, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		path := writeFile(t, t.TempDir(), "q.sql", "SELECT 2", 0o600)
		sql, err := readSQL(nil, path)
		if err != nil || sql != "SELECT 2" {
			t.Fatalf("sql=%q err=%v", sql, err)
		}
	})
	t.Run("both is an error", func(t *testing.T) {
		if _, err := readSQL([]string{"SELECT 1"}, "f.sql"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("multiple positionals is an error", func(t *testing.T) {
		if _, err := readSQL([]string{"SELECT", "1"}, ""); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("missing file is an error", func(t *testing.T) {
		if _, err := readSQL(nil, "/nonexistent/q.sql"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty positional is an error, not an introspect run", func(t *testing.T) {
		if _, err := readSQL([]string{"  "}, ""); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty file is an error, not an introspect run", func(t *testing.T) {
		path := writeFile(t, t.TempDir(), "empty.sql", "\n", 0o600)
		if _, err := readSQL(nil, path); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestParamFlagParsing(t *testing.T) {
	p := paramFlags{}
	if err := p.Set("k=v"); err != nil {
		t.Fatal(err)
	}
	if err := p.Set("k2=has=equals"); err != nil {
		t.Fatal(err)
	}
	if p["k2"] != "has=equals" {
		t.Fatalf("p = %v", p)
	}
	if err := p.Set("noequals"); err == nil {
		t.Fatal("want error for missing =")
	}
	if err := p.Set("=v"); err == nil {
		t.Fatal("want error for empty key")
	}
}

func TestUnknownHostAndFormat(t *testing.T) {
	cfg := testConfig(t)
	if code, _, errb := run(t, "query", "--host", "nope", "--config", cfg, "SELECT 1"); code != 1 || !strings.Contains(errb, "unknown host") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if code, _, _ := run(t, "query", "--host", "testpg", "--config", cfg, "--output", "yaml", "SELECT 1"); code != 1 {
		t.Fatal("unknown format must fail")
	}
	if code, _, errb := run(t, "query", "--config", cfg, "SELECT 1"); code != 1 || !strings.Contains(errb, "--host") {
		t.Fatalf("missing --host: code=%d err=%q", code, errb)
	}
}
