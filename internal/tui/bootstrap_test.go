package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/dblist"
	"github.com/geraldcsoftware/db-query/internal/session"
)

func TestHostPickerEnterSelects(t *testing.T) {
	p := newHostPicker([]string{"alpha", "beta", "gamma"}, nil)
	// Move down once (alpha -> beta), then select.
	var cmd tea.Cmd
	p, cmd = p.update(tea.KeyPressMsg{Code: tea.KeyDown})
	p, cmd = p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.chosen != "beta" {
		t.Fatalf("chosen = %q, want beta", p.chosen)
	}
	if cmd == nil {
		t.Fatal("expected a command signalling selection")
	}
}

func TestHostPickerEscQuits(t *testing.T) {
	p := newHostPicker([]string{"alpha"}, nil)
	_, cmd := p.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", msg)
	}
}

// TestDatabasePickerStartsOnConfiguredDatabase: the host's own database is
// the likeliest answer, so accepting it must not mean hunting for it first.
func TestDatabasePickerStartsOnConfiguredDatabase(t *testing.T) {
	p := newDatabasePicker([]string{"alpha", "reporting", "beta"}, "reporting", "testpg", nil, nil)
	if p.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", p.cursor)
	}
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.chosen != "reporting" {
		t.Fatalf("chosen = %q, want reporting", p.chosen)
	}
}

// TestDatabasePickerUnknownConfiguredDatabase: a configured database the host
// no longer has must not leave the cursor pointing at nothing.
func TestDatabasePickerUnknownConfiguredDatabase(t *testing.T) {
	p := newDatabasePicker([]string{"alpha", "beta"}, "gone", "testpg", nil, nil)
	if p.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", p.cursor)
	}
}

// stubPickers replaces the picker runner for one test, recording every picker
// it is asked to run and answering with the choice keyed on that picker's
// prompt. A picker with no prepared answer fails the test: a prompt appearing
// where the flow should have had one is exactly the regression these tests
// guard against.
func stubPickers(t *testing.T, answers map[string]string) *[]picker {
	t.Helper()
	shown := &[]picker{}
	saved := runPicker
	runPicker = func(p picker) (picker, error) {
		*shown = append(*shown, p)
		answer, ok := answers[p.prompt]
		if !ok {
			t.Errorf("unexpected picker %q", p.prompt)
		}
		p.chosen = answer
		return p, nil
	}
	t.Cleanup(func() { runPicker = saved })
	return shown
}

func prompts(shown []picker) []string {
	out := make([]string, 0, len(shown))
	for _, p := range shown {
		out = append(out, p.prompt)
	}
	return out
}

// bootstrapConfig writes a config with two hosts: testpg names a database,
// nodb leaves it unset so the resolved session has none.
func bootstrapConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("DBQ_TUI_TEST_PW", "pw")
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[hosts.testpg]
provider   = "postgres"
host       = "localhost"
database   = "testdb"
username   = "app"
credential = "env:DBQ_TUI_TEST_PW"

[hosts.nodb]
provider   = "postgres"
host       = "localhost"
username   = "app"
credential = "env:DBQ_TUI_TEST_PW"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// listingPsql stubs the catalog listing with two database names and isolates
// the cache the listing refreshes.
func listingPsql(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeBin(t, "psql", "cat > /dev/null\nprintf 'datname\\nalpha\\ntestdb\\n'")
}

// failingPsql stubs a catalog listing that cannot reach the host, and
// isolates the cache so only what a test seeds is there to fall back on.
func failingPsql(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeBin(t, "psql", "cat > /dev/null\necho 'FATAL: could not connect' >&2\nexit 1")
}

func bootstrapFlags(cfg string) session.CommonFlags {
	return session.CommonFlags{Config: cfg, Output: "text", Timeout: 10 * time.Second}
}

// TestBootstrapPicksDatabaseAfterHostPicker is the flow the interactive
// launch exists for: no --host and no --database, so both are chosen.
func TestBootstrapPicksDatabaseAfterHostPicker(t *testing.T) {
	cfg := bootstrapConfig(t)
	listingPsql(t)
	shown := stubPickers(t, map[string]string{hostPrompt: "testpg", databasePrompt: "alpha"})

	var errb strings.Builder
	r, code := bootstrap(bootstrapFlags(cfg), "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if got := prompts(*shown); !reflect.DeepEqual(got, []string{hostPrompt, databasePrompt}) {
		t.Fatalf("prompts = %v", got)
	}
	if names := (*shown)[1].names; !reflect.DeepEqual(names, []string{"alpha", "testdb"}) {
		t.Fatalf("offered names = %v, want the live listing", names)
	}
	if r.Host.Database != "alpha" {
		t.Fatalf("database = %q, want alpha", r.Host.Database)
	}
}

