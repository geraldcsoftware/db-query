package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/savedquery"
	"github.com/geraldcsoftware/db-query/internal/schema"
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

// isolateCache points the schema cache at a fresh temp dir so runs never
// touch the real cache and the first-run schema build is deterministic.
func isolateCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// seedSchemaCache isolates the cache and pre-writes the entry for
// testConfig's resolved host (localhost/testdb), so a query run finds the
// cache present and skips the silent schema build — the psql stub then only
// answers the user query.
func seedSchemaCache(t *testing.T) {
	t.Helper()
	isolateCache(t)
	path := schema.CachePath("localhost", "testdb")
	if err := schema.Write(path, adapter.Rows{Columns: []string{"seeded"}}); err != nil {
		t.Fatal(err)
	}
}

// splitPsql installs a psql stub that answers the introspection query with a
// canned catalogue (so the silent schema build succeeds) and runs the given
// user-query script for everything else. The introspection SQL is
// recognised by its information_schema reference. Both branches append a
// marker line to $DBQ_CALLS so a test can assert which invocations ran.
func splitPsql(t *testing.T, userScript string) {
	t.Helper()
	fakePsql(t, `
sql=$(cat)
case "$sql" in
  *information_schema*)
    printf 'introspect\n' >> "$DBQ_CALLS"
    printf 'table_schema,table_name,column_name,data_type,is_nullable\npublic,people,id,integer,NO\n'
    exit 0
    ;;
esac
printf 'query\n' >> "$DBQ_CALLS"
`+userScript)
}

// callsFile registers a temp calls-marker file for splitPsql and returns a
// reader for its contents.
func callsFile(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "calls")
	t.Setenv("DBQ_CALLS", path)
	return func() string {
		b, _ := os.ReadFile(path)
		return string(b)
	}
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

// TestMain clears the environment defaults the CLI reads, so a developer who
// has DB_QUERY_HOST/DB_QUERY_DATABASE/DB_QUERY_CONFIG/DB_QUERY_OUTPUT exported
// in their own shell does not change what the tests see. The tests that
// exercise the environment path set these explicitly with t.Setenv.
func TestMain(m *testing.M) {
	for _, k := range []string{"DB_QUERY_HOST", "DB_QUERY_DATABASE", "DB_QUERY_CONFIG", "DB_QUERY_OUTPUT"} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
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

// TestUsageListsShorthands pins the help format: each flag with a shorthand
// is listed as `--long (-x) : description` on one line.
func TestUsageListsShorthands(t *testing.T) {
	code, out, _ := run(t, "--help")
	if code != 0 {
		t.Fatalf("--help: code=%d", code)
	}
	for _, want := range []string{"--host (-H)", "--database (-d)", "--config (-c)", "--output (-o)", "--param (-p)", "--file (-f)", "--source (-s)", "--category (-C)", "--timeout (-t)", "--help (-h)"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q", want)
		}
	}
}

// TestSubcommandHelp pins that -h/--help on any subcommand prints the usage
// text on stdout and exits 0, instead of the flag package's default dump.
func TestSubcommandHelp(t *testing.T) {
	for _, cmd := range []string{"query", "list", "schema", "introspect", "hosts"} {
		for _, h := range []string{"-h", "--help"} {
			code, out, _ := run(t, cmd, h)
			if code != 0 || !strings.Contains(out, "Usage:") {
				t.Fatalf("%s %s: code=%d out=%q", cmd, h, code, out)
			}
		}
	}
}

// TestVersionShorthand pins -v as an alias of --version at the top level.
func TestVersionShorthand(t *testing.T) {
	code, out, _ := run(t, "-v")
	if code != 0 || !strings.Contains(out, "db-query") {
		t.Fatalf("-v: code=%d out=%q", code, out)
	}
}

// TestUsageDocumentsCommandsAndEnvironment pins that the help text carries the
// two discovery surfaces a terminal user has nowhere else to learn: the command
// shorthands and the environment defaults.
func TestUsageDocumentsCommandsAndEnvironment(t *testing.T) {
	code, out, _ := run(t, "--help")
	if code != 0 {
		t.Fatalf("--help: code=%d", code)
	}
	for _, want := range []string{"query      (q)", "list       (ls, l)", "schema     (s)", "introspect (i)", "DB_QUERY_HOST", "DB_QUERY_DATABASE"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q", want)
		}
	}
}

