package tui

import (
	"fmt"
	"strings"
	"testing"

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
	view0 := p.view()
	if !strings.Contains(view0, "\n0\n") && !strings.Contains(view0, "| 0") {
		t.Fatalf("page 0 must contain row 0:\n%s", view0)
	}
	if strings.Contains(view0, "| 15 ") {
		t.Fatalf("page 0 must not contain row 15 (it's on page 1):\n%s", view0)
	}
	p.pageDown()
	view1 := p.view()
	if !strings.Contains(view1, "15") {
		t.Fatalf("page 1 must contain row 15:\n%s", view1)
	}
}

// TestResultsViewCapsColumnWidth pins the pane's column cap: unbounded
// columns let one wide text value wrap every row and destroy the table's
// alignment inside a fixed-size pane.
func TestResultsViewCapsColumnWidth(t *testing.T) {
	wide := strings.Repeat("x", 200)
	var p resultsPane
	p.showRows(adapter.Rows{Columns: []string{"note"}, Rows: [][]*string{{&wide}}})
	out := p.view()
	if strings.Contains(out, wide) {
		t.Fatalf("a 200-cell value must be truncated to %d cells:\n%s", tuiMaxColWidth, out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(line) > tuiMaxColWidth+8 { // the cell plus the border/padding go-pretty adds
			t.Fatalf("line is %d cells wide, want the %d-cell cap to hold:\n%s", len(line), tuiMaxColWidth, out)
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
	out := p.view()
	if !strings.HasSuffix(out, ")") {
		t.Fatalf("view must end with the page indicator and nothing after it, got %q", out[len(out)-20:])
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
