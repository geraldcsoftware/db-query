package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestSavedViewRowsFillThePaneWidth keeps the cursor row's bar spanning the
// pane, and pins the category/name shape the store is browsed by.
func TestSavedViewRowsFillThePaneWidth(t *testing.T) {
	isolateStore(t)
	if _, err := savedquery.Save("pending-ct", "default", "postgres", "select 1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := savedquery.Save("daily-sum", "reports", "postgres", "select 2", false); err != nil {
		t.Fatal(err)
	}
	p := newSavedPane()
	lines := strings.Split(p.view(24), "\n")
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 24 {
			t.Errorf("row %d is %d cells wide, want 24", i, got)
		}
	}
	if !strings.Contains(lines[p.cursor], selectionSGR) {
		t.Errorf("the cursor row must carry the selection background:\n%q", lines[p.cursor])
	}
	if !strings.HasPrefix(ansi.Strip(lines[0]), " default/pending-ct") {
		t.Errorf("row = %q, want the query's category/name", ansi.Strip(lines[0]))
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
	if got := queryText(updated.(model)); got != "select * from orders" {
		t.Fatalf("query pane = %q, want the saved query's SQL", got)
	}
}
