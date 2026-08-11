package tui

import (
	"os"
	"strconv"

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
}

func (r *resultsPane) clear() { *r = resultsPane{} }

func (r resultsPane) hasContent() bool { return r.errText != "" || len(r.rows.Columns) > 0 }

func (r *resultsPane) showError(msg string) { *r = resultsPane{errText: msg} }

func (r *resultsPane) showRows(rows adapter.Rows) { *r = resultsPane{rows: rows} }

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

func (r *resultsPane) pageDown() {
	if r.page < r.pageCount()-1 {
		r.page++
	}
}

func (r *resultsPane) pageUp() {
	if r.page > 0 {
		r.page--
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
	return renderResultTable(r.currentSlice(), r.page*pageSize()+1)
}

// meta is the summary right-aligned on the pane's label row: how many rows the
// run returned, and where in them the current page sits once there is more
// than one.
func (r resultsPane) meta() string {
	if r.errText != "" || len(r.rows.Columns) == 0 {
		return ""
	}
	n := len(r.rows.Rows)
	out := strconv.Itoa(n) + " rows"
	if n == 1 {
		out = "1 row"
	}
	if r.pageCount() > 1 {
		out += " · " + pageIndicator(r)
	}
	return out
}

// pageIndicator summarizes the current page position, e.g.
// "page 2/5 (rows 101-200)".
func pageIndicator(r resultsPane) string {
	size := pageSize()
	start := r.page*size + 1
	end := start + len(r.currentSlice().Rows) - 1
	return "page " + strconv.Itoa(r.page+1) + "/" + strconv.Itoa(r.pageCount()) +
		" (rows " + strconv.Itoa(start) + "-" + strconv.Itoa(end) + ")"
}
