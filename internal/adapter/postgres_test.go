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

func TestPostgresEnvExplicitDefaults(t *testing.T) {
	a := postgresAdapter{}
	cred := credential.Credential{Username: "app", Password: "pw"}

	t.Run("all provider vars set even when config is minimal", func(t *testing.T) {
		env := a.Env(cred, config.HostConfig{})
		want := map[string]string{
			"PGHOST": "localhost", "PGPORT": "5432", "PGDATABASE": "app",
			"PGUSER": "app", "PGPASSWORD": "pw", "PGAPPNAME": "db-query",
			"PGSSLMODE": "prefer", // libpq default, pinned so an inherited PGSSLMODE can't leak in
		}
		if !reflect.DeepEqual(env, want) {
			t.Fatalf("env = %v, want %v (every var explicit blocks inherited leakage)", env, want)
		}
	})
	t.Run("config values flow through", func(t *testing.T) {
		env := a.Env(cred, config.HostConfig{
			Host: "db.internal", Port: 5433, Database: "core",
			Extra: map[string]string{"sslmode": "require"},
		})
		if env["PGHOST"] != "db.internal" || env["PGPORT"] != "5433" ||
			env["PGDATABASE"] != "core" || env["PGSSLMODE"] != "require" {
			t.Fatalf("env = %v", env)
		}
	})
}

func TestPostgresBuild(t *testing.T) {
	a := postgresAdapter{}

	t.Run("no params means no user -v binds", func(t *testing.T) {
		inv, err := a.Build(config.HostConfig{}, Query{SQL: "SELECT 1"})
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Join(inv.Argv, " ")
		if !strings.Contains(argv, "--csv") || !strings.Contains(argv, "ON_ERROR_STOP=1") {
			t.Fatalf("argv = %v", inv.Argv)
		}
		// Control vars only — no user param binds.
		for _, arg := range inv.Argv {
			if strings.Contains(arg, "name=") {
				t.Fatalf("unexpected param bind in argv: %v", inv.Argv)
			}
		}
		sql, _ := io.ReadAll(inv.Stdin)
		if string(sql) != "SELECT 1" {
			t.Fatalf("stdin = %q (SQL must ride stdin, never argv)", sql)
		}
	})

	t.Run("params bind via -v in sorted order", func(t *testing.T) {
		inv, err := a.Build(config.HostConfig{}, Query{
			SQL:    "SELECT :'b', :'a'",
			Params: map[string]string{"b": "2", "a": "1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Join(inv.Argv, " ")
		if !strings.Contains(argv, "-v a=1 -v b=2") {
			t.Fatalf("argv = %q, want sorted -v binds", argv)
		}
	})
}

func TestPostgresParse(t *testing.T) {
	a := postgresAdapter{}

	t.Run("csv with NULL and empty string", func(t *testing.T) {
		// psql -P null=\x01 --csv: NULL prints the sentinel, empty string prints "".
		out := "id,name,nickname\n1,Ada,\"\"\n2,Grace,\x01\n"
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
		if rows.Rows[0][2] == nil || *rows.Rows[0][2] != "" {
			t.Fatal("empty string must stay a non-nil empty string")
		}
		if rows.Rows[1][2] != nil {
			t.Fatal("NULL sentinel must become nil")
		}
	})

	t.Run("value containing comma and quote", func(t *testing.T) {
		out := "note\n\"a, \"\"quoted\"\" value\"\n"
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if got := *rows.Rows[0][0]; got != `a, "quoted" value` {
			t.Fatalf("cell = %q", got)
		}
	})

	t.Run("single-column empty-string row survives", func(t *testing.T) {
		// psql --csv prints a single-column empty-string value as a fully
		// blank line; encoding/csv would silently drop it as a non-record.
		out := "a\n\nx\n"
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

	t.Run("blank line inside quoted multi-line field untouched", func(t *testing.T) {
		out := "a\n\"line1\n\nline2\"\n"
		rows, err := a.Parse(executor.RawResult{Stdout: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows.Rows))
		}
		if got := *rows.Rows[0][0]; got != "line1\n\nline2" {
			t.Fatalf("cell = %q", got)
		}
	})

	t.Run("empty output is empty rows", func(t *testing.T) {
		rows, err := a.Parse(executor.RawResult{Stdout: nil})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Columns) != 0 || len(rows.Rows) != 0 {
			t.Fatalf("rows = %+v", rows)
		}
	})

	t.Run("nonzero exit is a parse error", func(t *testing.T) {
		_, err := a.Parse(executor.RawResult{ExitCode: 1, Stderr: []byte("ERROR: boom")})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("want error, got %v", err)
		}
	})
}

func TestPostgresIsSchemaError(t *testing.T) {
	a := postgresAdapter{}
	cases := []struct {
		stderr string
		exit   int
		want   bool
	}{
		{`ERROR:  42703: column "nope" does not exist`, 1, true},
		{`ERROR:  42P01: relation "ghosts" does not exist`, 1, true},
		{`ERROR:  column "x" does not exist`, 1, true},
		{`ERROR:  syntax error at or near "SELEC"`, 1, false},
		{`ERROR:  42703: column "nope" does not exist`, 0, false}, // exit 0 → not an error
	}
	for _, c := range cases {
		got := a.IsSchemaError(executor.RawResult{ExitCode: c.exit, Stderr: []byte(c.stderr)})
		if got != c.want {
			t.Errorf("IsSchemaError(%q, exit %d) = %v, want %v", c.stderr, c.exit, got, c.want)
		}
	}
}

func TestPostgresIntrospectSQL(t *testing.T) {
	sql := postgresAdapter{}.IntrospectSQL()
	if !strings.Contains(sql, "information_schema.columns") {
		t.Fatalf("introspection sql = %q", sql)
	}
}

// TestPostgresListDatabasesSQL pins the three predicates the candidate list
// depends on. NOT datistemplate is load-bearing rather than belt-and-braces:
// template1 has datallowconn = true and passes the privilege check, so only the
// template flag keeps it out of completion.
func TestPostgresListDatabasesSQL(t *testing.T) {
	sql := postgresAdapter{}.ListDatabasesSQL()
	for _, want := range []string{
		"pg_database",
		"datallowconn",
		"NOT datistemplate",
		"has_database_privilege(current_user, datname, 'CONNECT')",
		"ORDER BY datname",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("list-databases sql missing %q:\n%s", want, sql)
		}
	}
	// One column only: completion candidates are bare names (design.md §13.9).
	if strings.Count(sql, "SELECT") != 1 || strings.Contains(sql, ",\n") {
		t.Fatalf("list-databases sql must select exactly one column:\n%s", sql)
	}
}

func TestAdapterRegistry(t *testing.T) {
	for _, name := range []string{"postgres", "sqlserver"} {
		a, err := For(name)
		if err != nil {
			t.Fatal(err)
		}
		if a.Name() != name {
			t.Fatalf("adapter %q reports name %q", name, a.Name())
		}
	}
	if _, err := For("oracle"); err == nil {
		t.Fatal("want error for unknown provider")
	}
}
