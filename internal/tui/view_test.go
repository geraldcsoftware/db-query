package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// titleColumn returns the display column sub starts at in line, or -1 if it is
// absent. The column is the width of everything before the match, not its byte
// offset: pane frames are drawn with multi-byte box-drawing runes, for which
// the two differ.
func titleColumn(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(line[:i])
}

func TestViewContainsAllFourPaneTitles(t *testing.T) {
	m := newTestModel(t)
	out := m.View().Content
	for _, want := range []string{"Schema", "Saved", "Query", "Results"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing pane title %q:\n%s", want, out)
		}
	}
}

func TestViewShowsStatusMsgOverBottomBarWhenSet(t *testing.T) {
	m := newTestModel(t)
	m.statusMsg = "query already running — Ctrl+C to cancel"
	out := m.View().Content
	if !strings.Contains(out, "query already running") {
		t.Fatalf("View() must show the active status message:\n%s", out)
	}
}

func TestViewShowsTopBarWithVersionAndConnection(t *testing.T) {
	m := newTestModel(t)
	m.session.Host.Host = "db.example"
	m.session.Host.Database = "orders"
	m.session.Host.Provider = "postgres"
	first := strings.Split(ansi.Strip(m.View().Content), "\n")[0]
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
		lines := strings.Split(m.View().Content, "\n")
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
	out := ansi.Strip(m.View().Content)
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
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
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
		col := titleColumn(lines[r.y0], tc.title)
		if col < r.x0 || col >= r.x1 {
			t.Errorf("%s is drawn at column %d of row %d, outside its rect %+v", tc.title, col, r.y0, r)
		}
	}
}

// TestFocusedPaneIsVisuallyDistinct pins the affordance that tells a user
// which pane their next keystroke reaches. It asserts a difference rather than
// a specific colour: colour is downsampled on the way to the terminal and
// disappears altogether on one that cannot show any, so the frame's weight,
// not its colour, is what must carry focus for this to hold everywhere.
func TestFocusedPaneIsVisuallyDistinct(t *testing.T) {
	m := newTestModel(t)
	r := m.rects[paneSaved]
	m.focus = paneSaved
	focused := m.paneBlock(paneSaved, "Saved", m.saved.view(), r)
	m.focus = paneSchema
	unfocused := m.paneBlock(paneSaved, "Saved", m.saved.view(), r)

	if strings.Join(focused, "\n") == strings.Join(unfocused, "\n") {
		t.Fatal("a focused pane must render differently from the same pane unfocused")
	}
	if ansi.Strip(strings.Join(focused, "\n")) == ansi.Strip(strings.Join(unfocused, "\n")) {
		t.Error("focus must survive a colourless terminal, so it cannot be signalled by colour alone")
	}
}

// TestPaneBlockExactlyFillsItsRect is the per-pane half of the whole-screen
// bound TestViewNeverExceedsTerminalHeight enforces: a framed pane must still
// occupy exactly its rectangle, since View tiles the rectangles edge to edge
// and any drift would push a later pane off screen. The sizes run down to
// rectangles too small to frame at all.
func TestPaneBlockExactlyFillsItsRect(t *testing.T) {
	m := newTestModel(t)
	m.results.showRows(rowsOf(50)) // content far taller and wider than the small rects
	for _, size := range []struct{ w, h int }{{50, 19}, {20, 5}, {10, 3}, {4, 3}, {3, 3}, {6, 2}, {6, 1}, {1, 1}} {
		r := rect{0, 0, size.w, size.h}
		for _, focused := range []bool{true, false} {
			m.focus = paneSchema
			if focused {
				m.focus = paneResults
			}
			got := m.paneBlock(paneResults, "Results", m.results.view(), r)
			if len(got) != size.h {
				t.Errorf("%dx%d focused=%v: %d lines, want %d", size.w, size.h, focused, len(got), size.h)
				continue
			}
			for i, l := range got {
				if w := ansi.StringWidth(l); w != size.w {
					t.Errorf("%dx%d focused=%v: line %d is %d cells, want %d", size.w, size.h, focused, i, w, size.w)
				}
			}
		}
	}
}

func TestViewShowsRunningIndicator(t *testing.T) {
	m := newTestModel(t)
	idle := ansi.Strip(m.View().Content)
	m.running = true
	busy := ansi.Strip(m.View().Content)
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
	focused := m.View().Content
	m.focus = paneSchema
	blurred := m.View().Content

	if !strings.Contains(ansi.Strip(blurred), "select 1") {
		t.Fatalf("the query text must stay visible while another pane has focus:\n%s", blurred)
	}
	if focused == blurred {
		t.Fatal("the Query pane must render its cursor only while it holds focus")
	}
}
