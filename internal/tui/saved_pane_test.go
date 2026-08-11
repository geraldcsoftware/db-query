package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

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
	// newTestModel isolates the store itself, so the query is saved after it
	// is built — saving first would write to a directory the model's own
	// isolation then replaces.
	m := newTestModel(t)
	if _, err := savedquery.Save("pending-ct", "default", "postgres", "select * from orders", false); err != nil {
		t.Fatal(err)
	}
	m.saved = newSavedPane()
	m.focus = paneSaved

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(model).query.value(); got != "select * from orders" {
		t.Fatalf("query pane = %q, want the saved query's SQL", got)
	}
}
