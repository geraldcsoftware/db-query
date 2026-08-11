package tui

import (
	"strings"
	"testing"
)

func TestViewContainsAllFourPaneTitles(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	m.recomputeLayout()
	out := m.View()
	for _, want := range []string{"Schema", "Saved", "Query", "Results"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing pane title %q:\n%s", want, out)
		}
	}
}

func TestViewShowsStatusMsgOverBottomBarWhenSet(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	m.recomputeLayout()
	m.statusMsg = "query already running — Ctrl+C to cancel"
	out := m.View()
	if !strings.Contains(out, "query already running") {
		t.Fatalf("View() must show the active status message:\n%s", out)
	}
}
