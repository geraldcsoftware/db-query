package tui

// rect is an on-screen bounding box, half-open in both axes: columns
// [x0, x1) and rows [y0, y1). An empty rect is legitimate — it is how a pane
// the terminal is too small to draw reports that it is not on screen, and
// contains then rejects every point, so a click can never focus it.
type rect struct{ x0, y0, x1, y1 int }

func (r rect) contains(x, y int) bool {
	return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1
}

// minSidebarW is the narrowest terminal that still gets a sidebar. Below it a
// sidebar could not hold a readable table name beside the main column, so the
// main column takes the whole width and the sidebar panes go empty.
const minSidebarW = 8

// sidebarMinW and sidebarMaxW bound the sidebar around its quarter-width
// preference: wide enough for a table name and its column count, narrow enough
// that the Results table keeps the room it needs.
const (
	sidebarMinW = 22
	sidebarMaxW = 36
)

// layout is one frame's full geometry: the four pane rectangles plus the rows
// and columns the separator rules occupy. Rendering and mouse hit-testing both
// read it, which is what keeps a click landing on the pane drawn under the
// pointer.
//
//	row 0              top bar
//	row 1              horizontal rule, full width
//	rows 2 .. h-3      body
//	row h-2            horizontal rule, full width
//	row h-1            bottom bar
type layout struct {
	// bodyTop and bodyH bound the rows between the two full-width rules. bodyH
	// is 0 when the terminal is too short to hold a body at all.
	bodyTop, bodyH int

	// ruleX is the column of the vertical rule dividing the sidebar from the
	// main column, or -1 when the terminal is too narrow for a sidebar.
	ruleX int

	// sidebarRuleY and mainRuleY are the body rows carrying each column's own
	// horizontal rule, or -1 when that column is too short to be split.
	sidebarRuleY, mainRuleY int

	rects map[pane]rect
}

// computeLayout divides a w x h terminal into the layout above: Schema over
// Saved in the sidebar, Query over Results in the main column.
func computeLayout(w, h int) layout {
	// The body starts below the top bar and its rule, except on a terminal too
	// short to hold either — there the empty rectangles still have to name a row
	// that exists, so that hit-testing works on coordinates the screen has.
	l := layout{bodyTop: clamp(2, 0, max(h, 0)), ruleX: -1, sidebarRuleY: -1, mainRuleY: -1}
	l.bodyH = max(0, h-4) // the two bars and their two rules
	bodyEnd := l.bodyTop + l.bodyH

	sidebarW, mainX0 := 0, 0
	if w >= minSidebarW {
		// The vertical rule and at least one main column follow the sidebar, so
		// the preferred width yields when the terminal cannot hold both.
		sidebarW = min(clamp(w/4, sidebarMinW, sidebarMaxW), w-2)
		l.ruleX = sidebarW
		mainX0 = sidebarW + 1
	}

	// Schema takes three fifths of the sidebar; the Query editor takes a
	// quarter of the main column, bounded so a tall terminal does not turn it
	// into a page of empty buffer.
	sidebarRuleY, schemaEnd, savedStart := splitColumn(l.bodyTop, l.bodyH, l.bodyH*3/5)
	mainRuleY, queryEnd, resultsStart := splitColumn(l.bodyTop, l.bodyH, clamp(l.bodyH/4, 3, 8))
	if sidebarW > 0 {
		l.sidebarRuleY = sidebarRuleY
	}
	l.mainRuleY = mainRuleY

	l.rects = map[pane]rect{
		paneSchema:  {0, l.bodyTop, sidebarW, schemaEnd},
		paneSaved:   {0, savedStart, sidebarW, bodyEnd},
		paneQuery:   {mainX0, l.bodyTop, max(mainX0, w), queryEnd},
		paneResults: {mainX0, resultsStart, max(mainX0, w), bodyEnd},
	}
	return l
}

// splitColumn divides a column of h rows starting at top into a top pane, one
// horizontal rule, and a bottom pane, given the top pane's preferred height.
// When the column cannot hold all three the top pane takes every row, the
// bottom pane is empty and there is no rule (-1): a rule with nothing on one
// side of it reads as a stray line rather than as a divider.
func splitColumn(top, h, preferred int) (ruleY, topEnd, bottomStart int) {
	end := top + h
	if preferred > h-2 {
		preferred = h - 2
	}
	if preferred < 1 {
		return -1, end, end
	}
	ruleY = top + preferred
	return ruleY, ruleY, ruleY + 1
}

// layoutRects is the pane geometry alone, for the model's stored hit-testing
// map.
func layoutRects(w, h int) map[pane]rect { return computeLayout(w, h).rects }

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }
