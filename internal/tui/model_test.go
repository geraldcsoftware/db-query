package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/session"
)

func newTestModel() model {
	m := newModel(session.Resolved{}, session.CommonFlags{}, nil)
	m.width, m.height = 100, 40 // a WindowSizeMsg would normally set these
	m.recomputeLayout()
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "ctrl+h":
		return tea.KeyMsg{Type: tea.KeyCtrlH}
	case "ctrl+j":
		return tea.KeyMsg{Type: tea.KeyCtrlJ}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+l":
		return tea.KeyMsg{Type: tea.KeyCtrlL}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	panic("unhandled key " + s)
}

func TestFocusGridNavigation(t *testing.T) {
	m := newTestModel()
	if m.focus != paneSchema {
		t.Fatalf("initial focus = %v, want paneSchema", m.focus)
	}
	cases := []struct {
		keys []string
		want pane
	}{
		{[]string{"ctrl+l"}, paneQuery},
		{[]string{"ctrl+l", "ctrl+j"}, paneResults},
		{[]string{"ctrl+l", "ctrl+j", "ctrl+h"}, paneSaved},
		{[]string{"ctrl+l", "ctrl+j", "ctrl+h", "ctrl+k"}, paneSchema},
		{[]string{"ctrl+h"}, paneSchema}, // already leftmost: clamped, not wrapped
		{[]string{"ctrl+k"}, paneSchema}, // already topmost: clamped, not wrapped
	}
	for _, tc := range cases {
		m := newTestModel()
		var updated tea.Model = m
		for _, k := range tc.keys {
			updated, _ = updated.Update(key(k))
		}
		got := updated.(model).focus
		if got != tc.want {
			t.Errorf("keys %v -> focus %v, want %v", tc.keys, got, tc.want)
		}
	}
}

func TestEscQuitsFromAnyFocus(t *testing.T) {
	m := newTestModel()
	m.focus = paneResults
	_, cmd := m.Update(key("esc"))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestCtrlCQuitsWhenIdle(t *testing.T) {
	m := newTestModel() // running is false by default
	_, cmd := m.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestMouseClickSetsFocus(t *testing.T) {
	m := newTestModel()
	// recomputeLayout splits width/height in half: Schema/Saved on the left,
	// Query/Results on the right; Schema/Query on top, Saved/Results below.
	m.setFocusAt(m.width-1, m.height-1) // bottom-right corner: Results
	if m.focus != paneResults {
		t.Fatalf("focus after bottom-right click = %v, want paneResults", m.focus)
	}
	m.setFocusAt(0, 0) // top-left corner: Schema
	if m.focus != paneSchema {
		t.Fatalf("focus after top-left click = %v, want paneSchema", m.focus)
	}
}