// TestCommandAliases pins each shorthand to its command. Dispatch is asserted
// through a response only that command produces, so a shorthand landing on the
// wrong command (or on the unknown-command path) fails.
func TestCommandAliases(t *testing.T) {
	isolateStore(t)
	if _, err := savedquery.Save("alpha", "reports", "postgres", "SELECT 1 FROM t", false); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"ls", "l"} {
		code, out, errb := run(t, alias, "--output", "json")
		if code != 0 {
			t.Fatalf("%s: code=%d err=%q", alias, code, errb)
		}
		var list []map[string]any
		if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
			t.Fatalf("%s did not list the store: out=%q err=%v", alias, out, err)
		}
	}
	// query/schema/introspect all require a host: the host error proves the
	// shorthand reached the command, while "unknown command" would not. `q` is
	// given SQL because the query path reads its SQL before resolving a host.
	for _, args := range [][]string{{"q", "SELECT 1"}, {"s"}, {"i"}} {
		code, _, errb := run(t, args...)
		if code != 1 || !strings.Contains(errb, "--host is required") {
			t.Fatalf("%v: code=%d err=%q", args, code, errb)
		}
	}
	// -T belongs to schema alone, so accepting it pins `s` to schema rather
	// than to another host-requiring command.
	if code, _, errb := run(t, "s", "-T"); code != 1 || !strings.Contains(errb, "--host is required") {
		t.Fatalf("s -T: code=%d err=%q", code, errb)
	}
	// The rename is complete: the old name is not a hidden alias.
	if code, _, errb := run(t, "queries"); code != 1 || !strings.Contains(errb, "unknown command") {
		t.Fatalf("queries must be gone: code=%d err=%q", code, errb)
	}
}

// TestSharedFlagsBeforeCommand pins the position-free spelling of the shared
// flags: given before the command they reach the run exactly as they would
// after it, which is what lets the previous command be edited rather than
// retyped.
func TestSharedFlagsBeforeCommand(t *testing.T) {
	isolateCache(t)
	// The cache is keyed on host+database, so seed the overridden database:
	// a global --database that failed to land would trigger a schema build
	// against testdb instead and the assertion below would catch it.
	if err := schema.Write(schema.CachePath("localhost", "otherdb"), adapter.Rows{Columns: []string{"seeded"}}); err != nil {
		t.Fatal(err)
	}
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	fakePsql(t, `env > "$TMPDIR_CAPTURE/env"; printf 'ok\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	code, out, errb := run(t, "--host", "testpg", "--database", "otherdb", "--config", cfg, "query", "SELECT 1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("out = %q", out)
	}
	env, _ := os.ReadFile(filepath.Join(capture, "env"))
	if !strings.Contains(string(env), "PGDATABASE=otherdb") {
		t.Fatalf("a global --database must reach the client; env=%q", env)
	}
	// Shorthands work in the same position, and so does a command flag after
	// the globals.
	code, _, errb = run(t, "-H", "testpg", "-d", "otherdb", "-c", cfg, "query", "--no-headers", "SELECT 1")
	if code != 0 {
		t.Fatalf("shorthand globals: code=%d err=%q", code, errb)
	}
	// A command's own flag is not accepted before the command: it would
	// otherwise become global for every command.
	if code, _, errb := run(t, "--tables", "schema"); code != 1 || !strings.Contains(errb, "not defined") {
		t.Fatalf("subcommand flag before command: code=%d err=%q", code, errb)
	}
	// Globals with no command name run nothing.
	if code, _, errb := run(t, "-H", "testpg"); code != 1 || !strings.Contains(errb, "Usage:") {
		t.Fatalf("globals with no command: code=%d err=%q", code, errb)
	}
}

// TestCommandFlagBeatsGlobal pins the precedence between the two positions:
// the same flag after the command wins, in both directions.
func TestCommandFlagBeatsGlobal(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `printf 'ok\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	if code, _, errb := run(t, "--host", "nope", "--config", cfg, "query", "--host", "testpg", "SELECT 1"); code != 0 {
		t.Fatalf("command --host must win over the global one: code=%d err=%q", code, errb)
	}
	if code, _, errb := run(t, "--host", "testpg", "--config", cfg, "query", "--host", "nope", "SELECT 1"); code != 1 || !strings.Contains(errb, "unknown host") {
		t.Fatalf("command --host must win even when it is the bad one: code=%d err=%q", code, errb)
	}
}

