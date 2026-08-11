package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

type fakeAdapter struct{ adapter.Adapter }

func (fakeAdapter) PreviewSQL(table string) string { return "SELECT * FROM " + table + " LIMIT 100;" }

func TestSchemaShortcutTriggersRun(t *testing.T) {
	m := newTestModel()
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
	m := newTestModel()
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

func TestF5AndCtrlEnterBothTriggerQueryPaneRun(t *testing.T) {
	m := newTestModel()
	m.focus = paneQuery
	m.query.setValue("select 1")
	r := &controlledRunner{release: make(chan struct{})}
	defer close(r.release)
	m.runner = r.run

	for _, msg := range []tea.KeyMsg{f5Msg(), ctrlEnterMsg()} {
		mm := newTestModel()
		mm.focus = paneQuery
		mm.query.setValue("select 1")
		mm.runner = r.run
		updated, cmd := mm.Update(msg)
		if !updated.(model).running {
			t.Fatalf("%v did not start a run", msg)
		}
		if cmd == nil {
			t.Fatalf("%v produced no command", msg)
		}
	}
}

func f5Msg() tea.KeyMsg        { return tea.KeyMsg{Type: tea.KeyF5} }
func ctrlEnterMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlAt} }
