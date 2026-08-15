package tui

import (
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/schema"
)

// catalogPsql stubs a psql that answers both queries the startup flow makes:
// the database listing, and the introspection the gate runs when a chosen
// database has no cached schema. They are told apart by the SQL on stdin,
// which is how the real client receives it.
func catalogPsql(t *testing.T, introspectExit int) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	introspect := "printf 'table_schema,table_name,column_name,data_type,is_nullable\\npublic,widgets,id,integer,NO\\n'"
	if introspectExit != 0 {
		introspect = "echo 'FATAL: permission denied for database' >&2\nexit 1"
	}
	fakeBin(t, "psql", `sql=$(cat)
case "$sql" in
  *pg_database*) printf 'datname\nalpha\ntestdb\n' ;;
  *) `+introspect+` ;;
esac`)
}

// stubPickerAnswers answers each picker in turn rather than by prompt, so a
// test can drive a flow that shows the same picker more than once — which is
// exactly what declining the introspect gate does.
func stubPickerAnswers(t *testing.T, answers ...string) *[]picker {
	t.Helper()
	shown := &[]picker{}
	saved := runPicker
	i := 0
	runPicker = func(p picker) (picker, error) {
		*shown = append(*shown, p)
		if i >= len(answers) {
			t.Fatalf("picker %q shown %d times, more than the test prepared answers for", p.prompt, i+1)
		}
		p.chosen = answers[i]
		i++
		return p, nil
	}
	t.Cleanup(func() { runPicker = saved })
	return shown
}

func TestSchemaMarksTagOnlyTheUncachedDatabases(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedSchema(t, "localhost", "alpha")
	host := config.HostConfig{Name: "testpg", Host: "localhost"}

	marks := schemaMarks(host, []string{"alpha", "beta"})
	if _, tagged := marks["alpha"]; tagged {
		t.Fatal("alpha has a cached schema and must not be marked")
	}
	if marks["beta"] != noSchemaMark {
		t.Fatalf("marks[beta] = %q, want %q", marks["beta"], noSchemaMark)
	}
}

// TestIntrospectCachesTheChosenDatabaseNotTheSessionsOwn: the gate introspects
// a database the session has not switched to yet, so it must key the cache on
// the choice rather than on where the session currently points.
func TestIntrospectCachesTheChosenDatabaseNotTheSessionsOwn(t *testing.T) {
	catalogPsql(t, 0)
	r := testResolved(t) // host localhost, database testdb

	if err := introspect(r, "alpha", bootstrapFlags("")); err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if !schema.Exists(schema.CachePath("localhost", "alpha")) {
		t.Fatal("the chosen database's cache was not written")
	}
	if schema.Exists(schema.CachePath("localhost", "testdb")) {
		t.Fatal("the session's own database was introspected instead")
	}
	if r.Host.Database != "testdb" {
		t.Fatalf("caller's session was mutated: database = %q", r.Host.Database)
	}
}

func TestIntrospectReturnsTheClientsFailure(t *testing.T) {
	catalogPsql(t, 1)
	err := introspect(testResolved(t), "alpha", bootstrapFlags(""))
	if err == nil {
		t.Fatal("a failed introspection must not report success")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the client's own message", err)
	}
}

// TestBootstrapIntrospectsOnConfirmation is the gate's happy path: choosing a
// database with no cached schema offers to build one, and accepting lands the
// session on it with a schema to browse.
func TestBootstrapIntrospectsOnConfirmation(t *testing.T) {
	cfg := bootstrapConfig(t)
	catalogPsql(t, 0)
	shown := stubPickerAnswers(t, "testpg", "alpha")
	asked := stubConfirms(t, true)

	var errb strings.Builder
	r, code := bootstrap(bootstrapFlags(cfg), "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if *asked != 1 {
		t.Fatalf("confirmations asked = %d, want 1", *asked)
	}
	if r.Host.Database != "alpha" {
		t.Fatalf("database = %q, want alpha", r.Host.Database)
	}
	if !schema.Exists(schema.CachePath("localhost", "alpha")) {
		t.Fatal("the session launched without the schema it just agreed to build")
	}
	if len(*shown) != 2 {
		t.Fatalf("pickers shown = %d, want host then database", len(*shown))
	}
}

// TestBootstrapDecliningIntrospectReturnsToTheList: declining means "not this
// one", not "open it without a schema" — the session's rule is that whatever
// it lands on has one.
func TestBootstrapDecliningIntrospectReturnsToTheList(t *testing.T) {
	cfg := bootstrapConfig(t)
	catalogPsql(t, 0)
	shown := stubPickerAnswers(t, "testpg", "alpha", "")
	asked := stubConfirms(t, false)

	var errb strings.Builder
	r, code := bootstrap(bootstrapFlags(cfg), "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if *asked != 1 {
		t.Fatalf("confirmations asked = %d, want 1", *asked)
	}
	if len(*shown) != 3 {
		t.Fatalf("pickers shown = %d, want the database picker offered again", len(*shown))
	}
	if schema.Exists(schema.CachePath("localhost", "alpha")) {
		t.Fatal("declining still introspected the database")
	}
	if shouldLaunch(r) {
		t.Fatal("quitting the second picker must not leave a launchable session")
	}
}

// TestBootstrapFailedIntrospectReturnsToTheList: the invariant holds on the
// error path too. A database that could not be introspected is not one the
// session may open.
func TestBootstrapFailedIntrospectReturnsToTheList(t *testing.T) {
	cfg := bootstrapConfig(t)
	catalogPsql(t, 1)
	shown := stubPickerAnswers(t, "testpg", "alpha", "")
	stubConfirms(t, true)

	var errb strings.Builder
	r, code := bootstrap(bootstrapFlags(cfg), "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if len(*shown) != 3 {
		t.Fatalf("pickers shown = %d, want the database picker offered again", len(*shown))
	}
	if !strings.Contains(errb.String(), "permission denied") {
		t.Fatalf("stderr = %q, want the introspection failure reported", errb.String())
	}
	if shouldLaunch(r) {
		t.Fatal("a failed introspection must not leave a launchable session")
	}
}

// TestBootstrapSkipsTheGateForACachedDatabase: the gate is a cost, so it must
// not be paid by the common case of choosing a database already browsed.
func TestBootstrapSkipsTheGateForACachedDatabase(t *testing.T) {
	cfg := bootstrapConfig(t)
	catalogPsql(t, 1) // any introspection at all would fail this test loudly
	seedSchema(t, "localhost", "alpha")
	stubPickerAnswers(t, "testpg", "alpha")
	asked := stubConfirms(t, false)

	var errb strings.Builder
	r, code := bootstrap(bootstrapFlags(cfg), "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if *asked != 0 {
		t.Fatalf("confirmations asked = %d, want none", *asked)
	}
	if r.Host.Database != "alpha" {
		t.Fatalf("database = %q, want alpha", r.Host.Database)
	}
}
