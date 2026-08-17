package tui

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// visibleGutter reads back the row numbers the pane currently draws, taken from
// the gutter column of every line below the header. It is the cheapest way to
// say which slice of a page is on screen without asserting on the table's
// spacing.
func visibleGutter(t *testing.T, p resultsPane) []int {
	t.Helper()
	lines := strings.Split(ansi.Strip(p.view()), "\n")
	out := make([]int, 0, len(lines))
	for _, l := range lines[1:] { // line 0 is the header, which never scrolls
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("row %q does not lead with a gutter number", l)
		}
		out = append(out, n)
	}
	return out
}

// TestResultsPaneScrollsRowsIntoView is the property the change exists for: a
// page taller than the pane must be reachable, not cut off at whichever row the
// pane happens to end on.
func TestResultsPaneScrollsRowsIntoView(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.setSize(40, 6) // a header row plus five data rows

	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("unscrolled pane shows rows %v, want 1-5", got)
	}
	p.scrollDown()
	p.scrollDown()
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{3, 4, 5, 6, 7}) {
		t.Fatalf("after two scrolls the pane shows rows %v, want 3-7", got)
	}
}

// TestResultsPaneScrollStopsAtLastRow keeps the last row against the pane's
// bottom edge rather than letting the view run off the end of the page into
// blank rows.
func TestResultsPaneScrollStopsAtLastRow(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.setSize(40, 6)

	for i := 0; i < 50; i++ {
		p.scrollDown()
	}
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{21, 22, 23, 24, 25}) {
		t.Fatalf("scrolled past the end shows rows %v, want the last five (21-25)", got)
	}
}

// TestResultsPaneScrollStopsAtFirstRow is the same guard at the other end.
func TestResultsPaneScrollStopsAtFirstRow(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.setSize(40, 6)

	p.scrollDown()
	for i := 0; i < 50; i++ {
		p.scrollUp()
	}
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("scrolled past the start shows rows %v, want 1-5", got)
	}
}

// TestResultsPaneShortPageDoesNotScroll guards the other direction: a page that
// already fits must stay put, so a stray keystroke cannot slide the first row
// off the top of a pane with room to spare.
func TestResultsPaneShortPageDoesNotScroll(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(3))
	p.setSize(40, 10)

	p.scrollDown()
	p.scrollDown()
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("a page that fits shows rows %v, want all three from the top", got)
	}
}

// TestResultsPaneHeaderStaysPinnedWhileScrolling pins the header against the
// rows moving underneath it. Without this the column names scroll away and the
// values below become unreadable.
func TestResultsPaneHeaderStaysPinnedWhileScrolling(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.setSize(40, 6)

	for i := 0; i < 10; i++ {
		p.scrollDown()
	}
	header := strings.Split(ansi.Strip(p.view()), "\n")[0]
	if !strings.Contains(header, "id") {
		t.Fatalf("header row is %q, want it still naming the id column", header)
	}
	if !strings.Contains(header, "#") {
		t.Fatalf("header row is %q, want it still carrying the gutter heading", header)
	}
}

// TestResultsPaneScrollTopAndBottom covers g and G: the whole page in one
// keystroke each, rather than holding a row key down.
func TestResultsPaneScrollTopAndBottom(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.setSize(40, 6)

	p.scrollBottom()
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{21, 22, 23, 24, 25}) {
		t.Fatalf("scrollBottom shows rows %v, want the last five", got)
	}
	p.scrollTop()
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("scrollTop shows rows %v, want the first five", got)
	}
}

