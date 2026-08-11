package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestViewContainsAllFourPaneTitles(t *testing.T) {
	m := newTestModel(t)
	out := m.View()
	for _, want := range []string{"Schema", "Saved", "Query", "Results"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing pane title %q:\n%s", want, out)
		}
	}
}

func TestViewShowsStatusMsgOverBottomBarWhenSet(t *testing.T) {
	m := newTestModel(t)
	m.statusMsg = "query already running — Ctrl+C to cancel"
	out := m.View()
	if !strings.Contains(out, "query already running") {
		t.Fatalf("View() must show the active status message:\n%s", out)
	}
}

func TestViewShowsTopBarWithVersionAndConnection(t *testing.T) {
	m := newTestModel(t)
	m.session.Host.Host = "db.example"
	m.session.Host.Database = "orders"
	m.session.Host.Provider = "postgres"
	first := strings.Split(ansi.Strip(m.View()), "\n")[0]
	for _, want := range []string{"db-query", "1.2.3", "db.example", "orders", "postgres"} {
		if !strings.Contains(first, want) {
			t.Errorf("top bar %q missing %q", first, want)
		}
	}
	if !strings.HasSuffix(first, "(postgres)") {
		t.Errorf("connection must be right-aligned in the top bar, got %q", first)
	}
}

// TestViewNeverExceedsTerminalHeight is the guard on the reason View lays out
// against rectangles at all: Bubble Tea's renderer keeps only the last height
// lines of an over-tall frame, so an unbounded View pushes the top bar and the
// upper panes off screen the moment a result is larger than the terminal.
func TestViewNeverExceedsTerminalHeight(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "") // the 100-row default: one page renders ~105 lines
	m := newTestModel(t)
	m.results.showRows(rowsOf(432))
	for _, size := range []struct{ w, h int }{{100, 40}, {80, 24}, {120, 60}, {60, 10}, {40, 4}, {40, 2}, {40, 1}} {
		m.width, m.height = size.w, size.h
		m.recomputeLayout()
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d: View() rendered %d lines, want at most %d", size.w, size.h, len(lines), size.h)
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size.w {
				t.Errorf("%dx%d: line %d is %d cells wide, want at most %d", size.w, size.h, i, w, size.w)
			}
		}
	}
}

// TestViewKeepsEveryPaneVisibleWithALargeResult asserts the browse -> write ->
// run -> view loop survives a result page taller than the terminal: the query
// the user just ran is still on screen alongside the result.
func TestViewKeepsEveryPaneVisibleWithALargeResult(t *testing.T) {
	m := newTestModel(t)
	m.query.setValue("select * from orders")
	m.results.showRows(rowsOf(432))
	out := ansi.Strip(m.View())
	for _, want := range []string{"db-query", "[Schema]", "[Query]", "[Saved]", "[Results]", "select * from orders"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() lost %q behind a large result:\n%s", want, out)
		}
	}
}

// TestViewPlacesEachPaneInItsOwnRect ties rendering to the geometry mouse
// hit-testing uses: each pane's title must be drawn on its rectangle's first
// row, within its rectangle's columns.
func TestViewPlacesEachPaneInItsOwnRect(t *testing.T) {
	m := newTestModel(t)
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) != m.height {
		t.Fatalf("View() rendered %d lines, want exactly %d", len(lines), m.height)
	}
	for _, tc := range []struct {
		p     pane
		title string
	}{
		{paneSchema, "[Schema]"},
		{paneQuery, "[Query]"},
		{paneSaved, "[Saved]"},
		{paneResults, "[Results]"},
	} {
		r := m.rects[tc.p]
		col := strings.Index(lines[r.y0], tc.title)
		if col < r.x0 || col >= r.x1 {
			t.Errorf("%s is drawn at column %d of row %d, outside its rect %+v", tc.title, col, r.y0, r)
		}
	}
}

func TestViewShowsRunningIndicator(t *testing.T) {
	m := newTestModel(t)
	idle := ansi.Strip(m.View())
	m.running = true
	busy := ansi.Strip(m.View())
	if idle == busy {
		t.Fatal("a running query must look different from an idle screen")
	}
	if !strings.Contains(busy, "running") {
		t.Errorf("View() must indicate a run is in flight:\n%s", busy)
	}
	if strings.Contains(idle, "running") {
		t.Errorf("an idle screen must not claim a run is in flight:\n%s", idle)
	}
}

// TestViewBlursTheQueryPaneWhenAnotherPaneHasFocus pins the visual half of
// focus: only the focused pane shows a cursor, so the frame differs between
// the Query pane holding focus and not.
func TestViewBlursTheQueryPaneWhenAnotherPaneHasFocus(t *testing.T) {
	m := newTestModel(t)
	m.query.setValue("select 1")

	m.focus = paneQuery
	focused := m.View()
	m.focus = paneSchema
	blurred := m.View()

	if !strings.Contains(ansi.Strip(blurred), "select 1") {
		t.Fatalf("the query text must stay visible while another pane has focus:\n%s", blurred)
	}
	if focused == blurred {
		t.Fatal("the Query pane must render its cursor only while it holds focus")
	}
}
