package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/dblist"
	"github.com/geraldcsoftware/db-query/internal/savedquery"
)

// complete runs the hidden __complete command and returns its stdout,
// asserting the fail-silent contract: it must always exit 0.
func complete(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb strings.Builder
	code := Run(append([]string{"__complete"}, args...), &out, &errb)
	if code != 0 {
		t.Fatalf("__complete exited %d (must always be 0); err=%q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("__complete must never write stderr, got %q", errb.String())
	}
	return out.String()
}

func mustSave(t *testing.T, name, category, provider, sql string) {
	t.Helper()
	if _, err := savedquery.Save(name, category, provider, sql, false); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteHostsWithDescriptions(t *testing.T) {
	cfg := writeFile(t, t.TempDir(), "config.toml", `
[hosts.prod-fcubs]
provider = "postgres"
database = "vintegration_fcubs"
[hosts.uat-switch]
provider = "sqlserver"
database = "postbridge"
`, 0o600)
	got := complete(t, "--config", cfg, "host")
	want := "prod-fcubs\tpostgres · vintegration_fcubs\nuat-switch\tsqlserver · postbridge\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCompleteHostNoDatabase(t *testing.T) {
	cfg := writeFile(t, t.TempDir(), "config.toml", `
[hosts.h]
provider = "postgres"
`, 0o600)
	if got := complete(t, "--config", cfg, "host"); got != "h\tpostgres\n" {
		t.Fatalf("host with no database = %q", got)
	}
}

func TestCompleteHostReadsEnvConfig(t *testing.T) {
	cfg := writeFile(t, t.TempDir(), "config.toml", `
[hosts.only]
provider = "postgres"
database = "db1"
`, 0o600)
	t.Setenv("DB_QUERY_CONFIG", cfg)
	if got := complete(t, "host"); got != "only\tpostgres · db1\n" {
		t.Fatalf("env-config host = %q", got)
	}
}

// Profiles are not connectable, so offering one as a --host candidate would
// complete to something that can only fail. Inherited keys still show in the
// description, which is what makes a deduplicated config completable at all.
func TestCompleteHostExcludesProfiles(t *testing.T) {
	cfg := writeFile(t, t.TempDir(), "config.toml", `
[profiles.pg]
provider = "postgres"
database = "postgres"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`, 0o600)
	if got := complete(t, "--config", cfg, "host"); got != "node\tpostgres · postgres\n" {
		t.Fatalf("host completion = %q", got)
	}
}

func TestCompleteHostBadConfigIsSilent(t *testing.T) {
	if got := complete(t, "--config", "/nope/does-not-exist.toml", "host"); got != "" {
		t.Fatalf("a bad config must produce nothing, got %q", got)
	}
}

func TestCompleteSources(t *testing.T) {
	isolateStore(t)
	mustSave(t, "daily-recon", "ops", "postgres", "SELECT * FROM recon WHERE d = :'day'")
	mustSave(t, "month-close", "finance", "postgres", "SELECT 1")
	got := complete(t, "source")
	// List is sorted by category then name: finance/month-close, ops/daily-recon.
	want := "month-close\tfinance · SELECT 1\ndaily-recon\tops · SELECT * FROM recon WHERE d = :'day'\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCompleteSourcesCategoryFilter(t *testing.T) {
	isolateStore(t)
	mustSave(t, "a", "ops", "postgres", "SELECT 1")
	mustSave(t, "b", "finance", "postgres", "SELECT 2")
	if got := complete(t, "--category", "ops", "source"); got != "a\tops · SELECT 1\n" {
		t.Fatalf("category-filtered source = %q", got)
	}
}

func TestCompleteCategories(t *testing.T) {
	isolateStore(t)
	mustSave(t, "a", "ops", "postgres", "SELECT 1")
	mustSave(t, "b", "finance", "postgres", "SELECT 2")
	mustSave(t, "c", "finance", "postgres", "SELECT 3")
	// Sorted by category: finance (2), ops (1). Singular for the count of 1.
	want := "finance\t2 queries\nops\t1 query\n"
	if got := complete(t, "category"); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCompleteEmptyStoreIsSilent(t *testing.T) {
	isolateStore(t)
	if got := complete(t, "source"); got != "" {
		t.Fatalf("empty store source = %q", got)
	}
	if got := complete(t, "category"); got != "" {
		t.Fatalf("empty store category = %q", got)
	}
}

func TestCompleteUnknownAndMissingTargetIsSilent(t *testing.T) {
	if got := complete(t, "bogus"); got != "" {
		t.Fatalf("unknown target = %q", got)
	}
	if got := complete(t); got != "" {
		t.Fatalf("no target = %q", got)
	}
}

func TestCompletionZshEmitsEmbeddedScript(t *testing.T) {
	var out, errb strings.Builder
	code := Run([]string{"completion", "zsh"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	s := out.String()
	if !strings.HasPrefix(s, "#compdef db-query") {
		t.Fatalf("script must start with #compdef db-query, got prefix %q", s[:min(40, len(s))])
	}
	if !strings.Contains(s, "compdef _db-query db-query") {
		t.Fatal("script must self-register via compdef for the source install route")
	}
	if s != zshCompletionScript {
		t.Fatal("completion zsh must emit the embedded script verbatim")
	}
}

func TestCompletionUnknownAndMissingShell(t *testing.T) {
	code, _, errb := run(t, "completion", "bash")
	if code != 1 || !strings.Contains(errb, "zsh") {
		t.Fatalf("unknown shell: code=%d err=%q", code, errb)
	}
	code, _, errb = run(t, "completion")
	if code != 1 || !strings.Contains(errb, "zsh") {
		t.Fatalf("missing shell: code=%d err=%q", code, errb)
	}
}

// seedDatabaseList isolates the cache and writes a database list for one host
// label, standing in for a prior `db-query --host <label> databases` run.
func seedDatabaseList(t *testing.T, label string, names ...string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := dblist.Write(dblist.CachePath(label), names); err != nil {
		t.Fatal(err)
	}
}

// TestCompleteDatabasesNamesOnly pins the candidate shape: bare names, one per
// line, with no tab and so no description (design.md §13.9).
func TestCompleteDatabasesNamesOnly(t *testing.T) {
	seedDatabaseList(t, "lionel", "postgres", "testdb", "reporting")
	got := complete(t, "--host", "lionel", "database")
	want := "postgres\ntestdb\nreporting\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "\t") {
		t.Fatalf("database candidates must carry no description: %q", got)
	}
}

func TestCompleteDatabasesShorthandHostFlag(t *testing.T) {
	seedDatabaseList(t, "lionel", "postgres")
	if got := complete(t, "-H", "lionel", "database"); got != "postgres\n" {
		t.Fatalf("-H must be accepted like --host, got %q", got)
	}
}

// TestCompleteDatabasesFromEnvHost: the helper is a subprocess and inherits the
// shell's exported host, so DB_QUERY_HOST works with no flag on the line.
func TestCompleteDatabasesFromEnvHost(t *testing.T) {
	seedDatabaseList(t, "lionel", "postgres", "testdb")
	t.Setenv("DB_QUERY_HOST", "lionel")
	if got := complete(t, "database"); got != "postgres\ntestdb\n" {
		t.Fatalf("DB_QUERY_HOST must supply the host, got %q", got)
	}
}

// TestCompleteDatabasesFlagBeatsEnvHost mirrors the CLI's own precedence.
func TestCompleteDatabasesFlagBeatsEnvHost(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := dblist.Write(dblist.CachePath("flagged"), []string{"from-flag"}); err != nil {
		t.Fatal(err)
	}
	if err := dblist.Write(dblist.CachePath("envd"), []string{"from-env"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_QUERY_HOST", "envd")
	if got := complete(t, "--host", "flagged", "database"); got != "from-flag\n" {
		t.Fatalf("--host must beat DB_QUERY_HOST, got %q", got)
	}
}

// TestCompleteDatabasesColdCacheIsSilent is the case zsh was verified against:
// no candidates and no message, which completes nothing rather than falling
// through to filename completion.
func TestCompleteDatabasesColdCacheIsSilent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := complete(t, "--host", "never-listed", "database"); got != "" {
		t.Fatalf("a cold cache must produce nothing, got %q", got)
	}
}

func TestCompleteDatabasesNoHostIsSilent(t *testing.T) {
	seedDatabaseList(t, "lionel", "postgres")
	if got := complete(t, "database"); got != "" {
		t.Fatalf("no determinable host must produce nothing, got %q", got)
	}
}

func TestCompleteDatabasesCorruptCacheIsSilent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := dblist.CachePath("lionel")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := complete(t, "--host", "lionel", "database"); got != "" {
		t.Fatalf("a corrupt cache must produce nothing, got %q", got)
	}
}

// TestCompleteDatabasesNeverConnects is the invariant the whole design exists
// to preserve: a credential that would fail loudly if resolved is never
// touched, and the database client is never invoked.
func TestCompleteDatabasesNeverConnects(t *testing.T) {
	seedDatabaseList(t, "lionel", "postgres")
	marker := filepath.Join(t.TempDir(), "psql-was-called")
	fakePsql(t, "touch "+marker+"\nexit 1\n")
	cfg := writeFile(t, t.TempDir(), "config.toml", `
[hosts.lionel]
provider   = "postgres"
host       = "lionel.internal"
credential = "env:DBQ_NO_SUCH_VAR"
`, 0o600)
	if got := complete(t, "--config", cfg, "--host", "lionel", "database"); got != "postgres\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("completion must never invoke the database client")
	}
}
