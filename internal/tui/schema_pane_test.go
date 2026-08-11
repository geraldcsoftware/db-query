package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	p, _ = p.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !p.expanded[p.cursor] {
		t.Fatal("Enter must expand the selected table")
	}
	p, _ = p.update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.expanded[p.cursor] {
		t.Fatal("a second Enter must collapse it again")
	}
}

func TestSchemaPaneSelectedTable(t *testing.T) {
	seedSchemaCache(t, "lionel", "reporting")
	p := newSchemaPane(config.HostConfig{Host: "lionel", Database: "reporting"})
	p, _ = p.update(tea.KeyMsg{Type: tea.KeyDown})
	tbl, ok := p.selectedTable()
	if !ok || tbl.Name != "payments" {
		t.Fatalf("selectedTable = %+v, %v, want payments", tbl, ok)
	}
}
