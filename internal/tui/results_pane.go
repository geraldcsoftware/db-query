package tui

import (
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

const defaultTUIPageSize = 100

// tuiMaxColWidth caps a rendered cell's display width. It mirrors the value
// internal/cli uses for --max-col-width's default so the Results pane and
// `db-query query --output table` truncate identically; the constant is
// duplicated rather than shared because internal/cli imports this package.
const tuiMaxColWidth = 50

// pageSize reads DB_QUERY_TUI_PAGE_SIZE, defaulting to 100. An unset, empty,
// non-numeric, or non-positive value all fall back to the default rather
// than erroring — this is a display convenience, not a flag worth failing
// the whole session over.
func pageSize() int {
	v := os.Getenv("DB_QUERY_TUI_PAGE_SIZE")
	if v == "" {
		return defaultTUIPageSize
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultTUIPageSize
	}
	return n
}

// resultsPane holds the last completed run's outcome for display: either
// error text from a failed run or the columns/rows from a successful one,
// never both at once. It renders one page of rows at a time — pagination is
// purely client-side slicing over rows already fetched through the normal
// pipeline; the user's SQL is never rewritten with LIMIT/OFFSET.
type resultsPane struct {
	errText string
	rows    adapter.Rows
	page    int

	// top is the first row of the current page on screen and left the first
	// column, the two halves of the pane's scroll position. They index into the
	// page, not into the whole result: paging moves between blocks of rows,
	// scrolling moves within the block on screen.
	top, left int

	// w and h are the cells the layout gives the pane's content. Both are zero
	// until the first layout, which the rendering reads as unbounded so an early
	// frame draws the whole page rather than nothing.
	w, h int
}

// A new result, an error, or an empty pane all start the view over, since none
// of them share the rows or the columns the offsets were measured against. The
// pane's size survives, being a property of the layout rather than of the run.
func (r *resultsPane) clear() { *r = resultsPane{w: r.w, h: r.h} }

func (r resultsPane) hasContent() bool { return r.errText != "" || len(r.rows.Columns) > 0 }

func (r *resultsPane) showError(msg string) { *r = resultsPane{errText: msg, w: r.w, h: r.h} }

func (r *resultsPane) showRows(rows adapter.Rows) { *r = resultsPane{rows: rows, w: r.w, h: r.h} }

// setSize records the room the layout gives the pane and brings both offsets
// back inside it. A terminal that grows can otherwise leave the view scrolled
// past rows that have just become visible.
func (r *resultsPane) setSize(w, h int) {
	r.w, r.h = max(0, w), max(0, h)
	r.scrollRows(0)
	r.scrollColumns(0)
}

// visibleRows is how many data rows the pane draws: its content rows less the
// header pinned above them. An unsized pane reports 0, which the windowing
// reads as no limit.
func (r resultsPane) visibleRows() int {
	if r.h <= 0 {
		return 0
	}
	return max(0, r.h-1)
}

// table measures the current page for display. Widths are a property of the
// page rather than of the whole result — measuring every row of a large result
// to size a column nobody is looking at would cost more than it is worth — so
// they can change when the page does.
func (r resultsPane) table() resultTable {
	return measureTable(r.currentSlice(), r.page*pageSize()+1)
}

// scrollRows moves the view delta rows down the page, clamped to its ends.
func (r *resultsPane) scrollRows(delta int) {
	s := listScroll{offset: r.top, height: r.visibleRows()}
	s.by(delta, len(r.currentSlice().Rows))
	r.top = s.offset
}

// scrollColumns moves the view delta columns right, clamped so the last column
// lands against the pane's edge rather than scrolling past it.
func (r *resultsPane) scrollColumns(delta int) {
	r.left = clamp(r.left+delta, 0, r.table().maxLeft(r.w))
}

func (r *resultsPane) scrollDown() { r.scrollRows(1) }
func (r *resultsPane) scrollUp()   { r.scrollRows(-1) }
func (r *resultsPane) scrollTop()  { r.top = 0 }

func (r *resultsPane) scrollBottom() { r.scrollRows(len(r.currentSlice().Rows)) }

func (r *resultsPane) scrollLeft()  { r.scrollColumns(-1) }
func (r *resultsPane) scrollRight() { r.scrollColumns(1) }

func (r *resultsPane) scrollFirstColumn() { r.left = 0 }
func (r *resultsPane) scrollLastColumn()  { r.left = r.table().maxLeft(r.w) }

// update handles the Results pane's own keys. The pane has no cursor to move,
// so every one of them moves the viewport instead.
func (r *resultsPane) update(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "up", "k":
		r.scrollUp()
	case "down", "j":
		r.scrollDown()
	case "left", "h":
		r.scrollLeft()
	case "right", "l":
		r.scrollRight()
	case "g":
		r.scrollTop()
	// Which of the two a terminal reports for Shift+G depends on the keyboard
	// protocol it negotiated, so both are bound to the same action.
	case "G", "shift+g":
		r.scrollBottom()
	case "home":
		r.scrollFirstColumn()
	case "end":
		r.scrollLastColumn()
	}
}

