package tui

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

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
