package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// nullCell is how SQL NULL prints, and ellipsis marks a cell clipped at
// tuiMaxColWidth. Both match internal/render's table output so a row reads the
// same way in the pane as under `db-query query --output table`.
const (
	nullCell = "NULL"
	ellipsis = "…"
)

// colGap separates two columns. Borders would only add noise to a pane whose
// edges are already drawn by the layout's rules.
const colGap = "  "

// moreLeft and moreRight mark a table clipped horizontally, one cell against
// each edge of the column area. Without them a result too wide for the pane is
// indistinguishable from one that ends where the pane does.
const (
	moreLeft  = "‹"
	moreRight = "›"
)

// resultTable is one page measured for display: every cell's display text, and
// the widths the gutter and each column need. Measuring is separated from
// rendering because horizontal scrolling has to know how many columns fit
// before the pane can clamp its own offset, and asking that question must not
// mean rendering a table nobody draws.
type resultTable struct {
	columns []string
	cells   [][]string
	widths  []int
	numeric []bool
	gutterW int

	// first is the absolute number of the page's first row, so page 2 numbers
	// its rows from 101 rather than from 1.
	first int
}

// measureTable converts a page into the display strings and widths a table is
// drawn from. Numeric columns are detected here, from the page's own data
// rather than from a hardcoded column list, so a text column of digits aligns
// and colours like the numbers it holds.
func measureTable(rows adapter.Rows, firstRow int) resultTable {
	n := len(rows.Columns)
	t := resultTable{columns: rows.Columns, first: firstRow}
	if n == 0 {
		return t
	}
	t.cells = tableCells(rows, n)
	t.gutterW = len(strconv.Itoa(max(firstRow+len(t.cells)-1, 1)))
	t.widths = make([]int, n)
	t.numeric = make([]bool, n)
	for c := range t.widths {
		t.widths[c] = ansi.StringWidth(rows.Columns[c])
		t.numeric[c] = isNumericColumn(t.cells, c)
		for _, row := range t.cells {
			t.widths[c] = max(t.widths[c], ansi.StringWidth(row[c]))
		}
	}
	return t
}

// span is the cells columns [from, to) occupy, each with the gap that precedes
// it.
func (t resultTable) span(from, to int) int {
	w := 0
	for c := from; c < to; c++ {
		w += len(colGap) + t.widths[c]
	}
	return w
}

// budget is the room left for the columns themselves once the leading indent
// and the gutter are taken. The two edge-marker slots are only subtracted when
// the table is clipped, since an unclipped one draws no markers and should not
// pay for them.
func (t resultTable) budget(width int, clipped bool) int {
	b := width - 1 - t.gutterW
	if clipped {
		b -= 2
	}
	return b
}

// fits reports how many columns starting at left can be drawn in width cells,
// and whether any column is left hidden on either side. At least one column is
// always drawn: a pane too narrow for even a single column shows that column
// clipped rather than showing nothing at all. A width of zero is a pane the
// layout has not sized yet, which is treated as unbounded.
func (t resultTable) fits(left, width int) (count int, clipped bool) {
	n := len(t.columns)
	if n == 0 {
		return 0, false
	}
	if width <= 0 {
		return n - left, false
	}
	if left == 0 && t.span(0, n) <= t.budget(width, false) {
		return n, false
	}
	budget, used := t.budget(width, true), 0
	for c := left; c < n; c++ {
		used += len(colGap) + t.widths[c]
		if used > budget && c > left {
			return c - left, true
		}
	}
	return n - left, left > 0
}

// maxLeft is the furthest right the view may start and still end on the last
// column, so scrolling right stops with the table against the pane's edge
// rather than sliding it off into empty space.
func (t resultTable) maxLeft(width int) int {
	n := len(t.columns)
	if n == 0 || width <= 0 {
		return 0
	}
	if t.span(0, n) <= t.budget(width, false) {
		return 0
	}
	budget, used := t.budget(width, true), 0
	for c := n - 1; c >= 0; c-- {
		used += len(colGap) + t.widths[c]
		if used > budget {
			return min(c+1, n-1)
		}
	}
	return 0
}

