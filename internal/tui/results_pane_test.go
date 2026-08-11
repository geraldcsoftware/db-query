package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

func rowsOf(n int) adapter.Rows {
	rows := adapter.Rows{Columns: []string{"id"}}
	for i := 0; i < n; i++ {
		v := fmt.Sprintf("%d", i)
		rows.Rows = append(rows.Rows, []*string{&v})
	}
	return rows
}

func TestResultsPagination(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "") // exercise the default
	var p resultsPane
	p.showRows(rowsOf(432))
	if got := p.pageCount(); got != 5 {
		t.Fatalf("pageCount = %d, want 5 (432 rows / 100 per page, rounded up)", got)
	}
	if p.page != 0 {
		t.Fatalf("initial page = %d, want 0", p.page)
	}
	p.pageDown()
	if p.page != 1 {
		t.Fatalf("page after pageDown = %d, want 1", p.page)
	}
	for i := 0; i < 10; i++ {
		p.pageDown()
	}
	if p.page != 4 {
		t.Fatalf("page clamped at %d, want 4 (last page, 0-indexed)", p.page)
	}
	for i := 0; i < 10; i++ {
		p.pageUp()
	}
	if p.page != 0 {
		t.Fatalf("page clamped at %d, want 0", p.page)
	}
}

func TestResultsPageSizeEnvOverride(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "50")
	var p resultsPane
	p.showRows(rowsOf(432))
	if got := p.pageCount(); got != 9 { // 432/50 = 8.64 -> 9
		t.Fatalf("pageCount = %d, want 9", got)
	}
}

func TestResultsViewShowsOnlyCurrentPageRows(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "10")
	var p resultsPane
	p.showRows(rowsOf(25))
	view0 := ansi.Strip(p.view())
	if !strings.Contains(view0, " 1   0") { // gutter row 1 carrying value "0"
		t.Fatalf("page 0 must contain row 0:\n%s", view0)
	}
	if strings.Contains(view0, "15") {
		t.Fatalf("page 0 must not contain row 15 (it's on page 1):\n%s", view0)
	}
	p.pageDown()
	view1 := ansi.Strip(p.view())
	if !strings.Contains(view1, "15") {
		t.Fatalf("page 1 must contain row 15:\n%s", view1)
	}
}

// TestResultsViewNumbersRowsAbsolutely pins the gutter to the position in the
// whole result rather than in the page: page 2 starts at 11, not at 1.
func TestResultsViewNumbersRowsAbsolutely(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "10")
	var p resultsPane
	p.showRows(rowsOf(25))
	p.pageDown()
	first := strings.Split(ansi.Strip(p.view()), "\n")[1]
	if !strings.HasPrefix(first, " 11  10") {
		t.Fatalf("page 2's first row must be numbered 11, got %q", first)
	}
}

// TestResultsViewColoursNumericColumns pins the cue that separates a column of
// figures from a column of text: numeric columns are right-aligned and pink,
// and that is decided by the data, not by a hardcoded column list.
func TestResultsViewColoursNumericColumns(t *testing.T) {
	amount, status := "40.00", "pending"
	var p resultsPane
	p.showRows(adapter.Rows{
		Columns: []string{"amount", "status"},
		Rows:    [][]*string{{&amount, &status}},
	})
	row := strings.Split(p.view(), "\n")[1]
	// Both cells are padded to their header's width before styling, so the
	// leading space in front of the amount is the right-alignment.
	if !strings.Contains(row, numberSGR+"m "+amount) {
		t.Errorf("a numeric cell must render right-aligned in the number colour:\n%q", row)
	}
	if !strings.Contains(row, textSGR+"m"+status) {
		t.Errorf("a text cell must render left-aligned in body text:\n%q", row)
	}
}

// TestResultsViewCapsColumnWidth pins the pane's column cap: unbounded
// columns let one wide text value push every other column out of a
// fixed-size pane.
func TestResultsViewCapsColumnWidth(t *testing.T) {
	wide := strings.Repeat("x", 200)
	var p resultsPane
	p.showRows(adapter.Rows{Columns: []string{"note"}, Rows: [][]*string{{&wide}}})
	out := p.view()
	if strings.Contains(out, wide) {
		t.Fatalf("a 200-cell value must be truncated to %d cells:\n%s", tuiMaxColWidth, out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > tuiMaxColWidth+8 { // the cell plus the indent and gutter
			t.Fatalf("line is %d cells wide, want the %d-cell cap to hold:\n%s", w, tuiMaxColWidth, out)
		}
	}
}

// TestResultsViewHasNoTrailingBlankLine keeps the pane's block exactly as
// tall as its content: a trailing newline would cost a row of the Results
// pane to a blank line.
func TestResultsViewHasNoTrailingBlankLine(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "10")
	var p resultsPane
	p.showRows(rowsOf(25))
	lines := strings.Split(ansi.Strip(p.view()), "\n")
	if len(lines) != 11 { // one header row plus a full page
		t.Fatalf("view must be a header plus 10 rows and nothing after it, got %d lines", len(lines))
	}
	if strings.TrimSpace(lines[len(lines)-1]) == "" {
		t.Fatal("view must not end in a blank line")
	}
}

// TestResultsMetaSummarisesTheRun keeps the row count and page position on the
// pane's label row, where they read as chrome around the table rather than as
// another row of data.
func TestResultsMetaSummarisesTheRun(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "10")
	var p resultsPane
	p.showRows(rowsOf(25))
	if got := p.meta(); !strings.Contains(got, "25 rows") || !strings.Contains(got, "page 1/3") {
		t.Fatalf("meta = %q, want the row count and the page position", got)
	}
	p.showRows(rowsOf(3))
	if got := p.meta(); got != "3 rows" {
		t.Fatalf("meta = %q, want no page indicator for a single page", got)
	}
	p.clear()
	if got := p.meta(); got != "" {
		t.Fatalf("meta = %q, want nothing before the first run", got)
	}
}

func TestResultsClearAndError(t *testing.T) {
	var p resultsPane
	p.showRows(rowsOf(5))
	p.clear()
	if p.hasContent() {
		t.Fatal("clear must empty the pane")
	}
	p.showError("boom")
	if !p.hasContent() {
		t.Fatal("an error is content")
	}
	if !strings.Contains(p.view(), "boom") {
		t.Fatalf("view must show the error text:\n%s", p.view())
	}
}
