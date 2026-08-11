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

// renderResultTable lays one page out as a borderless table: a right-aligned
// row-number gutter, a muted header, then the values. Numeric columns are
// right-aligned and coloured, which is why the pane renders its own table
// rather than reusing internal/render — that renderer is deliberately neutral
// and cannot express per-column styling.
//
// firstRow is the absolute number of the page's first row, so page 2 numbers
// its rows from 101 rather than from 1.
func renderResultTable(rows adapter.Rows, firstRow int) string {
	n := len(rows.Columns)
	if n == 0 {
		return ""
	}
	cells := tableCells(rows, n)

	gutterW := len(strconv.Itoa(max(firstRow+len(cells)-1, 1)))
	widths := make([]int, n)
	numeric := make([]bool, n)
	for c := range widths {
		widths[c] = ansi.StringWidth(rows.Columns[c])
		numeric[c] = isNumericColumn(cells, c)
		for _, row := range cells {
			widths[c] = max(widths[c], ansi.StringWidth(row[c]))
		}
	}

	lines := make([]string, 0, len(cells)+1)
	header := make([]string, n)
	for c, name := range rows.Columns {
		header[c] = pad(name, widths[c], numeric[c])
	}
	lines = append(lines, " "+resultHeaderStyle.Render(pad("#", gutterW, true)+colGap+strings.Join(header, colGap)))

	for i, row := range cells {
		var b strings.Builder
		b.WriteString(" ")
		b.WriteString(resultGutterStyle.Render(pad(strconv.Itoa(firstRow+i), gutterW, true)))
		for c, v := range row {
			style := resultTextStyle
			if numeric[c] {
				style = resultNumberStyle
			}
			b.WriteString(colGap)
			b.WriteString(style.Render(pad(v, widths[c], numeric[c])))
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
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
