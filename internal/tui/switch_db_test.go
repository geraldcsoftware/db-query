package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// switcherModel is a model with the popup already open on a known list, so a
// test can drive the states without going through the live listing F2 fires.
func switcherModel(t *testing.T, names ...string) model {
	t.Helper()
	// Isolate the schema cache: these tests turn on which databases have one,
	// so a seed from another test — or the developer's own cache — must not be
	// visible here.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := newModel(testResolved(t), bootstrapFlags(""), "v1", nil, newTextareaEditor())
	m.width, m.height = 100, 30
	m.recomputeLayout()
	m.switcherOpen = true
	m.switcher = dbSwitcher{chooser: newChooser(names, switcherRows)}
	m.switcher.marks = schemaMarks(m.session.Host, names)
	return m
}

func press(m model, key string) model {
	next, _ := m.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key})
	return next.(model)
}

func pressCode(m model, code rune) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return next.(model), cmd
}

// TestF2OpensTheSwitcher is the entry point: the key has to reach the popup
// from any pane, since switching database is a session-level action rather
// than something one pane owns.
func TestF2OpensTheSwitcher(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, focus := range []pane{paneSchema, paneSaved, paneQuery, paneResults} {
		m := newModel(testResolved(t), bootstrapFlags(""), "v1", nil, newTextareaEditor())
		m.focus = focus
		next, cmd := pressCode(m, tea.KeyF2)
		if !next.switcherOpen {
			t.Fatalf("F2 with focus %v did not open the switcher", focus)
		}
		if cmd == nil {
			t.Fatalf("F2 with focus %v did not ask for a fresh listing", focus)
		}
	}
}

// TestSwitcherSwallowsKeysMeantForThePanes: while the popup is open it is the
// only thing keys reach, or typing a filter would edit the SQL buffer behind
// it and Esc would quit the whole program.
func TestSwitcherSwallowsKeysMeantForThePanes(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	m.focus = paneQuery
	before := queryText(m)

	m = press(m, "b")
	if queryText(m) != before {
		t.Fatalf("a keystroke reached the Query pane through the popup: %q", queryText(m))
	}
	if m.switcher.filter != "b" {
		t.Fatalf("filter = %q, want the keystroke to have gone to the popup", m.switcher.filter)
	}

	next, cmd := pressCode(m, tea.KeyEscape)
	if cmd != nil {
		t.Fatal("Esc quit the program instead of closing the popup")
	}
	if next.switcherOpen {
		t.Fatal("Esc did not close the popup")
	}
}

// TestSwitcherMouseClicksDoNotMoveFocusBehindIt: the click lands on panes the
// user cannot see, so honouring it would move focus somewhere invisible.
func TestSwitcherMouseClicksDoNotMoveFocusBehindIt(t *testing.T) {
	m := switcherModel(t, "alpha")
	m.focus = paneSchema
	r := m.rects[paneResults]

	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.x0 + 1, Y: r.y0 + 1})
	if got := next.(model).focus; got != paneSchema {
		t.Fatalf("focus = %v, want it unchanged while the popup is open", got)
	}
}

// TestSwitchingRebuildsWhatWasScopedToTheOldDatabase is the switch itself:
// the Schema pane follows the new database, the Results pane is emptied
// because its rows came from the old one, and the SQL survives because
// re-running it elsewhere is why anyone switches.
func TestSwitchingRebuildsWhatWasScopedToTheOldDatabase(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	seedSchema(t, m.session.Host.Host, "beta")
	m.switcher.marks = schemaMarks(m.session.Host, m.switcher.names)
	m.query.setValue("select 1")
	m.results.showRows(adapter.Rows{Columns: []string{"a"}, Rows: [][]*string{{nil}}})
	m.switcher.cursorTo("beta")

	got, _ := pressCode(m, tea.KeyEnter)

	if got.session.Host.Database != "beta" {
		t.Fatalf("database = %q, want beta", got.session.Host.Database)
	}
	if got.switcherOpen {
		t.Fatal("the popup stayed open after switching")
	}
	if queryText(got) != "select 1" {
		t.Fatalf("query buffer = %q, want it kept across the switch", queryText(got))
	}
	if strings.Contains(ansi.Strip(got.results.view()), "a") {
		t.Fatalf("the old database's rows survived the switch:\n%s", got.results.view())
	}
	if len(got.schema.tables) != 1 || got.schema.tables[0].Name != "widgets" {
		t.Fatalf("schema pane = %+v, want the new database's cached catalogue", got.schema.tables)
	}
}

