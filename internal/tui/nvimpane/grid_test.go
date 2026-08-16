package nvimpane

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The grid is driven here exactly as Neovim drives it: batches of redraw
// events, decoded as the RPC layer decodes them — integers as int64, calls as
// []any. Building them by hand is the point, since it is the only way to reach
// the shapes a live session produces only occasionally, like a scroll or an
// omitted highlight id.

func ev(name string, calls ...[]any) []any {
	out := []any{name}
	for _, c := range calls {
		out = append(out, c)
	}
	return out
}

func gridLine(row, col int, cells ...[]any) []any {
	anyCells := make([]any, len(cells))
	for i, c := range cells {
		anyCells[i] = c
	}
	return ev("grid_line", []any{int64(1), int64(row), int64(col), anyCells})
}

// gcell is one grid_line cell: text alone, text with a highlight id, or text
// with an id and a repeat count. Neovim sends one grapheme per cell, so the
// text here is one character and never a word.
func gcell(parts ...any) []any { return parts }

// text spells a word out as Neovim does, one cell per grapheme, all under the
// same highlight.
func text(s string, hl int) [][]any {
	out := make([][]any, 0, len(s))
	for _, r := range s {
		out = append(out, gcell(string(r), int64(hl)))
	}
	return out
}

var flush = ev("flush")

// rows is the painted grid as plain text, one string per row.
//
// A trailing run of unstyled blanks is dropped by the renderer itself, so a row
// comes back no longer than its last painted cell. That is why the host pads a
// grid row back out to the pane's width and skips its usual trailing-space trim
// only for these rows: a blank carrying a background is not an unstyled blank,
// and it survives to be painted.
func rows(g *Grid) []string {
	return strings.Split(ansi.Strip(g.Render()), "\n")
}

func TestGridLineRepeatsAnOmittedHighlightAndHonoursRepeatCounts(t *testing.T) {
	g := NewGrid(10, 2)
	g.Apply([][]any{
		gridLine(0, 0,
			gcell("a", int64(1)),
			gcell("b"),                     // no id: repeats the previous cell's
			gcell("c", int64(2)),           // a new id
			gcell("-", int64(2), int64(4)), // and a run of four
		),
		flush,
	})

	if got := rows(g)[0]; got != "abc----" {
		t.Fatalf("row 0 = %q, want the repeat count expanded", got)
	}
}

// TestGridLineSkipsTheRightHalfOfAWideGrapheme: Neovim sends "" for the second
// cell of a double-width character, the first having already claimed both
// columns. Painting the empty string as a blank would rub out half of it.
func TestGridLineSkipsTheRightHalfOfAWideGrapheme(t *testing.T) {
	g := NewGrid(6, 1)
	g.Apply([][]any{
		gridLine(0, 0, gcell("界", int64(0)), gcell(""), gcell("x", int64(0))),
		flush,
	})
	if got := rows(g)[0]; !strings.HasPrefix(got, "界x") {
		t.Fatalf("row 0 = %q, want the wide character followed by x", got)
	}
}

func TestGridScrollMovesTextBothWays(t *testing.T) {
	fill := func(g *Grid) {
		for i, word := range []string{"one", "two", "three", "four"} {
			g.Apply([][]any{gridLine(i, 0, text(word, 0)...)})
		}
		g.Apply([][]any{flush})
	}

	// A positive row count moves text up, which is what scrolling down does.
	up := NewGrid(8, 4)
	fill(up)
	up.Apply([][]any{
		ev("grid_scroll", []any{int64(1), int64(0), int64(4), int64(0), int64(8), int64(1), int64(0)}),
		flush,
	})
	if got := rows(up); !strings.HasPrefix(got[0], "two") || !strings.HasPrefix(got[2], "four") {
		t.Errorf("scrolling up left %q", got)
	}

	// A negative one moves text down.
	down := NewGrid(8, 4)
	fill(down)
	down.Apply([][]any{
		ev("grid_scroll", []any{int64(1), int64(0), int64(4), int64(0), int64(8), int64(-1), int64(0)}),
		flush,
	})
	if got := rows(down); !strings.HasPrefix(got[1], "one") || !strings.HasPrefix(got[3], "three") {
		t.Errorf("scrolling down left %q", got)
	}
}

// TestGridResizeKeepsTheOverlap: Neovim repaints after a resize, but not in the
// same batch, so blanking the grid would flash an empty pane in between.
func TestGridResizeKeepsTheOverlap(t *testing.T) {
	g := NewGrid(10, 3)
	g.Apply([][]any{
		gridLine(0, 0, text("keep", 0)...),
		gridLine(2, 0, text("gone", 0)...),
		flush,
	})

	g.Apply([][]any{ev("grid_resize", []any{int64(1), int64(6), int64(2)}), flush})
	g.Resize(6, 2)

	if w, h := g.Size(); w != 6 || h != 2 {
		t.Fatalf("size = %dx%d, want 6x2", w, h)
	}
	got := rows(g)
	if len(got) != 2 {
		t.Fatalf("painted %d rows, want 2", len(got))
	}
	if !strings.HasPrefix(got[0], "keep") {
		t.Errorf("the overlap was not carried over: %q", got)
	}
}