// TestEnvironmentDefaults pins DB_QUERY_HOST/DB_QUERY_DATABASE as defaults for
// --host/--database, and that either flag position still overrides them.
func TestEnvironmentDefaults(t *testing.T) {
	isolateCache(t)
	for _, db := range []string{"envdb", "flagdb"} {
		if err := schema.Write(schema.CachePath("localhost", db), adapter.Rows{Columns: []string{"seeded"}}); err != nil {
			t.Fatal(err)
		}
	}
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	fakePsql(t, `env > "$TMPDIR_CAPTURE/env"; printf 'ok\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	t.Setenv("DB_QUERY_CONFIG", cfg) // so no --config is needed either
	t.Setenv("DB_QUERY_HOST", "testpg")
	t.Setenv("DB_QUERY_DATABASE", "envdb")

	code, out, errb := run(t, "query", "SELECT 1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("out = %q", out)
	}
	env, _ := os.ReadFile(filepath.Join(capture, "env"))
	if !strings.Contains(string(env), "PGDATABASE=envdb") {
		t.Fatalf("DB_QUERY_DATABASE must supply the database; env=%q", env)
	}

	// A flag beats the environment, before or after the command.
	for _, args := range [][]string{
		{"query", "--database", "flagdb", "SELECT 1"},
		{"--database", "flagdb", "query", "SELECT 1"},
	} {
		if code, _, errb := run(t, args...); code != 0 {
			t.Fatalf("%v: code=%d err=%q", args, code, errb)
		}
		env, _ := os.ReadFile(filepath.Join(capture, "env"))
		if !strings.Contains(string(env), "PGDATABASE=flagdb") {
			t.Fatalf("%v: --database must beat DB_QUERY_DATABASE; env=%q", args, env)
		}
	}
	// An unknown host in the environment fails like an unknown host anywhere.
	t.Setenv("DB_QUERY_HOST", "nope")
	if code, _, errb := run(t, "query", "SELECT 1"); code != 1 || !strings.Contains(errb, "unknown host") {
		t.Fatalf("bad DB_QUERY_HOST: code=%d err=%q", code, errb)
	}
}

// TestShorthandFlags drives a full query run through the single-letter
// aliases (-H, -c, -o, -t, -p, --file) and asserts they land exactly where
// their long forms would.
func TestShorthandFlags(t *testing.T) {
	seedSchemaCache(t)
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	fakePsql(t, `
cat > "$TMPDIR_CAPTURE/stdin"
printf '%s\n' "$@" > "$TMPDIR_CAPTURE/argv"
printf 'id\n1\n'
`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	sqlFile := writeFile(t, t.TempDir(), "q.sql", "SELECT :'who'", 0o600)

	code, out, errb := run(t, "query", "-H", "testpg", "-c", cfg, "-o", "json", "-t", "10s", "-p", "who=Ada", "--file", sqlFile)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	var rows []map[string]*string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("-o json did not produce JSON: %q", out)
	}
	stdin, _ := os.ReadFile(filepath.Join(capture, "stdin"))
	if string(stdin) != "SELECT :'who'" {
		t.Fatalf("--file SQL over stdin = %q", stdin)
	}
	argv, _ := os.ReadFile(filepath.Join(capture, "argv"))
	if !strings.Contains(string(argv), "who=Ada") {
		t.Fatalf("-p param missing from client argv: %q", argv)
	}
}

// TestShorthandSourceAndCategory pins -s/--source and -C/--category via the
// usage errors they trigger, which fire before any credential or store work.
func TestShorthandSourceAndCategory(t *testing.T) {
	code, _, errb := run(t, "query", "-s", "foo", "--save", "bar")
	if code != 1 || !strings.Contains(errb, "mutually exclusive") {
		t.Fatalf("-s with --save: code=%d err=%q", code, errb)
	}
	code, _, errb = run(t, "query", "-s", "foo", "SELECT 1")
	if code != 1 || !strings.Contains(errb, "--source") {
		t.Fatalf("-s with SQL: code=%d err=%q", code, errb)
	}
	t.Setenv("DB_QUERY_QUERIES_DIR", t.TempDir())
	seedSchemaCache(t)
	fakePsql(t, `printf 'x\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb = run(t, "query", "-H", "testpg", "-c", cfg, "-s", "missing", "-C", "reports")
	if code != 1 || !strings.Contains(errb, `"reports"`) {
		t.Fatalf("-C category not honoured: code=%d err=%q", code, errb)
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

// inheritedConfig is the two-level shape profiles exist for: a base profile
// holding what every host shares, a narrower one holding per-group
// credentials, and hosts carrying only their address.
func inheritedConfig(t *testing.T) string {
	t.Helper()
	return writeFile(t, t.TempDir(), "config.toml", `
[profiles.pg]
provider = "postgres"
database = "postgres"

[profiles.eus]
inherit    = "pg"
username   = "gchifanzwa"
credential = "env:DBQ_TEST_PW"

[hosts.lionel]
inherit = "eus"
host    = "lionel.internal"
sslmode = "require"
`, 0o600)
}

func TestHostsCommandOneHostShowsEffectiveConfig(t *testing.T) {
	cfg := inheritedConfig(t)
	code, out, errb := run(t, "hosts", "lionel", "--config", cfg, "--output", "text")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	for _, want := range []string{
		"provider\tpostgres\tprofile pg",
		"database\tpostgres\tprofile pg",
		"username\tgchifanzwa\tprofile eus",
		"credential\tenv:DBQ_TEST_PW\tprofile eus",
		"host\tlionel.internal\thost lionel",
		"sslmode\trequire\thost lionel",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Core keys come first in a fixed order, then adapter keys — deterministic
	// output is what makes this readable by eye and testable.
	if i, j := strings.Index(out, "provider"), strings.Index(out, "host\t"); i > j {
		t.Errorf("provider must precede host:\n%s", out)
	}
	if i, j := strings.Index(out, "credential"), strings.Index(out, "sslmode"); i > j {
		t.Errorf("core keys must precede adapter keys:\n%s", out)
	}
	// An unset core key is omitted rather than shown blank.
	if strings.Contains(out, "port") {
		t.Errorf("unset port should not be listed:\n%s", out)
	}
}

func TestHostsCommandOneHostAcrossFormats(t *testing.T) {
	cfg := inheritedConfig(t)
	t.Run("json", func(t *testing.T) {
		code, out, errb := run(t, "hosts", "lionel", "--config", cfg, "--output", "json")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		found := false
		for _, r := range rows {
			if r["key"] == "username" {
				found = true
				if r["value"] != "gchifanzwa" || r["source"] != "profile eus" {
					t.Errorf("username row = %+v", r)
				}
			}
		}
		if !found {
			t.Errorf("no username row in %s", out)
		}
	})
	t.Run("table", func(t *testing.T) {
		code, out, errb := run(t, "hosts", "lionel", "--config", cfg, "--output", "table")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "+--") || !strings.Contains(out, "| key") {
			t.Fatalf("not tabular:\n%s", out)
		}
	})
}

func TestHostsCommandArgumentErrors(t *testing.T) {
	cfg := inheritedConfig(t)
	t.Run("unknown host", func(t *testing.T) {
		code, _, errb := run(t, "hosts", "nope", "--config", cfg)
		if code != 1 || !strings.Contains(errb, "unknown host") {
			t.Fatalf("code=%d err=%q", code, errb)
		}
	})
	t.Run("profile is not a host", func(t *testing.T) {
		code, _, errb := run(t, "hosts", "eus", "--config", cfg)
		if code != 1 || !strings.Contains(errb, "is a profile") {
			t.Fatalf("code=%d err=%q", code, errb)
		}
	})
	t.Run("too many positionals", func(t *testing.T) {
		code, _, errb := run(t, "hosts", "lionel", "testpg", "--config", cfg)
		if code != 1 || !strings.Contains(errb, "at most one host name") {
			t.Fatalf("code=%d err=%q", code, errb)
		}
	})
}

// Profiles are not connectable and must not appear in the listing.
func TestHostsListingExcludesProfiles(t *testing.T) {
	cfg := inheritedConfig(t)
	code, out, errb := run(t, "hosts", "--config", cfg)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "lionel") {
		t.Fatalf("host missing from listing:\n%s", out)
	}
	for _, p := range []string{"eus", " pg "} {
		if strings.Contains(out, p) {
			t.Errorf("profile %q leaked into the listing:\n%s", p, out)
		}
	}
}

func TestQueryHappyPath(t *testing.T) {
	seedSchemaCache(t) // cache present → single invocation, the user query
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
	seedSchemaCache(t)
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
	isolateCache(t)
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
	seedSchemaCache(t) // cache present → the failing stub is the user query
	fakePsql(t, `echo 'ERROR:  42703: column "nope" does not exist' >&2; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT nope FROM people")
	if code != 3 {
		t.Fatalf("code = %d, want 3 (schema error)", code)
	}
	if !strings.Contains(errb, "--refresh-schema") {
		t.Fatalf("schema error must hint at --refresh-schema, err=%q", errb)
	}
}

// TestQueryOtherSQLError pins the 3-vs-4 split: a non-schema SQL failure is
// exit code 4 and carries no --refresh-schema hint.
func TestQueryOtherSQLError(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `echo 'ERROR:  23505: duplicate key value violates unique constraint' >&2; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "INSERT INTO people VALUES (1)")
	if code != 4 {
		t.Fatalf("code = %d, want 4 (other SQL error)", code)
	}
	if strings.Contains(errb, "--refresh-schema") {
		t.Fatalf("a non-schema error must not hint at --refresh-schema, err=%q", errb)
	}
}

func TestQueryJSONErrorIsStructured(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `echo 'ERROR: boom' >&2; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "--output", "json", "SELECT 1")
	if code != 4 {
		t.Fatalf("code = %d, want 4", code)
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
	// Empty PATH: psql cannot start → distinct exit code 2. The schema
	// build is the first invocation and fails to start, which is enough.
	isolateCache(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT 1")
	if code != 2 || !strings.Contains(errb, "starting") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestIntrospectUsesAdapterSQL(t *testing.T) {
	isolateCache(t)
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
	// introspect persists the cache as a side effect of printing it.
	if !schema.Exists(schema.CachePath("localhost", "testdb")) {
		t.Fatal("introspect must write the schema cache")
	}
}

func TestQueryParamPassing(t *testing.T) {
	seedSchemaCache(t) // skip the build so the captured argv is the user query
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

// TestQueryFlagsAfterSQL guards the interspersed-args parsing: flags placed
// after the positional SQL (which Go's flag package would otherwise swallow)
// must still bind. The SQL and a trailing --param both have to reach psql.
func TestQueryFlagsAfterSQL(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `
printf '%s ' "$@" > "$TMPDIR_CAPTURE/argv"
cat > "$TMPDIR_CAPTURE/stdin"
printf 'ok\n1\n'
`)
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	// Flags on both sides of the SQL, including one that trails it.
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"SELECT :'who'", "--param", "who=Ada", "--no-headers")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	argv, _ := os.ReadFile(filepath.Join(capture, "argv"))
	if !strings.Contains(string(argv), "-v who=Ada") {
		t.Fatalf("trailing --param not bound; argv = %q", argv)
	}
	stdin, _ := os.ReadFile(filepath.Join(capture, "stdin"))
	if !strings.Contains(string(stdin), "SELECT :'who'") {
		t.Fatalf("SQL not delivered; stdin = %q", stdin)
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

func TestQueryBuildsSchemaOnFirstRun(t *testing.T) {
	isolateCache(t) // cache absent → the build must run before the query
	calls := callsFile(t)
	splitPsql(t, `printf 'id\n42\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT id FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("user query output = %q", out)
	}
	if c := calls(); !strings.Contains(c, "introspect") || !strings.Contains(c, "query") {
		t.Fatalf("first run must introspect then query, calls=%q", c)
	}
	if !schema.Exists(schema.CachePath("localhost", "testdb")) {
		t.Fatal("first run must write the schema cache")
	}
}

func TestQuerySkipsSchemaWhenCached(t *testing.T) {
	seedSchemaCache(t) // cache present → no build, just the user query
	calls := callsFile(t)
	splitPsql(t, `printf 'id\n42\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT id FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("out = %q", out)
	}
	if c := calls(); strings.Contains(c, "introspect") {
		t.Fatalf("a cached schema must not be rebuilt, calls=%q", c)
	}
}

func TestQueryRefreshSchemaForcesRebuild(t *testing.T) {
	seedSchemaCache(t) // cache present, but --refresh-schema overrides
	calls := callsFile(t)
	splitPsql(t, `printf 'id\n42\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--refresh-schema", "SELECT id FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if c := calls(); !strings.Contains(c, "introspect") || !strings.Contains(c, "query") {
		t.Fatalf("--refresh-schema must rebuild before the query, calls=%q", c)
	}
}

// TestQuerySchemaBuildFailureStops pins that a failed internal introspection
// surfaces and stops: the user query must not run.
func TestQuerySchemaBuildFailureStops(t *testing.T) {
	isolateCache(t)
	calls := callsFile(t)
	// The introspection query fails; the user branch would otherwise run.
	fakePsql(t, `
sql=$(cat)
case "$sql" in
  *information_schema*)
    printf 'introspect\n' >> "$DBQ_CALLS"
    echo 'ERROR:  57014: canceling statement due to statement timeout' >&2
    exit 1
    ;;
esac
printf 'query\n' >> "$DBQ_CALLS"
printf 'id\n42\n'
`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, _ := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT id FROM people")
	if code != 4 { // ran and failed, not a schema error
		t.Fatalf("code = %d, want 4 (introspection failed)", code)
	}
	if out != "" {
		t.Fatalf("user query must not run after a failed build, out=%q", out)
	}
	if c := calls(); strings.Contains(c, "query") {
		t.Fatalf("user query ran despite a failed schema build, calls=%q", c)
	}
}

func TestIntrospectAlwaysRebuilds(t *testing.T) {
	isolateCache(t)
	// Seed a stale placeholder; introspect must overwrite it with fresh
	// catalogue rows regardless of the cache already existing.
	path := schema.CachePath("localhost", "testdb")
	if err := schema.Write(path, adapter.Rows{Columns: []string{"stale"}}); err != nil {
		t.Fatal(err)
	}
	calls := callsFile(t)
	splitPsql(t, `printf 'unused\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "introspect", "--host", "testpg", "--config", cfg)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if c := calls(); !strings.Contains(c, "introspect") {
		t.Fatalf("introspect must run the catalogue query, calls=%q", c)
	}
	if !strings.Contains(out, "people") {
		t.Fatalf("introspect must print the fresh schema, out=%q", out)
	}
	got, err := schema.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) == 1 && got.Columns[0] == "stale" {
		t.Fatal("introspect must overwrite the stale cache")
	}
}

func TestQueryNoHeaders(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `printf 'count\n42\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--no-headers", "SELECT count(*) FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "42\n" {
		t.Fatalf("--no-headers 1×1 = %q, want %q", out, "42\n")
	}
}

func TestQueryNoHeadersJSONNoOp(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `printf 'id,name\n1,Ada\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--no-headers", "--output", "json", "SELECT id, name FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	var rows []map[string]*string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--no-headers must not affect JSON shape: %q", out)
	}
	if len(rows) != 1 || rows[0]["name"] == nil || *rows[0]["name"] != "Ada" {
		t.Fatalf("rows = %v", rows)
	}
}

// TestQueryDatabaseOverride pins that --database / -d overrides the host's
// configured database for the run: the client sees the overridden name, and
// the configured one does not leak.
func TestQueryDatabaseOverride(t *testing.T) {
	for _, flag := range []string{"-d", "--database"} {
		t.Run(flag, func(t *testing.T) {
			isolateCache(t)
			// The schema cache is keyed on host+database, so seed the entry for
			// the OVERRIDDEN database; the psql stub then only answers the query.
			if err := schema.Write(schema.CachePath("localhost", "otherdb"), adapter.Rows{Columns: []string{"seeded"}}); err != nil {
				t.Fatal(err)
			}
			fakePsql(t, `env > "$TMPDIR_CAPTURE/env"; printf 'ok\n1\n'`)
			capture := t.TempDir()
			t.Setenv("TMPDIR_CAPTURE", capture)
			t.Setenv("DBQ_TEST_PW", "pw")
			cfg := testConfig(t) // testpg is configured with database=testdb
			code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, flag, "otherdb", "SELECT 1")
			if code != 0 {
				t.Fatalf("code=%d err=%q", code, errb)
			}
			env, _ := os.ReadFile(filepath.Join(capture, "env"))
			if !strings.Contains(string(env), "PGDATABASE=otherdb") {
				t.Fatalf("%s must override the configured database; env=%q", flag, env)
			}
			if strings.Contains(string(env), "PGDATABASE=testdb") {
				t.Fatalf("the configured database must not leak when %s is set; env=%q", flag, env)
			}
		})
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

// isolateStore points the saved-query store at a fresh temp dir so a test
// never touches the real store; it returns the directory.
func isolateStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DB_QUERY_QUERIES_DIR", dir)
	return dir
}

// TestQuerySaveOnSuccess pins that a successful run persists the SQL under
// name+category with the host provider, keeping the placeholder and never the
// resolved --param value.
func TestQuerySaveOnSuccess(t *testing.T) {
	seedSchemaCache(t)
	isolateStore(t)
	fakePsql(t, `printf 'id,name\n1,Ada\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--save", "people-by-name", "--category", "reports",
		"--param", "who=Ada", "SELECT id, name FROM people WHERE name = :'who'")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "Ada") {
		t.Fatalf("query output not printed, out=%q", out)
	}
	sq, err := savedquery.Load("people-by-name", "reports")
	if err != nil {
		t.Fatalf("query was not saved: %v", err)
	}
	if sq.Provider != "postgres" {
		t.Fatalf("saved provider = %q, want postgres", sq.Provider)
	}
	if !strings.Contains(sq.SQL, ":'who'") {
		t.Fatalf("saved SQL must keep the placeholder, got %q", sq.SQL)
	}
	if strings.Contains(sq.SQL, "Ada") {
		t.Fatalf("saved SQL must not carry the param value, got %q", sq.SQL)
	}
}

