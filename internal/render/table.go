package render

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

// nullCell is how SQL NULL prints in table output. Unlike text mode — where
// NULL and the empty string both collapse to a blank cell — the table is for
// human eyes at a terminal, so it shows the distinction json already carries.
// Only Postgres can produce it: the sqlserver adapter's Path A never yields a
// nil *string (design.md §7.2).
const nullCell = "NULL"

// ellipsis marks a cell truncated at MaxColWidth. Matches the convention
// previewSQL already uses for the list command's SQL column.
const ellipsis = "…"

// tableRenderer prints an aligned ASCII box table with a row-count footer.
// It is what `--output auto` resolves to when stdout is a terminal; the
// resolution happens in the CLI layer, so this renderer stays a pure function
// of (Rows, Options) and renders identically to a buffer in tests.
type tableRenderer struct{}

func (tableRenderer) Render(w io.Writer, rows adapter.Rows, opts Options) error {
	// Matches the text renderer: a rowset with no columns renders nothing at
	// all, not an empty frame.
	if len(rows.Columns) == 0 {
		return nil
	}

	t := table.NewWriter()
	t.SetStyle(table.StyleDefault)
	// go-pretty upper-cases headers by default. A SQL identifier is
	// meaningful and may be a case-sensitive quoted name, so it must survive
	// verbatim — this is the one library default that would corrupt data.
	t.Style().Format.Header = text.FormatDefault
	t.Style().Options.SeparateRows = false

	if opts.MaxColWidth > 0 {
		cfgs := make([]table.ColumnConfig, len(rows.Columns))
		for i := range rows.Columns {
			cfgs[i] = table.ColumnConfig{
				Number:   i + 1,
				WidthMax: opts.MaxColWidth,
				// The default enforcer wraps; we truncate instead so one blob
				// column cannot balloon the table's height. text.Snip is
				// display-width aware and is a no-op below the limit.
				WidthMaxEnforcer: snip,
			}
		}
		t.SetColumnConfigs(cfgs)
	}

	if !opts.NoHeaders {
		t.AppendHeader(headerRow(rows.Columns))
	}
	for _, row := range rows.Rows {
		t.AppendRow(dataRow(row, len(rows.Columns)))
	}

	// Render() writes to an output mirror only if one is set, and discards
	// write errors when it does. Taking the string and writing it here keeps
	// error propagation consistent with the text and json renderers.
	if _, err := fmt.Fprintln(w, t.Render()); err != nil {
		return err
	}
	// The footer is chrome, not data: --no-headers means "just the rows".
	if opts.NoHeaders {
		return nil
	}
	_, err := fmt.Fprintf(w, "(%d %s)\n", len(rows.Rows), plural(len(rows.Rows)))
	return err
}

// snip truncates a cell to maxLen display cells, appending an ellipsis. It
// satisfies table.WidthEnforcer.
func snip(col string, maxLen int) string {
	return text.Snip(col, maxLen, ellipsis)
}

func headerRow(columns []string) table.Row {
	row := make(table.Row, len(columns))
	for i, c := range columns {
		row[i] = sanitize(c)
	}
	return row
}

// dataRow renders one row across n columns. A row shorter than the column
// list yields NULL for the missing cells, matching how the json renderer
// treats them (render.go: `ci < len(row)` else null).
func dataRow(row []*string, n int) table.Row {
	out := make(table.Row, n)
	for i := 0; i < n; i++ {
		if i >= len(row) || row[i] == nil {
			out[i] = nullCell
			continue
		}
		out[i] = sanitize(*row[i])
	}
	return out
}

// sanitize collapses control characters to spaces. A value containing a
// newline or tab would otherwise break the row framing, since the box is laid
// out on the assumption that one cell occupies one line. Full fidelity for
// such values stays available through --output json.
func sanitize(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func plural(n int) string {
	if n == 1 {
		return "row"
	}
	return "rows"
}