// TestBootstrapKeepsExplicitDatabase: --database/DB_QUERY_DATABASE is an
// answer already given, even when the host itself was picked interactively.
func TestBootstrapKeepsExplicitDatabase(t *testing.T) {
	cfg := bootstrapConfig(t)
	listingPsql(t)
	shown := stubPickers(t, map[string]string{hostPrompt: "testpg"})

	c := bootstrapFlags(cfg)
	c.Database = "explicit"
	var errb strings.Builder
	r, code := bootstrap(c, "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if got := prompts(*shown); !reflect.DeepEqual(got, []string{hostPrompt}) {
		t.Fatalf("prompts = %v, want the host picker only", got)
	}
	if r.Host.Database != "explicit" {
		t.Fatalf("database = %q, want explicit", r.Host.Database)
	}
}

// TestBootstrapKeepsConfiguredDatabase: passing --host to a block that names
// a database is the established way to launch straight into the TUI, so it
// must stay promptless.
func TestBootstrapKeepsConfiguredDatabase(t *testing.T) {
	cfg := bootstrapConfig(t)
	listingPsql(t)
	shown := stubPickers(t, nil)

	c := bootstrapFlags(cfg)
	c.Host = "testpg"
	var errb strings.Builder
	r, code := bootstrap(c, "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if len(*shown) != 0 {
		t.Fatalf("prompts = %v, want none", prompts(*shown))
	}
	if r.Host.Database != "testdb" {
		t.Fatalf("database = %q, want testdb", r.Host.Database)
	}
}

// TestBootstrapPicksDatabaseWhenNoneResolved: a session with no database at
// all cannot run a query, so it is worth a prompt even though --host was
// given explicitly.
func TestBootstrapPicksDatabaseWhenNoneResolved(t *testing.T) {
	cfg := bootstrapConfig(t)
	listingPsql(t)
	shown := stubPickers(t, map[string]string{databasePrompt: "alpha"})

	c := bootstrapFlags(cfg)
	c.Host = "nodb"
	var errb strings.Builder
	r, code := bootstrap(c, "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if got := prompts(*shown); !reflect.DeepEqual(got, []string{databasePrompt}) {
		t.Fatalf("prompts = %v, want the database picker only", got)
	}
	if r.Host.Database != "alpha" {
		t.Fatalf("database = %q, want alpha", r.Host.Database)
	}
}

// TestBootstrapDatabasePickerQuitAborts: quitting a picker is "nothing to
// do", so it must abort startup silently and unlaunchably, exactly as
// quitting the host picker does.
func TestBootstrapDatabasePickerQuitAborts(t *testing.T) {
	cfg := bootstrapConfig(t)
	listingPsql(t)
	stubPickers(t, map[string]string{hostPrompt: "testpg", databasePrompt: ""})

	var errb strings.Builder
	r, code := bootstrap(bootstrapFlags(cfg), "v1.2.3", &errb)
	if code != 0 {
		t.Fatalf("code=%d, want 0", code)
	}
	if errb.String() != "" {
		t.Fatalf("stderr = %q, want silence", errb.String())
	}
	if shouldLaunch(r) {
		t.Fatal("a quit picker must not leave a launchable session")
	}
}

func resolvedFor(t *testing.T, name string) session.Resolved {
	t.Helper()
	r := testResolved(t)
	r.Host = config.HostConfig{Name: name, Host: "localhost", Database: "testdb"}
	return r
}

// TestDatabaseChoicesFallsBackToCache: a host that cannot be reached right
// now still has a usable session if a previous listing cached its names.
func TestDatabaseChoicesFallsBackToCache(t *testing.T) {
	failingPsql(t)
	if err := dblist.Write(dblist.CachePath("cached"), []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	var errb strings.Builder
	names, code := databaseChoices(resolvedFor(t, "cached"), bootstrapFlags(""), &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if !reflect.DeepEqual(names, []string{"one", "two"}) {
		t.Fatalf("names = %v, want the cached list", names)
	}
	if errb.String() != "" {
		t.Fatalf("a covered failure must stay quiet, stderr = %q", errb.String())
	}
}

// TestDatabaseChoicesFailsWithNothingToOffer: with neither a live listing nor
// a cache there is nothing to query, so startup reports the listing's own
// failure rather than opening an unusable TUI.
func TestDatabaseChoicesFailsWithNothingToOffer(t *testing.T) {
	failingPsql(t)
	var errb strings.Builder
	names, code := databaseChoices(resolvedFor(t, "uncached"), bootstrapFlags(""), &errb)
	if code == 0 {
		t.Fatal("code = 0, want non-zero")
	}
	if names != nil {
		t.Fatalf("names = %v, want none", names)
	}
	if !strings.Contains(errb.String(), "could not connect") {
		t.Fatalf("stderr = %q, want the listing's own error", errb.String())
	}
}