// TestQuerySaveNotOnFailure pins that a non-zero run saves nothing and returns
// its own exit code.
func TestQuerySaveNotOnFailure(t *testing.T) {
	seedSchemaCache(t)
	isolateStore(t)
	fakePsql(t, `echo 'ERROR: boom' >&2; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, _ := run(t, "query", "--host", "testpg", "--config", cfg,
		"--save", "doomed", "SELECT 1")
	if code != 4 {
		t.Fatalf("code = %d, want 4 (the run's own exit code)", code)
	}
	if _, err := savedquery.Load("doomed", "default"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed run must save nothing, got %v", err)
	}
}

// TestQuerySaveDuplicateRefusal pins that a dedup refusal is a usage error
// (exit 1) even though the query already ran and printed, and that --force
// overrides it.
func TestQuerySaveDuplicateRefusal(t *testing.T) {
	seedSchemaCache(t)
	isolateStore(t)
	fakePsql(t, `printf 'id\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	if code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--save", "one", "SELECT id FROM people"); code != 0 {
		t.Fatalf("first save code=%d err=%q", code, errb)
	}
	// Equivalent SQL under a new name: refused, but the query still runs and
	// its output is printed.
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--save", "two", "SELECT id FROM people ;")
	if code != 1 {
		t.Fatalf("dup save code=%d, want 1", code)
	}
	if !strings.Contains(out, "1") {
		t.Fatalf("query must still run and print, out=%q", out)
	}
	if !strings.Contains(errb, "identical") && !strings.Contains(errb, "already exists") {
		t.Fatalf("must report the duplicate, err=%q", errb)
	}
	if _, err := savedquery.Load("two", "default"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused save must not create a file")
	}
	// --force writes it anyway.
	if code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--save", "two", "--force", "SELECT id FROM people ;"); code != 0 {
		t.Fatalf("force save code=%d err=%q", code, errb)
	}
	if _, err := savedquery.Load("two", "default"); err != nil {
		t.Fatalf("force save should persist: %v", err)
	}
}

