package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestQueryPaneAcceptsInputWhenFocused(t *testing.T) {
	q := newQueryPane()
	q, _ = q.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("select 1")})
	if q.value() != "select 1" {
		t.Fatalf("value = %q, want %q", q.value(), "select 1")
	}
}

func TestQueryPaneSetValueReplacesContent(t *testing.T) {
	q := newQueryPane()
	q, _ = q.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("old")})
	q.setValue("select * from orders")
	if q.value() != "select * from orders" {
		t.Fatalf("value = %q, want the replaced text", q.value())
	}
}

func TestModelRoutesInputOnlyToFocusedQueryPane(t *testing.T) {
	m := newTestModel()
	m.focus = paneSchema // not the query pane
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if updated.(model).query.value() != "" {
		t.Fatal("query pane must not receive input while unfocused")
	}

	m.focus = paneQuery
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("select 1")})
	if updated.(model).query.value() != "select 1" {
		t.Fatalf("value = %q, want select 1", updated.(model).query.value())
	}
}