// TestSwitchingDiscardsAnInFlightRunsResult is the correctness fix the
// generation stamp exists for: rows fetched from the database just left must
// never land under a top bar naming the one just arrived at.
func TestSwitchingDiscardsAnInFlightRunsResult(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	seedSchema(t, m.session.Host.Host, "beta")
	m.switcher.marks = schemaMarks(m.session.Host, m.switcher.names)

	// A run dispatched against the current database, captured as it would be.
	m.runner = func(context.Context, session.Resolved, string) (adapter.Rows, bool, error) {
		return adapter.Rows{}, false, nil
	}
	cmd := m.startRun("select 1", "")
	if !m.running {
		t.Fatal("the run did not start")
	}
	stale := cmd().(queryResultMsg)

	m.switcher.cursorTo("beta")
	after, _ := pressCode(m, tea.KeyEnter)
	if after.running {
		t.Fatal("the switch left the session marked as running")
	}

	stale.rows = adapter.Rows{Columns: []string{"leaked"}, Rows: [][]*string{{nil}}}
	final, _ := after.Update(stale)
	if strings.Contains(ansi.Strip(final.(model).results.view()), "leaked") {
		t.Fatal("a result from the old database was rendered after the switch")
	}
}

// TestSwitchingToTheCurrentDatabaseIsANoOp: re-picking where you already are
// should not cost the Results pane its contents.
func TestSwitchingToTheCurrentDatabaseIsANoOp(t *testing.T) {
	m := switcherModel(t, "testdb", "beta")
	m.results.showRows(adapter.Rows{Columns: []string{"kept"}, Rows: [][]*string{{nil}}})
	m.switcher.cursorTo("testdb")

	got, _ := pressCode(m, tea.KeyEnter)
	if got.switcherOpen {
		t.Fatal("the popup stayed open")
	}
	if !strings.Contains(ansi.Strip(got.results.view()), "kept") {
		t.Fatal("re-picking the current database cleared the Results pane")
	}
}

// TestChoosingAnUnintrospectedDatabaseAsksFirst: the popup enforces the same
// invariant the startup flow does — nothing is switched to without a schema.
func TestChoosingAnUnintrospectedDatabaseAsksFirst(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	m.switcher.cursorTo("beta") // no cache seeded, so it needs introspecting

	got, _ := pressCode(m, tea.KeyEnter)
	if got.switcher.state != switcherConfirming {
		t.Fatalf("state = %v, want the confirmation", got.switcher.state)
	}
	if got.session.Host.Database == "beta" {
		t.Fatal("the session switched before the question was answered")
	}
	if got.switcher.pending != "beta" {
		t.Fatalf("pending = %q, want beta", got.switcher.pending)
	}

	// Declining returns to the list without switching.
	d, _ := pressCode(got, tea.KeyEscape)
	if !d.switcherOpen || d.switcher.state != switcherChoosing {
		t.Fatal("declining should return to the list, not close the popup")
	}
	if d.session.Host.Database == "beta" {
		t.Fatal("declining still switched the session")
	}
}

// TestFailedIntrospectionDoesNotSwitch: the invariant holds on the error path.
func TestFailedIntrospectionDoesNotSwitch(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	m.switcher.state = switcherIntrospecting
	m.switcher.pending = "beta"

	next, _ := m.Update(introspectDoneMsg{database: "beta", err: context.Canceled})
	got := next.(model)
	if got.session.Host.Database == "beta" {
		t.Fatal("a cancelled introspection still switched the session")
	}
	if got.switcher.state != switcherChoosing {
		t.Fatalf("state = %v, want back at the list", got.switcher.state)
	}
	if !strings.Contains(got.switcher.err, "cancelled") {
		t.Fatalf("err = %q, want it to say the switch did not happen", got.switcher.err)
	}
}

