package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/savedquery"
)

func isolateStore(t *testing.T) {
	t.Helper()
	t.Setenv("DB_QUERY_QUERIES_DIR", t.TempDir())
}

func TestSavedPaneLoadsFromStore(t *testing.T) {
	isolateStore(t)
	if _, err := savedquery.Save("pending-ct", "default", "postgres", "select 1", false); err != nil {
		t.Fatal(err)
	}
	p := newSavedPane()
	if len(p.queries) != 1 || p.queries[0].Name != "pending-ct" {
		t.Fatalf("queries = %+v", p.queries)
	}
}

func TestSavedPaneEnterLoadsIntoQueryPane(t *testing.T) {
	isolateStore(t)
	if _, err := savedquery.Save("pending-ct", "default", "postgres", "select * from orders", false); err != nil {
		t.Fatal(err)
	}
	m := newTestModel()
	m.saved = newSavedPane()
	m.focus = paneSaved

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.(model).query.value(); got != "select * from orders" {
		t.Fatalf("query pane = %q, want the saved query's SQL", got)
	}
}