// wideRows builds a result of cols columns named c0, c1, ... over n rows, every
// cell holding its own column name, so a rendered row says exactly which
// columns are on screen.
func wideRows(cols, n int) adapter.Rows {
	out := adapter.Rows{}
	for i := 0; i < cols; i++ {
		out.Columns = append(out.Columns, "c"+strconv.Itoa(i))
	}
	for i := 0; i < n; i++ {
		row := make([]*string, cols)
		for c := range row {
			v := out.Columns[c]
			row[c] = &v
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// visibleColumns reads back which columns the pane currently draws, from the
// header row.
func visibleColumns(p resultsPane) []string {
	header := strings.Split(ansi.Strip(p.view()), "\n")[0]
	out := []string{}
	for _, f := range strings.Fields(header) {
		if strings.HasPrefix(f, "c") {
			out = append(out, f)
		}
	}
	return out
}

// TestResultsPaneScrollsColumnsIntoView is the horizontal half of the same
// property: a result wider than the pane must be reachable rather than clipped
// at the pane's right edge forever.
func TestResultsPaneScrollsColumnsIntoView(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)

	first := visibleColumns(p)
	if len(first) == 0 || first[0] != "c0" {
		t.Fatalf("unscrolled pane shows %v, want it to start at c0", first)
	}
	if len(first) >= 12 {
		t.Fatalf("a 20-cell pane cannot be showing all 12 columns: %v", first)
	}
	p.scrollRight()
	p.scrollRight()
	if got := visibleColumns(p); len(got) == 0 || got[0] != "c2" {
		t.Fatalf("after two column scrolls the pane shows %v, want it to start at c2", got)
	}
}

// TestResultsPaneGutterStaysPinnedWhileScrollingColumns keeps the row numbers
// on screen no matter how far right the view goes. They are the only thing
// saying which row a value belongs to.
func TestResultsPaneGutterStaysPinnedWhileScrollingColumns(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)

	for i := 0; i < 5; i++ {
		p.scrollRight()
	}
	if got := visibleGutter(t, p); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("scrolled right, the gutter reads %v, want row 1 still numbered", got)
	}
}

// TestResultsPaneColumnScrollStopsAtLastColumn keeps the rightmost column
// against the pane's edge rather than scrolling into empty space.
func TestResultsPaneColumnScrollStopsAtLastColumn(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)

	for i := 0; i < 50; i++ {
		p.scrollRight()
	}
	got := visibleColumns(p)
	if len(got) == 0 || got[len(got)-1] != "c11" {
		t.Fatalf("scrolled past the end shows %v, want it to end at c11", got)
	}
	before := strings.Join(got, ",")
	p.scrollRight()
	if after := strings.Join(visibleColumns(p), ","); after != before {
		t.Fatalf("scrolling past the last column moved the view from %q to %q", before, after)
	}
}

// TestResultsPaneColumnScrollStopsAtFirstColumn is the same guard on the left.
func TestResultsPaneColumnScrollStopsAtFirstColumn(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)

	p.scrollRight()
	for i := 0; i < 50; i++ {
		p.scrollLeft()
	}
	if got := visibleColumns(p); len(got) == 0 || got[0] != "c0" {
		t.Fatalf("scrolled past the start shows %v, want it back at c0", got)
	}
}

// TestResultsPaneNarrowResultDoesNotScrollColumns guards a result that already
// fits: every column is on screen, so there is nothing to scroll to.
func TestResultsPaneNarrowResultDoesNotScrollColumns(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(2, 1))
	p.setSize(60, 6)

	p.scrollRight()
	p.scrollRight()
	if got := visibleColumns(p); !reflect.DeepEqual(got, []string{"c0", "c1"}) {
		t.Fatalf("a result that fits shows %v, want both columns from the left", got)
	}
}

// TestResultsPaneFirstAndLastColumn covers Home and End.
func TestResultsPaneFirstAndLastColumn(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)

	p.scrollLastColumn()
	got := visibleColumns(p)
	if len(got) == 0 || got[len(got)-1] != "c11" {
		t.Fatalf("scrollLastColumn shows %v, want it to end at c11", got)
	}
	p.scrollFirstColumn()
	if got := visibleColumns(p); len(got) == 0 || got[0] != "c0" {
		t.Fatalf("scrollFirstColumn shows %v, want it back at c0", got)
	}
}

// TestResultsPaneMarksHiddenColumns is what tells a user there is more table
// off the edge of the pane. Without a marker, a clipped result is
// indistinguishable from a complete one.
func TestResultsPaneMarksHiddenColumns(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)

	out := ansi.Strip(p.view())
	if !strings.Contains(out, moreRight) {
		t.Fatalf("columns hidden to the right must be marked with %q:\n%s", moreRight, out)
	}
	if strings.Contains(out, moreLeft) {
		t.Fatalf("nothing is hidden to the left at column 0, so %q must not show:\n%s", moreLeft, out)
	}
	p.scrollRight()
	if out := ansi.Strip(p.view()); !strings.Contains(out, moreLeft) {
		t.Fatalf("scrolled right, columns hidden to the left must be marked with %q:\n%s", moreLeft, out)
	}
}

