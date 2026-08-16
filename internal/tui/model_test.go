package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
	m := newModel(session.Resolved{}, session.CommonFlags{}, "1.2.3", nil, newTextareaEditor())
	m.width, m.height = 100, 40 // a WindowSizeMsg would normally set these
	m.recomputeLayout()
	return m
}

// key builds the key press a terminal reports for the named keystroke, in the
// same shape Update matches on: a key code plus its modifiers, whose String()
// is the name given here.
func key(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+h":
		return tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}
	case "ctrl+j":
		return tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
	case "ctrl+k":
		return tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	case "ctrl+l":
		return tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	panic("unhandled key " + s)
}

// runeKey builds the key press a terminal reports for one printable
// character: the character itself in Text alongside its key code.
func runeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
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

func clickAt(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// TestMouseClickFocusesThePaneRenderedThere checks hit-testing against what is
// actually on screen rather than against the rectangle map alone: for each
// pane it finds that pane's label in the rendered frame, then clicks on the
// label's own cell and asserts focus lands on the pane drawn there.
func TestMouseClickFocusesThePaneRenderedThere(t *testing.T) {
	base := newTestModel(t)
	lines := strings.Split(ansi.Strip(base.View().Content), "\n")
	for _, tc := range []struct {
		p     pane
		label string
	}{
		{paneResults, "RESULTS"},
		{paneSchema, "SCHEMA"},
		{paneQuery, "QUERY"},
		{paneSaved, "SAVED"},
	} {
		r := base.rects[tc.p]
		col := titleColumn(lines[r.y0], tc.label)
		if col < 0 {
			t.Fatalf("%s is not rendered on row %d", tc.label, r.y0)
		}
		m := base
		m.focus = paneSchema
		if tc.p == paneSchema {
			m.focus = paneResults // so a no-op cannot pass as a success
		}
		updated, _ := m.Update(clickAt(col, r.y0))
		if got := updated.(model).focus; got != tc.p {
			t.Errorf("click on the rendered %s label focused %v", tc.label, got)
		}
	}
}

// TestNonPressMouseEventsDoNotChangeFocus guards the gate on mouse events:
// View enables cell-motion reporting, so wheel, motion and release events all
// arrive as their own tea.MouseMsg types and none of them may steal focus.
func TestNonPressMouseEventsDoNotChangeFocus(t *testing.T) {
	m := newTestModel(t)
	m.focus = paneQuery
	r := m.rects[paneResults]
	x, y := (r.x0+r.x1)/2, (r.y0+r.y1)/2
	for _, msg := range []tea.MouseMsg{
		tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseNone},
		tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft},
		tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown},
		tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp},
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