// render lays the table out as a borderless table: a right-aligned row-number
// gutter, a muted header, then the values. Numeric columns are right-aligned
// and coloured, which is why the pane renders its own table rather than reusing
// internal/render — that renderer is deliberately neutral and cannot express
// per-column styling.
//
// left is the first column drawn and top the first row, so the pane can show
// any window of a table larger than itself. The header and the gutter are drawn
// outside those windows and so stay put: the header names the columns the rows
// beneath it belong to, and the gutter says which row a value came from.
func (t resultTable) render(left, width, top, height int) string {
	n := len(t.columns)
	if n == 0 {
		return ""
	}
	left = clamp(left, 0, n-1)
	count, clipped := t.fits(left, width)
	end := min(left+count, n)

	lines := make([]string, 0, len(t.cells)+1)
	lines = append(lines, t.headerLine(left, end, clipped))
	for i, row := range t.cells {
		lines = append(lines, t.rowLine(i, row, left, end, clipped))
	}
	window := listScroll{offset: top, height: height}.window(lines[1:])
	return strings.Join(append(lines[:1], window...), "\n")
}

// headerLine draws the column names, with an edge marker against whichever side
// still has columns hidden behind it.
func (t resultTable) headerLine(left, end int, clipped bool) string {
	names := make([]string, 0, end-left)
	for c := left; c < end; c++ {
		names = append(names, pad(t.columns[c], t.widths[c], t.numeric[c]))
	}
	return " " + resultHeaderStyle.Render(pad("#", t.gutterW, true)) +
		edgeMarker(clipped, left > 0, moreLeft) +
		resultHeaderStyle.Render(colGap+strings.Join(names, colGap)) +
		edgeMarker(clipped, end < len(t.columns), moreRight)
}

// rowLine draws one row of values. Its marker slot is always blank — the
// markers belong on the header, where a user reads what the columns are — but
// the slot is still reserved on a clipped table so the values stay lined up
// under their names.
func (t resultTable) rowLine(i int, row []string, left, end int, clipped bool) string {
	var b strings.Builder
	b.WriteString(" ")
	b.WriteString(resultGutterStyle.Render(pad(strconv.Itoa(t.first+i), t.gutterW, true)))
	b.WriteString(edgeMarker(clipped, false, ""))
	for c := left; c < end; c++ {
		style := resultTextStyle
		if t.numeric[c] {
			style = resultNumberStyle
		}
		b.WriteString(colGap)
		b.WriteString(style.Render(pad(row[c], t.widths[c], t.numeric[c])))
	}
	return b.String()
}

// edgeMarker is one cell of the column area's border: the glyph when something
// is hidden behind it, a blank when nothing is, and nothing at all on a table
// that is not clipped and so has no border to draw.
func edgeMarker(clipped, hidden bool, glyph string) string {
	switch {
	case !clipped:
		return ""
	case hidden:
		return resultGutterStyle.Render(glyph)
	default:
		return " "
	}
}

// tableCells converts a page's rows into their display strings: NULL spelled
// out, control characters flattened so a value containing a newline cannot
// break the row framing, and every cell clipped at tuiMaxColWidth so one wide
// text column cannot push every other column off the pane.
func tableCells(rows adapter.Rows, n int) [][]string {
	out := make([][]string, len(rows.Rows))
	for i, row := range rows.Rows {
		cells := make([]string, n)
		for c := range cells {
			if c >= len(row) || row[c] == nil {
				cells[c] = nullCell
				continue
			}
			cells[c] = ansi.Truncate(sanitizeCell(*row[c]), tuiMaxColWidth, ellipsis)
		}
		out[i] = cells
	}
	return out
}

// isNumericColumn reports whether every value in column c that carries any
// text parses as a number. It is a property of the page's data, not of a
// hardcoded column list, so a text column of digits aligns and colours like
// the numbers it holds.
func isNumericColumn(cells [][]string, c int) bool {
	seen := false
	for _, row := range cells {
		v := strings.TrimSpace(row[c])
		if v == "" || v == nullCell {
			continue
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return false
		}
		seen = true
	}
	return seen
}

// sanitizeCell collapses control characters to spaces.
func sanitizeCell(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// pad widens s to w display cells, on the right for text and on the left for
// numbers so a column of figures lines up on its last digit.
func pad(s string, w int, right bool) string {
	fill := w - ansi.StringWidth(s)
	if fill <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", fill) + s
	}
	return s + strings.Repeat(" ", fill)
}
