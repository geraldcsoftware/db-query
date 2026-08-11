package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// layoutSizes runs from a comfortable terminal down to sizes too small to hold
// a body, a split or a sidebar at all. Every invariant below must hold at every
// one of them, because View tiles what layoutRects returns and setFocusAt
// hit-tests against it.
var layoutSizes = []struct{ w, h int }{
	{132, 36}, {100, 40}, {80, 24}, {60, 10}, {40, 8}, {40, 5},
	{40, 4}, {40, 2}, {40, 1}, {22, 20}, {8, 20}, {7, 20}, {1, 1},
}

func height(r rect) int { return r.y1 - r.y0 }
func width(r rect) int  { return r.x1 - r.x0 }

// TestLayoutColumnsExactlyFillTheBody is the geometric half of the whole-screen
// height bound: each column must account for every body row — its two panes
// plus its rule, if it has one — or View would emit a short or long frame.
func TestLayoutColumnsExactlyFillTheBody(t *testing.T) {
	for _, size := range layoutSizes {
		lay := computeLayout(size.w, size.h)
		for _, c := range []struct {
			name         string
			top, bottom  pane
			ruleY        int
			hasColumn    bool
			expectedRule bool
		}{
			{"sidebar", paneSchema, paneSaved, lay.sidebarRuleY, lay.ruleX >= 0, lay.sidebarRuleY >= 0},
			{"main", paneQuery, paneResults, lay.mainRuleY, size.w > 0, lay.mainRuleY >= 0},
		} {
			if !c.hasColumn {
				continue
			}
			rows := height(lay.rects[c.top]) + height(lay.rects[c.bottom])
			if c.expectedRule {
				rows++
			}
			if rows != lay.bodyH {
				t.Errorf("%dx%d %s column covers %d rows, want bodyH=%d",
					size.w, size.h, c.name, rows, lay.bodyH)
			}
			if r := lay.rects[c.top]; r.y0 != lay.bodyTop {
				t.Errorf("%dx%d %s: top pane starts at row %d, want %d", size.w, size.h, c.name, r.y0, lay.bodyTop)
			}
			if r := lay.rects[c.bottom]; r.y1 != lay.bodyTop+lay.bodyH {
				t.Errorf("%dx%d %s: bottom pane ends at row %d, want %d",
					size.w, size.h, c.name, r.y1, lay.bodyTop+lay.bodyH)
			}
		}
	}
}

// TestLayoutRectsStayInsideTheScreen keeps every rectangle well formed and on
// screen, since a click is hit-tested against them and View clips to them.
func TestLayoutRectsStayInsideTheScreen(t *testing.T) {
	for _, size := range layoutSizes {
		for p, r := range computeLayout(size.w, size.h).rects {
			if r.x0 > r.x1 || r.y0 > r.y1 {
				t.Errorf("%dx%d: pane %v has inverted rect %+v", size.w, size.h, p, r)
			}
			if r.x0 < 0 || r.y0 < 0 || r.x1 > size.w || r.y1 > max(size.h, 0) {
				t.Errorf("%dx%d: pane %v rect %+v leaves the screen", size.w, size.h, p, r)
			}
		}
	}
}

// TestLayoutSeparatesTheColumnsByExactlyOneRule pins the vertical rule to the
// gap between the two columns: no pane may own the rule's column, or a click on
// it would focus a pane that is not drawn there.
func TestLayoutSeparatesTheColumnsByExactlyOneRule(t *testing.T) {
	for _, size := range layoutSizes {
		lay := computeLayout(size.w, size.h)
		if lay.ruleX < 0 {
			continue
		}
		if got := width(lay.rects[paneSchema]); got != lay.ruleX {
			t.Errorf("%dx%d: sidebar is %d wide, want it to end at the rule column %d", size.w, size.h, got, lay.ruleX)
		}
		if got := lay.rects[paneQuery].x0; got != lay.ruleX+1 {
			t.Errorf("%dx%d: main column starts at %d, want one past the rule at %d", size.w, size.h, got, lay.ruleX)
		}
		if got := lay.rects[paneQuery].x1; got != size.w {
			t.Errorf("%dx%d: main column ends at %d, want the screen's edge", size.w, size.h, got)
		}
	}
}

// TestLayoutDropsTheSidebarOnANarrowTerminal covers the width degradation: too
// narrow for a sidebar, the main column takes the whole width and the sidebar
// panes go empty rather than being squeezed into an unreadable strip.
func TestLayoutDropsTheSidebarOnANarrowTerminal(t *testing.T) {
	lay := computeLayout(minSidebarW-1, 20)
	if lay.ruleX != -1 {
		t.Errorf("ruleX = %d, want no vertical rule below %d columns", lay.ruleX, minSidebarW)
	}
	if w := width(lay.rects[paneSchema]); w != 0 {
		t.Errorf("sidebar is %d wide, want it dropped entirely", w)
	}
	if r := lay.rects[paneQuery]; r.x0 != 0 || r.x1 != minSidebarW-1 {
		t.Errorf("main column %+v, want the full width", r)
	}
}

// TestLayoutOmitsARuleItCannotSurround covers the height degradation: a column
// with no room for two panes and a rule between them gives every row to its top
// pane, so no rule is drawn with nothing on one side of it.
func TestLayoutOmitsARuleItCannotSurround(t *testing.T) {
	lay := computeLayout(80, 6) // bodyH = 2: a rule would leave a pane with no rows
	if lay.sidebarRuleY != -1 || lay.mainRuleY != -1 {
		t.Fatalf("sidebarRuleY = %d, mainRuleY = %d, want neither rule drawn", lay.sidebarRuleY, lay.mainRuleY)
	}
	if got := height(lay.rects[paneSchema]); got != lay.bodyH {
		t.Errorf("Schema takes %d rows, want the whole body of %d", got, lay.bodyH)
	}
	if got := height(lay.rects[paneSaved]); got != 0 {
		t.Errorf("Saved takes %d rows, want none", got)
	}
}

// TestViewJoinsItsRulesAtJunctions pins the cue that makes the rules read as
// one frame: every place a rule meets the vertical rule carries the junction
// rune for that meeting, not a disconnected line fragment.
func TestViewJoinsItsRulesAtJunctions(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 132, 36
	m.recomputeLayout()
	lay := computeLayout(m.width, m.height)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")

	for _, tc := range []struct {
		row  int
		want string
		what string
	}{
		{1, ruleTeeDown, "the top full-width rule"},
		{m.height - 2, ruleTeeUp, "the bottom full-width rule"},
		{lay.sidebarRuleY, ruleTeeLeft, "the sidebar's rule"},
		{lay.mainRuleY, ruleTeeRight, "the main column's rule"},
	} {
		got := string([]rune(lines[tc.row])[lay.ruleX])
		if got != tc.want {
			t.Errorf("%s meets the vertical rule as %q, want %q", tc.what, got, tc.want)
		}
	}
	// Every other body row carries the plain vertical rule.
	for y := lay.bodyTop; y < lay.bodyTop+lay.bodyH; y++ {
		if y == lay.sidebarRuleY || y == lay.mainRuleY {
			continue
		}
		if got := string([]rune(lines[y])[lay.ruleX]); got != ruleVertical {
			t.Errorf("row %d has %q at the rule column, want %q", y, got, ruleVertical)
		}
	}
}