// pageCount is the number of pages needed to show every row, at least 1 so
// an empty (but non-error) result still has a page to render.
func (r resultsPane) pageCount() int {
	n := len(r.rows.Rows)
	size := pageSize()
	pages := (n + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	return pages
}

// pageDown and pageUp step between blocks of rows. Each shows its new page from
// the first row, since none of the rows on screen carry over, but keeps the
// horizontal position: the columns are the same ones, and throwing the user
// back to the left edge on every page would undo the scrolling they just did.
// The column offset is still re-clamped, because a page whose values are
// narrower measures narrower columns and so may have fewer of them hidden.
func (r *resultsPane) pageDown() {
	if r.page < r.pageCount()-1 {
		r.page++
		r.top = 0
		r.scrollColumns(0)
	}
}

func (r *resultsPane) pageUp() {
	if r.page > 0 {
		r.page--
		r.top = 0
		r.scrollColumns(0)
	}
}

// currentSlice returns the Rows for the current page only — Columns
// unchanged, Rows sliced to [page*size, page*size+size).
func (r resultsPane) currentSlice() adapter.Rows {
	size := pageSize()
	start := r.page * size
	if start > len(r.rows.Rows) {
		start = len(r.rows.Rows)
	}
	end := start + size
	if end > len(r.rows.Rows) {
		end = len(r.rows.Rows)
	}
	return adapter.Rows{Columns: r.rows.Columns, Rows: r.rows.Rows[start:end]}
}

// view renders the current page as a borderless table. --output/--border/
// --max-col-width are not exposed in the TUI: this always renders a table,
// capping cells at tuiMaxColWidth so one wide text column cannot destroy the
// table's alignment inside a fixed-size pane.
func (r resultsPane) view() string {
	if r.errText != "" {
		// Styled so a failed run reads as a failure at a glance rather than as
		// another line of output; lipgloss applies the style per line, so a
		// multi-line message (an error plus its hint) is marked throughout.
		return indentLines(errorStyle.Render("error: " + r.errText))
	}
	return r.table().render(r.left, r.w, r.top, r.visibleRows())
}

// meta is the pane's summary as one string, for callers that want it whole.
func (r resultsPane) meta() string { return strings.Join(r.metaParts(), metaSep) }

// metaParts is the summary right-aligned on the pane's label row, in clauses so
// that a pane too narrow for all of them keeps the ones that matter most: how
// many rows the run returned, where in them the current page sits, and then the
// two positions within that page. The order is the order they are dropped in,
// from the end, and it puts the whole result ahead of any position inside it.
func (r resultsPane) metaParts() []string {
	if r.errText != "" || len(r.rows.Columns) == 0 {
		return nil
	}
	n := len(r.rows.Rows)
	count := strconv.Itoa(n) + " rows"
	if n == 1 {
		count = "1 row"
	}
	out := []string{count}
	rows := r.rowSpan()
	if r.pageCount() > 1 {
		out = append(out, pageIndicator(r, rows == ""))
	}
	if rows != "" {
		out = append(out, rows)
	}
	if cols := r.columnSpan(); cols != "" {
		out = append(out, cols)
	}
	return out
}

// rowSpan names the slice of the page actually on screen, in the same absolute
// numbering the gutter uses. It earns its place only when the page is taller
// than the pane: otherwise the page indicator already says which rows are
// showing, and repeating it would read as a contradiction of itself.
func (r resultsPane) rowSpan() string {
	h, total := r.visibleRows(), len(r.currentSlice().Rows)
	if h <= 0 || total <= h {
		return ""
	}
	first := r.page*pageSize() + r.top + 1
	last := first + min(h, total-r.top) - 1
	return "showing " + strconv.Itoa(first) + "-" + strconv.Itoa(last)
}

// columnSpan says which columns are on screen, and appears only while some are
// not. A span that always reads "cols 1-2/2" would be noise on the one row the
// pane can least afford to spend.
func (r resultsPane) columnSpan() string {
	t := r.table()
	count, clipped := t.fits(r.left, r.w)
	if !clipped {
		return ""
	}
	return "cols " + strconv.Itoa(r.left+1) + "-" + strconv.Itoa(r.left+count) +
		"/" + strconv.Itoa(len(t.columns))
}

// pageIndicator summarizes the current page position, e.g.
// "page 2/5 (rows 101-200)". The row range is dropped when the pane is showing
// only part of the page: the visible slice is reported separately, and two
// ranges disagreeing about which rows are on screen would leave a user
// believing the wrong one.
func pageIndicator(r resultsPane, withRows bool) string {
	out := "page " + strconv.Itoa(r.page+1) + "/" + strconv.Itoa(r.pageCount())
	if !withRows {
		return out
	}
	size := pageSize()
	start := r.page*size + 1
	end := start + len(r.currentSlice().Rows) - 1
	return out + " (rows " + strconv.Itoa(start) + "-" + strconv.Itoa(end) + ")"
}