// TestResultsPagingResetsRowsButKeepsColumns is the ergonomic rule between the
// two notions of position: a new page is a new set of rows, so the view returns
// to the top of it, but it is the same set of columns, so the horizontal
// position is kept rather than throwing the user back to the left edge.
func TestResultsPagingResetsRowsButKeepsColumns(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "10")
	var p resultsPane
	p.showRows(wideRows(12, 25))
	p.setSize(20, 4)

	p.scrollDown()
	p.scrollDown()
	for i := 0; i < 3; i++ {
		p.scrollRight()
	}
	p.pageDown()

	if p.top != 0 {
		t.Errorf("top = %d after a page change, want the new page shown from its first row", p.top)
	}
	if p.left != 3 {
		t.Errorf("left = %d after a page change, want the column position kept", p.left)
	}
}

// TestResultsNewRunResetsBothOffsets covers the other direction: a fresh result
// has neither the same rows nor the same columns, so the view starts over.
func TestResultsNewRunResetsBothOffsets(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(wideRows(12, 25))
	p.setSize(20, 6)
	p.scrollDown()
	p.scrollRight()
	p.scrollRight()

	p.showRows(wideRows(12, 25))
	if p.top != 0 || p.left != 0 {
		t.Fatalf("top/left = %d/%d after a new run, want 0/0", p.top, p.left)
	}
}

// TestResultsResizeReclampsOffsets keeps a scrolled pane honest when the
// terminal grows: rows that were off the bottom become visible, so an offset
// that was legal at the old height would strand the view past the end.
func TestResultsResizeReclampsOffsets(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.setSize(40, 6)
	p.scrollBottom() // top = 20, the last five rows

	p.setSize(40, 26) // now the whole page fits
	if p.top != 0 {
		t.Fatalf("top = %d after the pane grew to fit the page, want 0", p.top)
	}
	if got := visibleGutter(t, p); len(got) != 25 {
		t.Fatalf("a pane tall enough for the page shows %d rows, want all 25", len(got))
	}
}

// TestResultsMetaReportsScrollPosition puts the visible slice on the label row.
// With paging kept, the page indicator alone says rows 1-100 while the pane
// shows thirteen of them, which reads as a lie without this.
func TestResultsMetaReportsScrollPosition(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(250))
	p.setSize(40, 6)
	p.scrollDown()
	p.scrollDown()

	if got := p.meta(); !strings.Contains(got, "showing 3-7") {
		t.Fatalf("meta = %q, want it to report the visible rows as 'showing 3-7'", got)
	}
}

// TestResultsMetaReportsColumnSpanOnlyWhenClipped keeps the label row quiet
// when every column is on screen: a span that always reads "cols 1-2/2" is
// noise on the one row the pane can least afford to waste.
func TestResultsMetaReportsColumnSpanOnlyWhenClipped(t *testing.T) {
	var p resultsPane
	p.showRows(wideRows(12, 1))
	p.setSize(20, 6)
	if got := p.meta(); !strings.Contains(got, "cols") {
		t.Fatalf("meta = %q, want a column span while columns are hidden", got)
	}

	p.showRows(wideRows(2, 1))
	p.setSize(60, 6)
	if got := p.meta(); strings.Contains(got, "cols") {
		t.Fatalf("meta = %q, want no column span when every column fits", got)
	}
}

// TestResultsUnsizedPaneRendersEverything pins the behaviour the rest of the
// package relies on: a pane that has not been given a size draws its whole page
// rather than nothing, so a frame rendered before the first layout is not blank.
func TestResultsUnsizedPaneRendersEverything(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "10")
	var p resultsPane
	p.showRows(rowsOf(25))
	if got := visibleGutter(t, p); len(got) != 10 {
		t.Fatalf("an unsized pane drew %d rows, want the whole page of 10", len(got))
	}
}

