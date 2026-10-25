package adapter

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/pgfake"
)

// fakeHost points a host at a running fake.
func fakeHost(srv *pgfake.Server) config.HostConfig {
	return config.HostConfig{Name: "t", Host: "127.0.0.1", Port: srv.Port, Database: "testdb", Extra: map[string]string{"sslmode": "disable"}}
}

var testCred = credential.Credential{Username: "app", Password: "pw"}

func TestPostgresConnConfigExplicitDefaults(t *testing.T) {
	// An inherited PG* variable must not redirect the query: every setting the
	// host decides is written into the connection string, so the environment
	// is never consulted for it.
	for k, v := range map[string]string{
		"PGHOST": "elsewhere", "PGPORT": "6543", "PGDATABASE": "wrong",
		"PGUSER": "intruder", "PGPASSWORD": "leaked", "PGSSLMODE": "disable",
		"PGAPPNAME": "other",
	} {
		t.Setenv(k, v)
	}
	cfg, err := postgresAdapter{}.connConfig(testCred, config.HostConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "localhost" || cfg.Port != 5432 || cfg.Database != "app" ||
		cfg.User != "app" || cfg.Password != "pw" {
		t.Fatalf("config = host %q port %d db %q user %q", cfg.Host, cfg.Port, cfg.Database, cfg.User)
	}
	if cfg.RuntimeParams["application_name"] != "db-query" {
		t.Fatalf("application_name = %q", cfg.RuntimeParams["application_name"])
	}
	// sslmode=prefer, libpq's default, tries TLS first; an inherited
	// PGSSLMODE=disable would have left no TLS attempt at all.
	if cfg.TLSConfig == nil {
		t.Fatal("sslmode must default to prefer, not follow an inherited PGSSLMODE")
	}
}

func TestPostgresConnConfigValuesFlowThrough(t *testing.T) {
	cred := credential.Credential{Username: "o'brien", Password: `p\w'"x`}
	cfg, err := postgresAdapter{}.connConfig(cred, config.HostConfig{
		Host: "db.internal", Port: 5433, Database: "core",
		Extra: map[string]string{"sslmode": "disable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "db.internal" || cfg.Port != 5433 || cfg.Database != "core" || cfg.TLSConfig != nil {
		t.Fatalf("config = %+v", cfg)
	}
	// Quotes and backslashes in a credential survive the connection string.
	if cfg.User != cred.Username || cfg.Password != cred.Password {
		t.Fatalf("user %q password %q", cfg.User, cfg.Password)
	}
}

func TestPostgresReadOnlySetsOptions(t *testing.T) {
	t.Setenv("PGOPTIONS", "-c default_transaction_read_only=off")
	for _, tt := range []struct {
		name     string
		readOnly bool
		want     string
	}{
		{"read-only host replaces an inherited PGOPTIONS", true, "-c default_transaction_read_only=on"},
		{"writable host leaves the environment's own", false, "-c default_transaction_read_only=off"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := postgresAdapter{}.connConfig(testCred, config.HostConfig{ReadOnly: tt.readOnly})
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.RuntimeParams["options"]; got != tt.want {
				t.Errorf("options: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresPlanBindsPositionalParams(t *testing.T) {
	stmts, err := postgresAdapter{}.plan(Query{
		SQL:    "SELECT $2; SELECT $1, $1; SELECT 1",
		Params: map[string]string{"1": "a", "2": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 3 {
		t.Fatalf("statements = %d", len(stmts))
	}
	// $2 alone: $1 is declared text and sent NULL, $2 is inferred.
	if !reflect.DeepEqual(stmts[0].oids, []uint32{textOID, 0}) ||
		stmts[0].values[0] != nil || string(stmts[0].values[1]) != "b" {
		t.Fatalf("statement 1 = %+v", stmts[0])
	}
	if !reflect.DeepEqual(stmts[1].oids, []uint32{0}) || string(stmts[1].values[0]) != "a" {
		t.Fatalf("statement 2 = %+v", stmts[1])
	}
	if stmts[2].values != nil || stmts[2].oids != nil {
		t.Fatalf("statement 3 = %+v", stmts[2])
	}
}

func TestPostgresPlanRefusals(t *testing.T) {
	for _, tt := range []struct {
		name   string
		q      Query
		want   string
		secret string
	}{
		{"a named parameter", Query{SQL: "SELECT $1", Params: map[string]string{"who": "Ada; DROP"}}, `"who"`, "Ada; DROP"},
		{"a zero position", Query{SQL: "SELECT 1", Params: map[string]string{"0": "x"}}, `"0"`, ""},
		{"a leading zero", Query{SQL: "SELECT $1", Params: map[string]string{"01": "x"}}, `"01"`, ""},
		{"beyond the protocol's limit", Query{SQL: "SELECT 1", Params: map[string]string{"65536": "x"}}, `"65536"`, ""},
		{"an unbound reference", Query{SQL: "SELECT 1; SELECT $2", Params: map[string]string{"1": "x"}}, "$2 is not bound", ""},
		{"a client directive", Query{SQL: "\\i evil.sql\nSELECT 1"}, "client directive", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := postgresAdapter{}.plan(tt.q)
			var local *LocalError
			if !errors.As(err, &local) {
				t.Fatalf("err = %v, want a LocalError", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %s", err, tt.want)
			}
			// §9: the message names the parameter, never its value.
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestPostgresExecuteSingleStatement(t *testing.T) {
	srv := pgfake.Start(t, func(q pgfake.Query) pgfake.Reply {
		return pgfake.Rows([]string{"id", "name", "nickname"},
			[]string{"1", "Ada", ""},
			[]string{"2", "Grace", pgfake.Null})
	})
	sets, err := postgresAdapter{}.Execute(context.Background(), testCred, fakeHost(srv), Query{SQL: "SELECT * FROM people"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 {
		t.Fatalf("sets = %d, want 1", len(sets))
	}
	rows := sets[0]
	if !reflect.DeepEqual(rows.Columns, []string{"id", "name", "nickname"}) || len(rows.Rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows.Rows[0][2] == nil || *rows.Rows[0][2] != "" {
		t.Fatal("empty string must stay a non-nil empty string")
	}
	if rows.Rows[1][2] != nil {
		t.Fatal("NULL must become nil")
	}
	// A single statement runs on its own, without a transaction around it,
	// so a statement that refuses one (VACUUM) still works.
	if got := srv.SQL(); !reflect.DeepEqual(got, []string{"SELECT * FROM people"}) {
		t.Fatalf("statements sent = %q", got)
	}
	if srv.Password() != "pw" || srv.Startup()["user"] != "app" || srv.Startup()["database"] != "testdb" {
		t.Fatalf("startup = %v", srv.Startup())
	}
}

func TestPostgresExecuteScriptRunsInOneTransaction(t *testing.T) {
	srv := pgfake.Start(t, func(q pgfake.Query) pgfake.Reply {
		switch {
		case strings.HasPrefix(q.SQL, "SELECT a"):
			return pgfake.Rows([]string{"a"}, []string{"1"})
		case strings.HasPrefix(q.SQL, "SELECT b"):
			return pgfake.Rows([]string{"b"})
		}
		return pgfake.Reply{}
	})
	sets, err := postgresAdapter{}.Execute(context.Background(), testCred, fakeHost(srv), Query{
		SQL:    "SELECT a FROM t;\nUPDATE t SET x = $1;\nSELECT b FROM t WHERE y = $1",
		Params: map[string]string{"1": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BEGIN", "SELECT a FROM t", "UPDATE t SET x = $1", "SELECT b FROM t WHERE y = $1", "COMMIT"}
	if got := srv.SQL(); !reflect.DeepEqual(got, want) {
		t.Fatalf("statements sent = %q, want %q", got, want)
	}
	// One result set per statement that returned columns, an empty one
	// included; the UPDATE contributes nothing.
	if len(sets) != 2 || sets[0].Columns[0] != "a" || sets[1].Columns[0] != "b" || len(sets[1].Rows) != 0 {
		t.Fatalf("sets = %+v", sets)
	}
	// $1 is the same value in every statement that uses it.
	for _, q := range srv.Queries()[2:4] {
		if len(q.Params) != 1 || string(q.Params[0]) != "v" {
			t.Fatalf("%q bound %q", q.SQL, q.Params)
		}
	}
}

func TestPostgresExecuteScriptStopsAtFirstError(t *testing.T) {
	srv := pgfake.Start(t, func(q pgfake.Query) pgfake.Reply {
		if strings.Contains(q.SQL, "nope") {
			r := pgfake.Error("42703", `column "nope" does not exist`)
			r.Hint = "Perhaps you meant id."
			return r
		}
		return pgfake.Rows([]string{"x"}, []string{"1"})
	})
	sets, err := postgresAdapter{}.Execute(context.Background(), testCred, fakeHost(srv), Query{
		SQL: "SELECT 1; SELECT nope FROM t; SELECT 3",
	})
	if sets != nil {
		t.Fatalf("a failed script returns no result sets, got %+v", sets)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want a QueryError", err)
	}
	if !qe.Schema {
		t.Error("42703 must be reported as a schema error")
	}
	for _, want := range []string{"statement 2 of 3", "42703", `column "nope" does not exist`, "HINT: Perhaps you meant id."} {
		if !strings.Contains(qe.Msg, want) {
			t.Errorf("message %q lacks %q", qe.Msg, want)
		}
	}
	want := []string{"BEGIN", "SELECT 1", "SELECT nope FROM t", "ROLLBACK"}
	if got := srv.SQL(); !reflect.DeepEqual(got, want) {
		t.Fatalf("statements sent = %q, want %q (nothing after the failure, then a rollback)", got, want)
	}
}

func TestPostgresExecuteOtherSQLError(t *testing.T) {
	srv := pgfake.Start(t, func(pgfake.Query) pgfake.Reply {
		return pgfake.Error("23505", "duplicate key value violates unique constraint")
	})
	_, err := postgresAdapter{}.Execute(context.Background(), testCred, fakeHost(srv), Query{SQL: "INSERT INTO t VALUES (1)"})
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Schema {
		t.Fatalf("err = %#v, want a non-schema QueryError", err)
	}
	if strings.Contains(qe.Msg, "statement 1") {
		t.Errorf("a single statement needs no position: %q", qe.Msg)
	}
}

func TestPostgresExecuteUnreachable(t *testing.T) {
	srv := pgfake.Start(t, func(pgfake.Query) pgfake.Reply { return pgfake.Reply{} })
	host := fakeHost(srv)
	host.Port = closedPort(t)
	_, err := postgresAdapter{}.Execute(context.Background(), testCred, host, Query{SQL: "SELECT 1"})
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want a QueryError, as psql's connection failure was", err)
	}
	if strings.Contains(err.Error(), testCred.Password) {
		t.Fatalf("connection error carries the password: %v", err)
	}
}

func TestPostgresExecuteNothingToRun(t *testing.T) {
	// No server at all: an empty submission never connects.
	sets, err := postgresAdapter{}.Execute(context.Background(), testCred, config.HostConfig{Port: 1}, Query{SQL: " ; -- nothing\n"})
	if err != nil || sets != nil {
		t.Fatalf("sets = %v, err = %v", sets, err)
	}
}

func TestPostgresExecuteCancellation(t *testing.T) {
	srv := pgfake.Start(t, func(pgfake.Query) pgfake.Reply { return pgfake.Reply{Delay: 10 * time.Second} })

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := postgresAdapter{}.Execute(ctx, testCred, fakeHost(srv), Query{SQL: "SELECT pg_sleep(10)"})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v, want a timeout wrapping context.DeadlineExceeded", err)
		}
		// The server was asked to stop, so the reply came back promptly.
		if time.Since(start) > 5*time.Second {
			t.Fatalf("cancel took %s", time.Since(start))
		}
	})
	t.Run("explicit cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		_, err := postgresAdapter{}.Execute(ctx, testCred, fakeHost(srv), Query{SQL: "SELECT pg_sleep(10)"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// closedPort returns a port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
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

func TestPostgresPreviewSQL(t *testing.T) {
	cases := map[string]string{
		"public.orders":     `SELECT * FROM "public"."orders" LIMIT 100;`,
		"orders":            `SELECT * FROM "orders" LIMIT 100;`,
		"public.Order Item": `SELECT * FROM "public"."Order Item" LIMIT 100;`, // mixed case and a space survive quoting
		`public.we"ird`:     `SELECT * FROM "public"."we""ird" LIMIT 100;`,    // an embedded quote is doubled
	}
	for table, want := range cases {
		if got := (postgresAdapter{}).PreviewSQL(table); got != want {
			t.Errorf("PreviewSQL(%q) = %q, want %q", table, got, want)
		}
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