// TestListingRefreshKeepsTheCursorWhereItWas: the live listing lands a moment
// after the popup opens, and must not move the highlight under a user already
// reaching for Enter.
func TestListingRefreshKeepsTheCursorWhereItWas(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	m.switcher.cursorTo("beta")

	next, _ := m.Update(databaseListMsg{names: []string{"aaa", "alpha", "beta", "zzz"}})
	got := next.(model)
	if name, _ := got.switcher.selected(); name != "beta" {
		t.Fatalf("selected = %q, want beta held across the refresh", name)
	}
	if got.switcher.loading {
		t.Fatal("the popup still reports the listing as in flight")
	}
}

// TestListingFailureOnlyShowsWhenThereIsNothingElse: a stale cache the user can
// still choose from is worth more than an error about the refresh.
func TestListingFailureOnlyShowsWhenThereIsNothingElse(t *testing.T) {
	withCache := switcherModel(t, "alpha")
	next, _ := withCache.Update(databaseListMsg{err: context.DeadlineExceeded})
	if got := next.(model).switcher.err; got != "" {
		t.Fatalf("err = %q, want silence while the cached names are usable", got)
	}

	empty := switcherModel(t)
	next, _ = empty.Update(databaseListMsg{err: context.DeadlineExceeded})
	if got := next.(model).switcher.err; got == "" {
		t.Fatal("a listing failure with nothing cached must be reported")
	}
}

// TestSwitcherViewFitsTheScreen: the composited frame is what Bubble Tea
// renders, and a view taller than the terminal loses its top rows silently.
func TestSwitcherViewFitsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 16}, {40, 12}} {
		m := switcherModel(t, "alpha", "beta", "gamma")
		m.width, m.height = size[0], size[1]
		m.recomputeLayout()

		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > size[1] {
			t.Fatalf("%dx%d: view is %d lines, want at most %d", size[0], size[1], len(lines), size[1])
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Fatalf("%dx%d: line %d is %d cells wide", size[0], size[1], i, w)
			}
		}
	}
}

// TestSwitcherViewShowsItsStates: each state has to say what it is waiting for,
// or an introspection in progress is indistinguishable from a stuck popup.
func TestSwitcherViewShowsItsStates(t *testing.T) {
	m := switcherModel(t, "alpha", "beta")
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, "Switch database") {
		t.Fatalf("choosing state missing its heading:\n%s", out)
	}

	m.switcher.state = switcherConfirming
	m.switcher.pending = "beta"
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "no cached schema") || !strings.Contains(out, "Introspect it now?") {
		t.Fatalf("confirming state missing its question:\n%s", out)
	}

	m.switcher.state = switcherIntrospecting
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, "introspecting beta") {
		t.Fatalf("introspecting state missing its progress line:\n%s", out)
	}
}

// TestBottomBarKeepsTheExitsWhenItCannotFitEverything: the bar is clipped from
// the right, which is exactly where the exits sit. Every hint set is clipped
// the same way, and each has to keep its own way out.
func TestBottomBarKeepsTheExitsWhenItCannotFitEverything(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode hintMode
		exit string
	}{
		{"default", hintDefault, "Esc"},
		{"modal editor", hintModalEditor, "F10"},
		{"results", hintResults, "Esc"},
	} {
		for _, w := range []int{120, 90, 60, 40} {
			bar := bottomBarHint(w, tc.mode)
			out := ansi.Strip(bar)
			if !strings.Contains(out, tc.exit) || !strings.Contains(out, "quit") {
				t.Fatalf("%s bar at width %d dropped the way out: %q", tc.name, w, out)
			}
			if ansi.StringWidth(bar) > w {
				t.Fatalf("%s bar at width %d is %d cells wide", tc.name, w, ansi.StringWidth(bar))
			}
		}
		if !strings.Contains(ansi.Strip(bottomBarHint(120, tc.mode)), "F2") {
			t.Fatalf("%s: a wide bar should still advertise the switch key", tc.name)
		}
	}
}
