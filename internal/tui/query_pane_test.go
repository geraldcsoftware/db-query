package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// typeInto feeds s to the editor the way a terminal reports typing it: one key
// press per character.
func typeInto(q *textareaEditor, s string) *textareaEditor {
	for _, r := range s {
		q.update(runeKey(r))
	}
	return q
}

func TestQueryPaneAcceptsInputWhenFocused(t *testing.T) {
	q := typeInto(newTextareaEditor(), "select 1")
	if q.value() != "select 1" {
		t.Fatalf("value = %q, want %q", q.value(), "select 1")
	}
}

func TestQueryPaneSetValueReplacesContent(t *testing.T) {
	q := typeInto(newTextareaEditor(), "old")
	q.setValue("select * from orders")
	if q.value() != "select * from orders" {
		t.Fatalf("value = %q, want the replaced text", q.value())
	}
}

func TestModelRoutesInputOnlyToFocusedQueryPane(t *testing.T) {
	m := newTestModel(t)
	m.focus = paneSchema // not the query pane
	updated, _ := m.Update(runeKey('x'))
	if queryText(updated.(model)) != "" {
		t.Fatal("query pane must not receive input while unfocused")
	}

	m.focus = paneQuery
	updated = m
	for _, r := range "select 1" {
		updated, _ = updated.Update(runeKey(r))
	}
	if queryText(updated.(model)) != "select 1" {
		t.Fatalf("value = %q, want select 1", queryText(updated.(model)))
	}
}

// TestPasteReachesTheFocusedQueryPane covers the one input path that is not a
// key press: a bracketed paste is its own message type, so it only lands in
// the buffer if Update routes it there deliberately.
func TestPasteReachesTheFocusedQueryPane(t *testing.T) {
	m := newTestModel(t)
	m.focus = paneSchema
	updated, _ := m.Update(tea.PasteMsg{Content: "select * from orders"})
	if got := queryText(updated.(model)); got != "" {
		t.Fatalf("query pane = %q, want nothing pasted while unfocused", got)
	}

	m.focus = paneQuery
	updated, _ = m.Update(tea.PasteMsg{Content: "select * from orders"})
	if got := queryText(updated.(model)); got != "select * from orders" {
		t.Fatalf("query pane = %q, want the pasted text", got)
	}
}
