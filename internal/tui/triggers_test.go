package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

type fakeAdapter struct{ adapter.Adapter }

func (fakeAdapter) PreviewSQL(table string) string { return "SELECT * FROM " + table + " LIMIT 100;" }

func TestSchemaShortcutTriggersRun(t *testing.T) {
	m := newTestModel(t)
	m.session.Adapter = fakeAdapter{}
	seedSchemaCache(t, "", "") // matches m.session.Host's zero-value Host/Database
	m.schema = newSchemaPane(m.session.Host)
	m.focus = paneSchema
	r := &controlledRunner{release: make(chan struct{})}
	defer close(r.release)
	m.runner = r.run

	updated, cmd := m.Update(f5Msg())
	if !updated.(model).running {
		t.Fatal("schema-pane F5 did not start a run")
	}
	if cmd == nil {
		t.Fatal("expected a run command")
	}
}

func TestRunQueryFromSchemaBuildsPreviewSQL(t *testing.T) {
	m := newTestModel(t)
	m.session.Adapter = fakeAdapter{}
	seedSchemaCache(t, "", "")
	m.schema = newSchemaPane(m.session.Host)
	sql, ok := m.schemaRunSQL()
	if !ok {
		t.Fatal("expected a selected table")
	}
	// seedSchemaCache tags every row with table_schema "public", so the
	// selected table's Schema field is non-empty and schemaRunSQL
	// schema-qualifies the name it passes to PreviewSQL.
	if sql != "SELECT * FROM public.orders LIMIT 100;" {
		t.Fatalf("sql = %q", sql)
	}
}

// TestEveryRunKeyTriggersQueryPaneRun: a run key asks the editor what to run
// and the run begins when the answer arrives, since an editor that has to ask
// another process cannot answer on the event loop's own goroutine.
func TestEveryRunKeyTriggersQueryPaneRun(t *testing.T) {
	r := &controlledRunner{release: make(chan struct{})}
	defer close(r.release)

	for _, msg := range []tea.KeyPressMsg{f5Msg(), ctrlEnterMsg(), cmdEnterMsg()} {
		m := newTestModel(t)
		m.focus = paneQuery
		m.query.setValue("select 1")
		m.runner = r.run

		asked, cmd := m.Update(msg)
		if cmd == nil {
			t.Fatalf("%v did not ask the editor what to run", msg)
		}
		if asked.(model).running {
			t.Fatalf("%v started a run before the editor had answered", msg)
		}

		answer, ok := cmd().(queryTextMsg)
		if !ok {
			t.Fatalf("%v answered with something other than the query text", msg)
		}
		if answer.sql != "select 1" {
			t.Fatalf("%v read %q from the buffer", msg, answer.sql)
		}

		running, cmd := asked.Update(answer)
		if !running.(model).running {
			t.Fatalf("%v did not start a run once the text arrived", msg)
		}
		if cmd == nil {
			t.Fatalf("%v produced no run command", msg)
		}
	}
}

func f5Msg() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyF5} }

// cmdEnterMsg is Cmd+Enter: the Enter key code carrying the Super modifier,
// which stringifies as "super+enter". Reaching the program at all depends on
// the terminal not claiming the chord for itself first — Ghostty binds it to
// toggle_fullscreen by default — which is why it is one of three bindings for
// this action rather than the only one.
func cmdEnterMsg() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModSuper}
}

// ctrlEnterMsg is the Ctrl+Enter a terminal reports once the Kitty keyboard
// protocol is negotiated: the Enter key code carrying a Ctrl modifier, which
// stringifies as "ctrl+enter". Without that protocol the chord is
// indistinguishable from plain Enter, which is why F5 stays bound to the same
// action.
func ctrlEnterMsg() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}
}