// TestQuerySource pins that --source loads a stored query and sends its SQL
// (placeholders included) to the client, binding --param as usual.
func TestQuerySource(t *testing.T) {
	seedSchemaCache(t)
	isolateStore(t)
	if _, err := savedquery.Save("by-name", "people", "postgres",
		"SELECT id, name FROM people WHERE name = :'who'", false); err != nil {
		t.Fatal(err)
	}
	fakePsql(t, `cat > "$TMPDIR_CAPTURE/stdin"; printf 'id,name\n1,Ada\n'`)
	capture := t.TempDir()
	t.Setenv("TMPDIR_CAPTURE", capture)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--source", "by-name", "--category", "people", "--param", "who=Ada")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "Ada") {
		t.Fatalf("out=%q", out)
	}
	stdin, _ := os.ReadFile(filepath.Join(capture, "stdin"))
	if !strings.Contains(string(stdin), "WHERE name = :'who'") {
		t.Fatalf("source SQL not sent to the client, stdin=%q", stdin)
	}
}

// TestQuerySourceProviderGuard pins that a provider-mismatched saved query is
// refused (exit 1) and never runs.
func TestQuerySourceProviderGuard(t *testing.T) {
	seedSchemaCache(t)
	isolateStore(t)
	if _, err := savedquery.Save("mismatch", "default", "sqlserver", "SELECT 1", false); err != nil {
		t.Fatal(err)
	}
	calls := callsFile(t)
	fakePsql(t, `printf 'ran\n' >> "$DBQ_CALLS"; printf 'x\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "--source", "mismatch")
	if code != 1 {
		t.Fatalf("provider mismatch must exit 1, got %d", code)
	}
	if !strings.Contains(errb, "provider") {
		t.Fatalf("must explain the provider mismatch, err=%q", errb)
	}
	if strings.Contains(calls(), "ran") {
		t.Fatal("a provider-mismatched saved query must not run")
	}
}

// TestQuerySourceMissing pins that an unknown --source exits 1 and lists what
// is available.
func TestQuerySourceMissing(t *testing.T) {
	seedSchemaCache(t)
	isolateStore(t)
	if _, err := savedquery.Save("present", "default", "postgres", "SELECT 1", false); err != nil {
		t.Fatal(err)
	}
	fakePsql(t, `printf 'x\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "--source", "absent")
	if code != 1 {
		t.Fatalf("missing source must exit 1, got %d", code)
	}
	if !strings.Contains(errb, "present") {
		t.Fatalf("must list available queries, err=%q", errb)
	}
}

// TestQuerySourceSaveExclusive and the SQL-argument guard are pure usage
// errors, resolved before any secret is touched.
func TestQuerySourceSaveExclusive(t *testing.T) {
	cfg := testConfig(t)
	if code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--source", "x", "--save", "y"); code != 1 || !strings.Contains(errb, "mutually exclusive") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--source", "x", "SELECT 1"); code != 1 || !strings.Contains(errb, "--source") {
		t.Fatalf("source+SQL: code=%d err=%q", code, errb)
	}
	if code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--save", "", "SELECT 1"); code != 1 || !strings.Contains(errb, "--save") {
		t.Fatalf("empty save name: code=%d err=%q", code, errb)
	}
}

