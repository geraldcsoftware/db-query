package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/precheck"
)

// dryRun runs a query with --dry-run and decodes the document.
func dryRun(t *testing.T, cfg string, args ...string) (int, precheck.Document, string) {
	t.Helper()
	full := append([]string{"query", "--host", "testpg", "--config", cfg, "--dry-run"}, args...)
	code, out, errb := run(t, full...)
	var doc precheck.Document
	if out != "" {
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("--dry-run did not produce a document: %q (err %q)", out, errb)
		}
	}
	return code, doc, errb
}

func TestDryRunClassifiesWithoutRunning(t *testing.T) {
	seedSchemaCache(t)
	// No fake client is installed: a dry run must not need one, because it
	// runs nothing.
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	code, doc, errb := dryRun(t, cfg, "SELECT id FROM people")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if doc.SchemaVersion != precheck.SchemaVersion {
		t.Errorf("schema_version: got %d, want %d", doc.SchemaVersion, precheck.SchemaVersion)
	}
	if doc.Status != precheck.StatusClassified {
		t.Errorf("status: got %q", doc.Status)
	}
	if doc.Decision.Action != "allow" {
		t.Errorf("a plain SELECT should be allowed, got %+v", doc.Decision)
	}
	if doc.PrecheckToken == nil || *doc.PrecheckToken == "" {
		t.Error("an allowed submission should carry a token")
	}
	if doc.Target.Host != "testpg" || doc.Target.Provider != "postgres" {
		t.Errorf("target: got %+v", doc.Target)
	}
	if doc.Source.Kind != "arg" {
		t.Errorf("source kind: got %q, want arg", doc.Source.Kind)
	}
}

func TestDryRunReportsButDoesNotRefuse(t *testing.T) {
	seedSchemaCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	code, doc, _ := dryRun(t, cfg, "DROP TABLE people")
	// Exit 0 means "this document is valid", not "this query is permitted".
	// Conflating the two would make a transport failure indistinguishable
	// from a refusal.
	if code != 0 {
		t.Errorf("a dry run reports rather than refusing; code=%d", code)
	}
	if doc.Decision.Action == "allow" {
		t.Errorf("a DROP must not be allowed on a read-only host: %+v", doc.Decision)
	}
	if doc.PrecheckToken != nil {
		t.Error("a refused submission must not be handed a token")
	}
}

func TestGateRefusesAWriteOnAReadOnlyHost(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `echo "should not run"; exit 0`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg, "DELETE FROM people")
	if code != 5 {
		t.Fatalf("code=%d, want 5; out=%q err=%q", code, out, errb)
	}
	if strings.Contains(out, "should not run") {
		t.Error("the client ran despite the refusal")
	}
	// The refusal has to tell an operator what to do instead.
	if !strings.Contains(errb, "readonly = false") {
		t.Errorf("refusal does not name the remedy: %q", errb)
	}
}

func TestGateAllowsAWriteOnAWritableHost(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `printf 'n\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	// A host declared writable is the operator having already said so; a
	// challenge on every INSERT would get the gate switched off.
	code, _, errb := run(t, "query", "--host", "testpg", "--config", writableConfig(t),
		"INSERT INTO people VALUES (1)")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestGateRefusesADropEvenOnAWritableHost(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `echo "should not run"; exit 0`)
	t.Setenv("DBQ_TEST_PW", "pw")
	code, out, _ := run(t, "query", "--host", "testpg", "--config", writableConfig(t), "DROP TABLE people")
	if code != 5 {
		t.Errorf("code=%d, want 5: a drop meets a human on any host", code)
	}
	if strings.Contains(out, "should not run") {
		t.Error("the client ran despite the refusal")
	}
}

func TestGateRefusesAClientDirective(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `echo "should not run"; exit 0`)
	t.Setenv("DBQ_TEST_PW", "pw")
	code, out, errb := run(t, "query", "--host", "testpg", "--config", writableConfig(t),
		"SELECT 1;\n\\! whoami")
	if code != 5 {
		t.Fatalf("code=%d, want 5; err=%q", code, errb)
	}
	if strings.Contains(out, "should not run") {
		t.Error("the client ran despite the refusal")
	}
	if !strings.Contains(errb, "client directive") && !strings.Contains(errb, "not commands to psql") {
		t.Errorf("refusal does not explain the directive: %q", errb)
	}
}

func TestPrecheckTokenMismatchIsItsOwnExitCode(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `printf 'n\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	_, doc, _ := dryRun(t, cfg, "SELECT id FROM people")
	if doc.PrecheckToken == nil {
		t.Fatal("expected a token")
	}
	// The same token, a different query: the file changed between the check
	// and the run, which is not the same failure as a policy refusal.
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--precheck", *doc.PrecheckToken, "SELECT name FROM people")
	if code != 6 {
		t.Fatalf("code=%d, want 6; err=%q", code, errb)
	}
}

func TestPrecheckTokenRoundTrips(t *testing.T) {
	seedSchemaCache(t)
	fakePsql(t, `printf 'id\n1\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	_, doc, _ := dryRun(t, cfg, "SELECT id FROM people")
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--precheck", *doc.PrecheckToken, "SELECT id FROM people")
	if code != 0 {
		t.Fatalf("a token from this exact invocation should pass: code=%d err=%q", code, errb)
	}
}

func TestDryRunHonoursTheEnvironmentForm(t *testing.T) {
	seedSchemaCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	t.Setenv("DB_QUERY_DRY_RUN", "1")
	code, out, errb := run(t, "query", "--host", "testpg", "--config", testConfig(t), "SELECT 1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	var doc precheck.Document
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("DB_QUERY_DRY_RUN did not produce a document: %q", out)
	}
}

func TestDryRunOmitsSQLUnlessAsked(t *testing.T) {
	seedSchemaCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	const literal = "4111111111111111"

	_, out, _ := run(t, "query", "--host", "testpg", "--config", cfg, "--dry-run",
		"SELECT * FROM people WHERE pan = '"+literal+"'")
	if strings.Contains(out, literal) {
		t.Error("the document leaks a SQL literal by default")
	}
	_, out, _ = run(t, "query", "--host", "testpg", "--config", cfg, "--dry-run", "--show-sql",
		"SELECT * FROM people WHERE pan = '"+literal+"'")
	if !strings.Contains(out, literal) {
		t.Error("--show-sql should include the SQL")
	}
}
