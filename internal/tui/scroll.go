package tui

// listScroll keeps a list pane's cursor on screen when the list is taller than
// the rows the layout gives it. The pane owns the geometry — its height comes
// from the layout, its cursor's line from its own row building — so this holds
// only the resulting offset and the arithmetic that moves it.
//
// The offset moves the least distance that brings the cursor back into view,
// rather than recentring on it, so paging down a long catalogue scrolls one row
// at a time instead of jumping half a pane per keystroke.
type listScroll struct {
	offset int
	height int
}

// setHeight records how many content rows the pane has to draw into.
func (s *listScroll) setHeight(h int) {
	if h < 0 {
		h = 0
	}
	s.height = h
}

// follow brings line back into view within a list of total lines. Call it
// whenever the cursor moves or the line count changes — expanding a table does
// both.
func (s *listScroll) follow(line, total int) {
	if s.height <= 0 {
		s.offset = 0
		return
	}
	if line < s.offset {
		s.offset = line
	}
	if line >= s.offset+s.height {
		s.offset = line - s.height + 1
	}
	// Collapsing a table shortens the list, which can strand the offset past
	// the end of it.
	if maxOffset := total - s.height; s.offset > maxOffset {
		s.offset = maxOffset
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// window returns the rows the pane should draw. A list that already fits is
// returned whole, so a pane with room to spare never scrolls.
func (s listScroll) window(rows []string) []string {
	if s.height <= 0 || len(rows) <= s.height {
		return rows
	}
	start := min(s.offset, len(rows)-s.height)
	if start < 0 {
		start = 0
	}
	return rows[start : start+s.height]
}