// TestListSubcommand pins the saved-query listing in both formats and the
// category filter.
func TestListSubcommand(t *testing.T) {
	isolateStore(t)
	if _, err := savedquery.Save("alpha", "reports", "postgres", "SELECT 1 FROM t", false); err != nil {
		t.Fatal(err)
	}
	if _, err := savedquery.Save("beta", "people", "postgres", "SELECT 2 FROM u", false); err != nil {
		t.Fatal(err)
	}

	code, out, errb := run(t, "list")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	for _, want := range []string{"alpha", "beta", "reports", "people", "postgres"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text listing missing %q, out=%q", want, out)
		}
	}

	code, out, errb = run(t, "list", "--output", "json")
	if code != 0 {
		t.Fatalf("json code=%d err=%q", code, errb)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 entries, got %d (%v)", len(list), list)
	}
	if s, _ := list[0]["sql"].(string); !strings.Contains(s, "SELECT") {
		t.Fatalf("json must carry the full SQL, got %v", list[0])
	}
	if _, ok := list[0]["sqlhash"].(string); !ok {
		t.Fatalf("json must carry the full hash, got %v", list[0])
	}

	code, out, _ = run(t, "list", "--category", "people", "--output", "json")
	if code != 0 {
		t.Fatalf("filtered code=%d", code)
	}
	var filtered []map[string]any
	if err := json.Unmarshal([]byte(out), &filtered); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if len(filtered) != 1 || filtered[0]["name"] != "beta" {
		t.Fatalf("category filter failed: %v", filtered)
	}
}