// TestNothingIsPaintedBeforeAFlush: Neovim sends a batch in pieces, and the
// first batch after attach is global setup with no grid content and no flush at
// all. Apply reporting the flush is what lets the pane hold its last frame.
func TestNothingIsPaintedBeforeAFlush(t *testing.T) {
	g := NewGrid(8, 1)

	if g.Apply([][]any{ev("hl_attr_define", []any{int64(1), map[string]any{"bold": true}})}) {
		t.Error("a batch with no flush reported one")
	}
	if g.Apply([][]any{gridLine(0, 0, text("half", 0)...)}) {
		t.Error("a batch that only painted cells reported a flush")
	}
	if !g.Apply([][]any{gridLine(0, 4, text("done", 0)...), flush}) {
		t.Error("a batch ending in a flush did not report one")
	}
	if got := rows(g)[0]; got != "halfdone" {
		t.Fatalf("row 0 = %q, want both halves once the flush arrived", got)
	}
}

// TestABackgroundEqualToNeovimsDefaultIsDropped is what lets the pane sit on
// the terminal's own background like every other pane, while a popup that picks
// its own colour still reads as floating.
func TestABackgroundEqualToNeovimsDefaultIsDropped(t *testing.T) {
	const defaultBG, popupBG = 0x101010, 0x2c2e33
	g := NewGrid(4, 1)
	g.Apply([][]any{
		ev("default_colors_set", []any{int64(0xe0e0e0), int64(defaultBG), int64(0)}),
		ev("hl_attr_define",
			[]any{int64(1), map[string]any{"background": int64(defaultBG)}},
			[]any{int64(2), map[string]any{"background": int64(popupBG)}},
		),
		gridLine(0, 0, gcell("a", int64(1)), gcell("b", int64(2))),
		flush,
	})

	painted := g.Render()
	if strings.Contains(painted, "48;2;16;16;16") {
		t.Errorf("Neovim's own background was painted into the pane:\n%q", painted)
	}
	if !strings.Contains(painted, "48;2;44;46;51") {
		t.Errorf("a background that differs from the default was dropped too:\n%q", painted)
	}
}

func TestCursorPositionShapeAndBlinkFollowTheMode(t *testing.T) {
	g := NewGrid(10, 4)
	g.Apply([][]any{
		ev("mode_info_set", []any{true, []any{
			map[string]any{"cursor_shape": "block"},
			map[string]any{"cursor_shape": "vertical", "blinkwait": int64(700), "blinkon": int64(400), "blinkoff": int64(250)},
		}}),
		ev("grid_cursor_goto", []any{int64(1), int64(2), int64(5)}),
		ev("mode_change", []any{"normal", int64(0)}),
		flush,
	})

	if c, r := g.Cursor(); c != 5 || r != 2 {
		t.Errorf("cursor = col %d row %d, want col 5 row 2", c, r)
	}
	if g.Mode() != "normal" || g.CursorShape() != "block" || g.CursorBlinks() {
		t.Errorf("normal mode reported as %q/%q/blink=%v", g.Mode(), g.CursorShape(), g.CursorBlinks())
	}

	g.Apply([][]any{ev("mode_change", []any{"insert", int64(1)}), flush})
	if g.Mode() != "insert" || g.CursorShape() != "vertical" || !g.CursorBlinks() {
		t.Errorf("insert mode reported as %q/%q/blink=%v", g.Mode(), g.CursorShape(), g.CursorBlinks())
	}

	// Neovim hides the cursor while it is busy, and says so with its own event
	// rather than by moving it somewhere invisible.
	g.Apply([][]any{ev("busy_start"), flush})
	if !g.CursorHidden() {
		t.Error("busy_start did not hide the cursor")
	}
	g.Apply([][]any{ev("busy_stop"), flush})
	if g.CursorHidden() {
		t.Error("busy_stop did not bring the cursor back")
	}
}

// TestUnknownAndMalformedEventsAreIgnored: the renderer subscribes to one UI
// extension and must survive everything else Neovim sends, including events
// added by a future version.
func TestUnknownAndMalformedEventsAreIgnored(t *testing.T) {
	g := NewGrid(8, 1)
	g.Apply([][]any{
		ev("something_new_in_0_13", []any{int64(1)}),
		ev("option_set", []any{"guifont", "whatever"}),
		ev("grid_line"),                       // no calls
		ev("grid_line", []any{int64(1)}),      // too few arguments
		gridLine(99, 0, gcell("x", int64(0))), // a row off the end of the grid
		ev("grid_scroll", []any{int64(1)}),    // too few arguments
		gridLine(0, 0, text("ok", 0)...),
		flush,
	})
	if got := rows(g)[0]; !strings.HasPrefix(got, "ok") {
		t.Fatalf("row 0 = %q, want the one well-formed event still applied", got)
	}
}
