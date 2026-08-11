package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/schema"
)

func seedSchemaCache(t *testing.T, host, database string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rows := adapter.Rows{Columns: []string{"table_schema", "table_name", "column_name", "data_type", "is_nullable"}}
	add := func(s, tb, c, d, n string) {
		a, b, cc, d2, e := s, tb, c, d, n
		rows.Rows = append(rows.Rows, []*string{&a, &b, &cc, &d2, &e})
	}
	add("public", "orders", "id", "int8", "NO")
	add("public", "payments", "id", "int8", "NO")
	add("public", "payments", "amount", "numeric", "NO")
	if err := schema.Write(schema.CachePath(host, database), rows); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaPaneLoadsFromCache(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	if len(p.tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(p.tables))
	}
	if p.hint != "" {
		t.Fatalf("hint = %q, want empty when cache is present", p.hint)
	}
}

func TestSchemaPaneColdCacheShowsHintNoIO(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // present but empty: no cache file written
	p := newSchemaPane(config.HostConfig{Host: "nope", Database: "nope"})
	if len(p.tables) != 0 {
		t.Fatalf("got %d tables from a cold cache, want 0", len(p.tables))
	}
	if p.hint == "" {
		t.Fatal("expected a hint pointing at introspect")
	}
}

func TestSchemaPaneEnterExpandsColumns(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	if p.expanded[p.cursor] {
		t.Fatal("must start collapsed")
	}
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.expanded[p.cursor] {
		t.Fatal("Enter must expand the selected table")
	}
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.expanded[p.cursor] {
		t.Fatal("a second Enter must collapse it again")
	}
}

// TestSchemaViewRowsFillThePaneWidth is what lets the cursor row read as a bar
// rather than as a highlight that stops at the end of the table's name.
func TestSchemaViewRowsFillThePaneWidth(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	p.expanded[1] = true
	for _, w := range []int{30, 22, 12, 4, 1} {
		for i, line := range strings.Split(p.view(w), "\n") {
			if got := ansi.StringWidth(line); got != w {
				t.Errorf("width %d: row %d is %d cells wide", w, i, got)
			}
		}
	}
}

// TestSchemaViewMarksTheCursorRow pins the sidebar's strongest cue: the row
// under the cursor is a mint bar, and nothing else is.
func TestSchemaViewMarksTheCursorRow(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	lines := strings.Split(p.view(30), "\n")
	if !strings.Contains(lines[0], selectionSGR) {
		t.Errorf("the cursor row must carry the selection background:\n%q", lines[0])
	}
	if strings.Contains(lines[1], selectionSGR) {
		t.Errorf("no other row may:\n%q", lines[1])
	}
}

// TestSchemaViewShowsMarkersCountsAndColumns covers the row's three parts: the
// disclosure marker, the column count against the pane's right edge, and an
// expanded table's columns with their data types.
func TestSchemaViewShowsMarkersCountsAndColumns(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	collapsed := strings.Split(ansi.Strip(p.view(30)), "\n")
	if want := " " + markerCollapsed + "orders"; !strings.HasPrefix(collapsed[0], want) {
		t.Errorf("row = %q, want it to start %q", collapsed[0], want)
	}
	if !strings.HasSuffix(collapsed[0], "1 ") { // orders has one column in the seed
		t.Errorf("row = %q, want the column count against the right edge", collapsed[0])
	}
	if len(collapsed) != 2 {
		t.Fatalf("a collapsed catalogue must be one row per table, got %d", len(collapsed))
	}

	p.expanded[1] = true // payments: id int8, amount numeric
	expanded := strings.Split(ansi.Strip(p.view(30)), "\n")
	if len(expanded) != 4 {
		t.Fatalf("an expanded table must list its columns, got %d rows", len(expanded))
	}
	if want := " " + markerExpanded + "payments"; !strings.HasPrefix(expanded[1], want) {
		t.Errorf("row = %q, want it to start %q", expanded[1], want)
	}
	if !strings.HasPrefix(expanded[2], " "+markerColumn+"id") || !strings.HasSuffix(expanded[2], "int8 ") {
		t.Errorf("column row = %q, want the name under the table's and its type on the right", expanded[2])
	}
}

func TestSchemaPaneSelectedTable(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyDown})
	tbl, ok := p.selectedTable()
	if !ok || tbl.Name != "payments" {
		t.Fatalf("selectedTable = %+v, %v, want payments", tbl, ok)
	}
}