// TestScrollKeysReachTheResultsPaneOnlyWhenFocused is the routing half of the
// change. The arrows mean something different in every pane, so they can only
// belong to the one the next keystroke is aimed at.
func TestScrollKeysReachTheResultsPaneOnlyWhenFocused(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	m, ed := modalModel(t)
	m.results.showRows(rowsOf(432))

	m.focus = paneResults
	next, _ := pressKey(m, "down")
	if next.results.top != 1 {
		t.Errorf("top = %d after Down on the focused Results pane, want 1", next.results.top)
	}

	next.focus = paneQuery
	after, _ := pressKey(next, "down")
	if after.results.top != 1 {
		t.Errorf("top = %d after Down in the editor, want the Results pane left alone", after.results.top)
	}
	if !ed.forwarded("down") {
		t.Error("Down did not reach the editor")
	}
}

// TestVimScrollKeysMoveTheResultsViewport covers the second binding on each
// direction, which is what a user with vim reflexes reaches for first.
func TestVimScrollKeysMoveTheResultsViewport(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	m, _ := modalModel(t)
	// Wide enough to overflow the pane the layout gives it, or there would be
	// nowhere for the horizontal keys to scroll to and they would pass for
	// working while doing nothing.
	m.results.showRows(wideRows(40, 432))
	m.focus = paneResults
	if m.results.table().maxLeft(m.results.w) == 0 {
		t.Fatalf("a %d-cell pane already fits every column, so this proves nothing", m.results.w)
	}

	next, _ := pressKey(m, "j")
	if next.results.top != 1 {
		t.Errorf("top = %d after j, want 1", next.results.top)
	}
	next, _ = pressKey(next, "l")
	if next.results.left != 1 {
		t.Errorf("left = %d after l, want 1", next.results.left)
	}
	next, _ = pressKey(next, "k")
	if next.results.top != 0 {
		t.Errorf("top = %d after k, want it back to 0", next.results.top)
	}
	next, _ = pressKey(next, "h")
	if next.results.left != 0 {
		t.Errorf("left = %d after h, want it back to 0", next.results.left)
	}
}

// TestFocusKeysStillMoveFocusFromTheResultsPane guards the chords against the
// bare keys that now sit beside them: Ctrl+K must leave the pane rather than
// scroll it.
func TestFocusKeysStillMoveFocusFromTheResultsPane(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	m, _ := modalModel(t)
	m.results.showRows(rowsOf(432))
	m.focus = paneResults
	m.results.scrollDown()

	next, _ := pressKey(m, "ctrl+k")
	if next.focus != paneQuery {
		t.Errorf("focus = %v after Ctrl+K, want the Query pane", next.focus)
	}
	if next.results.top != 1 {
		t.Errorf("top = %d after Ctrl+K, want the scroll position untouched", next.results.top)
	}
}

// TestTheLayoutSizesTheResultsPane is what connects the pane's windowing to the
// screen. Without it the pane never learns how tall it is, keeps drawing its
// whole page, and paneBlock goes on cutting off whatever does not fit.
func TestTheLayoutSizesTheResultsPane(t *testing.T) {
	m := newTestModel(t)
	r := m.rects[paneResults]
	if got, want := m.results.h, contentRows(r); got != want {
		t.Errorf("results pane height = %d, want the layout's %d content rows", got, want)
	}
	if got, want := m.results.w, r.x1-r.x0; got != want {
		t.Errorf("results pane width = %d, want the layout's %d cells", got, want)
	}
	if m.results.h <= 0 || m.results.w <= 0 {
		t.Fatalf("the layout gave the pane %dx%d, which windows nothing", m.results.w, m.results.h)
	}
}

// TestResizeReachesTheResultsPane keeps the pane's own geometry in step with
// the terminal's, so a window that shrinks re-windows rather than overflowing.
func TestResizeReachesTheResultsPane(t *testing.T) {
	m := newTestModel(t)
	before := m.results.h
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	if got := next.(model).results.h; got == before || got <= 0 {
		t.Errorf("results pane height = %d after a resize, want it re-derived from the new layout (was %d)", got, before)
	}
}

