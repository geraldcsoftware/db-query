package cli

import (
	"strings"
	"testing"

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
