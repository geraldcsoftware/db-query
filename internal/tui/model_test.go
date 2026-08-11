package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// newTestModel builds a model against empty, per-test schema-cache and
// saved-query directories. newModel reads both stores in its constructor, so
// without this isolation every test built on it would read whatever happens
// to exist in the developer's real home directory.
func newTestModel(t *testing.T) model {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DB_QUERY_QUERIES_DIR", t.TempDir())
	m := newModel(session.Resolved{}, session.CommonFlags{}, "1.2.3", nil)
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
	m := newTestModel(t)
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
		m := newTestModel(t)
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
	m := newTestModel(t)
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
	m := newTestModel(t) // running is false by default
	_, cmd := m.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func clickAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

// TestMouseClickFocusesThePaneRenderedThere checks hit-testing against what is
// actually on screen rather than against the rectangle map alone: for each
// pane it finds that pane's title in the rendered frame, then clicks on the
// title's own cell and asserts focus lands on the pane drawn there.
func TestMouseClickFocusesThePaneRenderedThere(t *testing.T) {
	base := newTestModel(t)
	lines := strings.Split(ansi.Strip(base.View()), "\n")
	for _, tc := range []struct {
		p     pane
		title string
	}{
		{paneResults, "[Results]"},
		{paneSchema, "[Schema]"},
		{paneQuery, "[Query]"},
		{paneSaved, "[Saved]"},
	} {
		r := base.rects[tc.p]
		col := titleColumn(lines[r.y0], tc.title)
		if col < 0 {
			t.Fatalf("%s is not rendered on row %d", tc.title, r.y0)
		}
		m := base
		m.focus = paneSchema
		if tc.p == paneSchema {
			m.focus = paneResults // so a no-op cannot pass as a success
		}
		updated, _ := m.Update(clickAt(col, r.y0))
		if got := updated.(model).focus; got != tc.p {
			t.Errorf("click on the rendered %s title focused %v", tc.title, got)
		}
	}
}

// TestNonPressMouseEventsDoNotChangeFocus guards the gate on mouse actions:
// tui.Run enables cell-motion reporting, so wheel, motion and release events
// all arrive as tea.MouseMsg and none of them may steal focus.
func TestNonPressMouseEventsDoNotChangeFocus(t *testing.T) {
	m := newTestModel(t)
	m.focus = paneQuery
	r := m.rects[paneResults]
	x, y := (r.x0+r.x1)/2, (r.y0+r.y1)/2
	for _, msg := range []tea.MouseMsg{
		{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone},
		{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft},
		{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown},
		{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp},
	} {
		updated, _ := m.Update(msg)
		if got := updated.(model).focus; got != paneQuery {
			t.Errorf("%s changed focus to %v", msg, got)
		}
	}
}

func TestEscCancelsAnInFlightRunBeforeQuitting(t *testing.T) {
	m := newTestModel(t)
	m.running = true
	canceled := false
	m.cancel = func() { canceled = true }

	_, cmd := m.Update(key("esc"))
	if !canceled {
		t.Fatal("Esc must cancel the in-flight run so the client subprocess is not orphaned")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestIdleCtrlCCancelsBeforeQuitting(t *testing.T) {
	m := newTestModel(t) // running is false, but a stale CancelFunc must still be released
	canceled := false
	m.cancel = func() { canceled = true }

	_, cmd := m.Update(key("ctrl+c"))
	if !canceled {
		t.Fatal("quitting must release the held CancelFunc")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}