// TestTheHintBarAdvertisesScrollingOnTheResultsPane: the bar is the only place
// a user is told the keys exist, and scrolling nobody can discover is scrolling
// nobody uses.
func TestTheHintBarAdvertisesScrollingOnTheResultsPane(t *testing.T) {
	m := newTestModel(t)
	const w = 140

	m.focus = paneResults
	bar := ansi.Strip(m.bottomBar(w))
	if !strings.Contains(bar, "scroll") {
		t.Errorf("the Results pane's bar does not advertise scrolling:\n%s", bar)
	}
	if !strings.Contains(bar, "Esc") {
		t.Errorf("the Results pane's bar dropped the way out:\n%s", bar)
	}

	m.focus = paneSchema
	if bar := ansi.Strip(m.bottomBar(w)); strings.Contains(bar, "scroll") {
		t.Errorf("the Schema pane's bar advertises a key it does not have:\n%s", bar)
	}
}

// TestPaneLabelRowDropsMetaClausesRatherThanAllOfThem: the label row drops its
// summary whole when it will not fit, which on a scrolled Results pane means
// losing the row count and the page along with the scroll position that made it
// too long. Clauses come off the end, least important first, so what remains is
// what a user most needs.
func TestPaneLabelRowDropsMetaClausesRatherThanAllOfThem(t *testing.T) {
	meta := []string{"250 rows", "page 1/3", "showing 2-10", "cols 6-10/10"}
	const w = 40

	row := ansi.Strip(paneLabelRow("RESULTS", meta, w, true))
	if !strings.Contains(row, "250 rows") {
		t.Errorf("the label row dropped the row count to fit:\n%q", row)
	}
	if strings.Contains(row, "cols 6-10/10") {
		t.Errorf("the label row kept a clause it has no room for:\n%q", row)
	}
	if got := ansi.StringWidth(row); got != w {
		t.Errorf("label row is %d cells wide, want exactly %d", got, w)
	}
}

// TestPaneLabelRowKeepsEveryClauseThatFits is the other half: nothing is
// dropped from a pane with room for all of it.
func TestPaneLabelRowKeepsEveryClauseThatFits(t *testing.T) {
	meta := []string{"250 rows", "page 1/3", "showing 2-10"}
	row := ansi.Strip(paneLabelRow("RESULTS", meta, 80, true))
	for _, want := range meta {
		if !strings.Contains(row, want) {
			t.Errorf("the label row dropped %q despite having room:\n%q", want, row)
		}
	}
}

// TestResultsMetaDropsThePageRowRangeWhileScrolling removes the redundancy
// between the two positions. "page 1/3 (rows 1-100)" and "showing 2-10"
// disagree about which rows are on screen, and the second is the true one, so
// the parenthetical goes rather than being explained away.
func TestResultsMetaDropsThePageRowRangeWhileScrolling(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "100")
	var p resultsPane
	p.showRows(rowsOf(250))

	p.setSize(40, 200) // the whole page fits, so nothing scrolls
	if got := p.meta(); !strings.Contains(got, "(rows 1-100)") {
		t.Errorf("meta = %q, want the page's row range while the page fits", got)
	}

	p.setSize(40, 10) // now it does not
	got := p.meta()
	if strings.Contains(got, "(rows 1-100)") {
		t.Errorf("meta = %q, want the page's row range gone once 'showing' contradicts it", got)
	}
	if !strings.Contains(got, "page 1/3") || !strings.Contains(got, "showing 1-9") {
		t.Errorf("meta = %q, want the page still named and the visible rows given", got)
	}
}

// TestTheResultsHintBarLeadsWithTheKeysNobodyGuesses orders the bar by what a
// user would not find on their own. A narrow terminal drops hints from the
// right, and g/G and Home/End are the two nobody tries unprompted, so they must
// not be the first to go.
func TestTheResultsHintBarLeadsWithTheKeysNobodyGuesses(t *testing.T) {
	bar := ansi.Strip(bottomBarHint(110, hintResults))
	for _, want := range []string{"scroll", "g/G", "Home/End"} {
		if !strings.Contains(bar, want) {
			t.Errorf("a 110-cell bar dropped %q, which a user cannot guess:\n%s", want, bar)
		}
	}
}
