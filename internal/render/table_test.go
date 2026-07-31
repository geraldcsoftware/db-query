package render

import (
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

func renderTable(t *testing.T, rows adapter.Rows, opts Options) string {
	t.Helper()
	var b strings.Builder
	if err := Render(&b, "table", rows, opts); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTableRenderer(t *testing.T) {
	got := renderTable(t, sampleRows(), Options{})
	want := "" +
		"+----+-------+----------+\n" +
		"| id | name  | nickname |\n" +
		"+----+-------+----------+\n" +
		"| 1  | Ada   | NULL     |\n" +
		"| 2  | Grace |          |\n" +
		"+----+-------+----------+\n" +
		"(2 rows)\n"
	if got != want {
		t.Fatalf("table =\n%s\nwant:\n%s", got, want)
	}
}

// TestTableRendererPreservesHeaderCase is the regression guard for go-pretty's
// header formatting: it upper-cases headers unless Format.Header is pinned to
// FormatDefault. A SQL identifier may be a case-sensitive quoted name, so the
// column must come back exactly as the server reported it.
func TestTableRendererPreservesHeaderCase(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"user_name", "createdAt", "UPPER"},
		Rows:    [][]*string{{ptr("a"), ptr("b"), ptr("c")}},
	}
	got := renderTable(t, rows, Options{})
	for _, col := range rows.Columns {
		if !strings.Contains(got, col) {
			t.Errorf("column %q was mangled:\n%s", col, got)
		}
	}
	if strings.Contains(got, "USER NAME") || strings.Contains(got, "USER_NAME") {
		t.Errorf("header was upper-cased:\n%s", got)
	}
}

// TestTableRendererNullVsEmpty pins the distinction text mode collapses: a
// SQL NULL is visibly NULL, an empty string is a blank cell.
func TestTableRendererNullVsEmpty(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"v"},
		Rows:    [][]*string{{nil}, {ptr("")}},
	}
	got := renderTable(t, rows, Options{})
	if !strings.Contains(got, "| NULL |") {
		t.Errorf("NULL not rendered as NULL:\n%s", got)
	}
	if !strings.Contains(got, "|      |") {
		t.Errorf("empty string should be a blank cell:\n%s", got)
	}
}

func TestTableRendererEmpty(t *testing.T) {
	// Matches the text renderer: no columns means no output at all, not an
	// empty frame.
	if got := renderTable(t, adapter.Rows{}, Options{}); got != "" {
		t.Fatalf("empty rows must render nothing, got %q", got)
	}
}

func TestTableRendererZeroRows(t *testing.T) {
	rows := adapter.Rows{Columns: []string{"id", "name"}}
	got := renderTable(t, rows, Options{})
	want := "" +
		"+----+------+\n" +
		"| id | name |\n" +
		"+----+------+\n" +
		"+----+------+\n" +
		"(0 rows)\n"
	if got != want {
		t.Fatalf("zero rows =\n%s\nwant:\n%s", got, want)
	}
}

func TestTableRendererSingularFooter(t *testing.T) {
	rows := adapter.Rows{Columns: []string{"id"}, Rows: [][]*string{{ptr("1")}}}
	got := renderTable(t, rows, Options{})
	if !strings.HasSuffix(got, "(1 row)\n") {
		t.Fatalf("want singular footer, got:\n%s", got)
	}
}

// TestTableRendererNoHeaders pins that --no-headers drops the header row and
// the row-count footer, leaving only the framed data.
func TestTableRendererNoHeaders(t *testing.T) {
	got := renderTable(t, sampleRows(), Options{NoHeaders: true})
	want := "" +
		"+---+-------+------+\n" +
		"| 1 | Ada   | NULL |\n" +
		"| 2 | Grace |      |\n" +
		"+---+-------+------+\n"
	if got != want {
		t.Fatalf("no-headers table =\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "nickname") {
		t.Error("header row leaked into --no-headers output")
	}
	if strings.Contains(got, "rows)") {
		t.Error("row-count footer leaked into --no-headers output")
	}
}

func TestTableRendererMaxColWidth(t *testing.T) {
	long := "Lorem ipsum dolor sit amet consectetur adipiscing elit"
	rows := adapter.Rows{Columns: []string{"note"}, Rows: [][]*string{{ptr(long)}}}

	t.Run("truncates with an ellipsis", func(t *testing.T) {
		got := renderTable(t, rows, Options{MaxColWidth: 20})
		if !strings.Contains(got, "…") {
			t.Errorf("want ellipsis:\n%s", got)
		}
		if strings.Contains(got, "elit") {
			t.Errorf("cell was not truncated:\n%s", got)
		}
		for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
			if strings.HasPrefix(line, "(") {
				continue
			}
			// 20 cells of content + 2 padding + 2 borders.
			if w := len([]rune(line)); w != 24 {
				t.Errorf("line %q is %d cells wide, want 24", line, w)
			}
		}
	})

	t.Run("zero means unlimited", func(t *testing.T) {
		got := renderTable(t, rows, Options{MaxColWidth: 0})
		if !strings.Contains(got, long) {
			t.Errorf("MaxColWidth 0 must not truncate:\n%s", got)
		}
	})

	t.Run("short cells are untouched", func(t *testing.T) {
		got := renderTable(t, sampleRows(), Options{MaxColWidth: 20})
		if strings.Contains(got, "…") {
			t.Errorf("cells under the limit must not be snipped:\n%s", got)
		}
	})
}

// TestTableRendererControlChars pins that a value containing a newline or tab
// cannot break the row framing — every rendered line stays the same width.
func TestTableRendererControlChars(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"v"},
		Rows:    [][]*string{{ptr("line\nbreak")}, {ptr("tab\there")}},
	}
	got := renderTable(t, rows, Options{})
	if strings.Contains(got, "\t") {
		t.Errorf("tab survived into table output:\n%q", got)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	width := len([]rune(lines[0]))
	for _, line := range lines {
		if strings.HasPrefix(line, "(") {
			continue
		}
		if len([]rune(line)) != width {
			t.Fatalf("ragged frame — %q is %d wide, want %d:\n%s", line, len([]rune(line)), width, got)
		}
	}
}

// TestTableRendererWideRunes pins display-width alignment: CJK glyphs occupy
// two cells each, so a rune count would render a ragged frame.
func TestTableRendererWideRunes(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"city", "note"},
		Rows: [][]*string{
			{ptr("東京"), ptr("a")},
			{ptr("よこはま"), ptr("b")},
			{ptr("Paris"), ptr("c")},
		},
	}
	got := renderTable(t, rows, Options{})
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	// Every border line must be identical, which only holds if the widest
	// cell was measured in display cells rather than runes.
	if lines[0] != lines[2] || lines[0] != lines[len(lines)-2] {
		t.Fatalf("border lines differ — width math is wrong:\n%s", got)
	}
	if !strings.Contains(got, "よこはま") {
		t.Fatalf("wide value missing:\n%s", got)
	}
}

// TestTableRendererShortRow pins that a row shorter than the column list is
// padded rather than producing a ragged frame, matching how the json renderer
// treats the missing cells (null).
func TestTableRendererShortRow(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"a", "b", "c"},
		Rows:    [][]*string{{ptr("1")}},
	}
	got := renderTable(t, rows, Options{})
	want := "" +
		"+---+------+------+\n" +
		"| a | b    | c    |\n" +
		"+---+------+------+\n" +
		"| 1 | NULL | NULL |\n" +
		"+---+------+------+\n" +
		"(1 row)\n"
	if got != want {
		t.Fatalf("short row =\n%s\nwant:\n%s", got, want)
	}
}