// TestListEmptyStoreJSON pins that an empty store renders as a JSON array,
// not null, so a consuming agent can always range over it.
func TestListEmptyStoreJSON(t *testing.T) {
	isolateStore(t)
	code, out, errb := run(t, "list", "--output", "json")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty store must be [], got %q", out)
	}
}

// seedCatalogueCache isolates the cache and pre-writes a realistic
// introspected catalogue for testConfig's resolved host (localhost/testdb):
// two tables in public plus a same-named table in another schema, so tests
// can exercise bare-name, qualified, and distinct-table behaviour.
func seedCatalogueCache(t *testing.T) {
	t.Helper()
	isolateCache(t)
	s := func(v string) *string { return &v }
	rows := adapter.Rows{
		Columns: []string{"table_schema", "table_name", "column_name", "data_type", "is_nullable"},
		Rows: [][]*string{
			{s("public"), s("people"), s("id"), s("integer"), s("NO")},
			{s("public"), s("people"), s("name"), s("text"), s("YES")},
			{s("public"), s("orders"), s("id"), s("integer"), s("NO")},
			{s("audit"), s("people"), s("event"), s("text"), s("NO")},
		},
	}
	if err := schema.Write(schema.CachePath("localhost", "testdb"), rows); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaReadsCacheWithoutClient(t *testing.T) {
	seedCatalogueCache(t)
	calls := callsFile(t)
	fakePsql(t, `printf 'called\n' >> "$DBQ_CALLS"; exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if calls() != "" {
		t.Fatalf("schema must not invoke the client when the cache exists; calls=%q", calls())
	}
	for _, want := range []string{"table_schema\ttable_name", "public\tpeople\tid", "audit\tpeople\tevent"} {
		if !strings.Contains(out, want) {
			t.Errorf("out missing %q; out=%q", want, out)
		}
	}
}

func TestSchemaTablesOnly(t *testing.T) {
	seedCatalogueCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "--tables")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "table\npublic.people\npublic.orders\naudit.people\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestSchemaTableFilter(t *testing.T) {
	t.Setenv("DBQ_TEST_PW", "pw")
	t.Run("bare name matches any schema", func(t *testing.T) {
		seedCatalogueCache(t)
		cfg := testConfig(t)
		code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "people")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "public\tpeople\tid") || !strings.Contains(out, "audit\tpeople\tevent") {
			t.Fatalf("bare name must match both schemas; out=%q", out)
		}
		if strings.Contains(out, "orders") {
			t.Fatalf("unrelated table leaked into filter; out=%q", out)
		}
	})
	t.Run("qualified name pins the schema", func(t *testing.T) {
		seedCatalogueCache(t)
		cfg := testConfig(t)
		code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "audit.people")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "audit\tpeople\tevent") || strings.Contains(out, "public") {
			t.Fatalf("qualified filter wrong; out=%q", out)
		}
	})
	t.Run("matching is case-insensitive", func(t *testing.T) {
		seedCatalogueCache(t)
		cfg := testConfig(t)
		code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "ORDERS")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "public\torders\tid") {
			t.Fatalf("case-insensitive match failed; out=%q", out)
		}
	})
}

func TestSchemaUnknownTable(t *testing.T) {
	seedCatalogueCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "nonexistent")
	if code != 3 {
		t.Fatalf("unknown table must exit 3, got %d (err=%q)", code, errb)
	}
	if !strings.Contains(errb, "nonexistent") || !strings.Contains(errb, "--refresh-schema") {
		t.Fatalf("err must name the table and hint at --refresh-schema; err=%q", errb)
	}
}

func TestSchemaTablesAndNameExclusive(t *testing.T) {
	seedCatalogueCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, _, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "--tables", "people")
	if code != 1 || !strings.Contains(errb, "mutually exclusive") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestSchemaBuildsCacheOnMiss(t *testing.T) {
	isolateCache(t)
	calls := callsFile(t)
	splitPsql(t, `exit 1`) // only the introspection branch may run
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if calls() != "introspect\n" {
		t.Fatalf("expected exactly one introspection call, got %q", calls())
	}
	if !strings.Contains(out, "public\tpeople\tid") {
		t.Fatalf("out = %q", out)
	}
	if !schema.Exists(schema.CachePath("localhost", "testdb")) {
		t.Fatal("cache file must exist after the silent build")
	}
}

func TestSchemaRefreshForcesRebuild(t *testing.T) {
	seedSchemaCache(t) // stale cache present: without --refresh-schema it would be printed as-is
	calls := callsFile(t)
	splitPsql(t, `exit 1`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "--refresh-schema")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if calls() != "introspect\n" {
		t.Fatalf("expected a rebuild introspection call, got %q", calls())
	}
	if !strings.Contains(out, "people") || strings.Contains(out, "seeded") {
		t.Fatalf("out must show the rebuilt catalogue, not the stale cache; out=%q", out)
	}
}

// TestSchemaShorthands drives the schema command entirely through
// single-letter aliases: the common ones from addCommon (-H, -c, -o) and
// -T as shorthand for --tables.
func TestSchemaShorthands(t *testing.T) {
	seedCatalogueCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "schema", "-H", "testpg", "-c", cfg, "-T")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "table\npublic.people\npublic.orders\naudit.people\n" {
		t.Fatalf("-T must behave as --tables; out = %q", out)
	}
	code, _, errb = run(t, "schema", "-H", "testpg", "-c", cfg, "-T", "people")
	if code != 1 || !strings.Contains(errb, "mutually exclusive") {
		t.Fatalf("-T with a table name: code=%d err=%q", code, errb)
	}
	code, out, errb = run(t, "schema", "-H", "testpg", "-c", cfg, "-o", "json", "orders")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("-o json on schema: code=%d out=%q err=%q", code, out, errb)
	}
}

func TestSchemaInUsage(t *testing.T) {
	code, out, _ := run(t, "help")
	if code != 0 || !strings.Contains(out, "show the cached schema") {
		t.Fatalf("usage must document the schema command; out=%q", out)
	}
}

// TestBWSAccessTokenGuards pins the two config-time refusals on
// [bws].accessToken: a bws: URI (chicken-and-egg) and a raw, non-URI value.
// Both are usage errors resolved before any secret is touched.
func TestBWSAccessTokenGuards(t *testing.T) {
	t.Run("bws: token URI is rejected", func(t *testing.T) {
		cfg := writeFile(t, t.TempDir(), "config.toml", `
[bws]
accessToken = "bws:self"
[hosts.h]
provider   = "postgres"
credential = "bws:secret-id"
`, 0o600)
		code, _, errb := run(t, "query", "--host", "h", "--config", cfg, "SELECT 1")
		if code != 1 || !strings.Contains(errb, "chicken-and-egg") {
			t.Fatalf("code=%d err=%q", code, errb)
		}
	})
	t.Run("raw token value is rejected", func(t *testing.T) {
		cfg := writeFile(t, t.TempDir(), "config.toml", `
[bws]
accessToken = "raw-token-value"
[hosts.h]
provider   = "postgres"
credential = "bws:secret-id"
`, 0o600)
		code, _, errb := run(t, "query", "--host", "h", "--config", cfg, "SELECT 1")
		if code != 1 || !strings.Contains(errb, "credential URI") {
			t.Fatalf("code=%d err=%q", code, errb)
		}
	})
}
