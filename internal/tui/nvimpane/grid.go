package nvimpane

import (
	"image/color"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// cell is one grid cell as Neovim describes it: a grapheme and a highlight id.
// The style itself is resolved at paint time from the highlight table, never
// stored per cell, so a colourscheme change costs one table rebuild rather than
// a walk over the whole grid.
type cell struct {
	text string
	hl   int
}

// hlAttr is one entry of the highlight table. A colour of -1 means "whatever
// the default is", which is how Neovim says a group does not override it.
type hlAttr struct {
	fg, bg, sp            int
	reverse, bold, italic bool
	strikethrough         bool
	underline             uv.Underline
}

// modeInfo is one entry of the cursor-shape table Neovim sends once, at attach.
// mode_change then names an index into it.
type modeInfo struct {
	shape                        string
	blinkWait, blinkOn, blinkOff int
}

// Grid is the whole ext_linegrid state. There is exactly one grid, because
// ext_multigrid is off: Neovim composites its command line, its completion
// popup and any floating window into this one before sending it.
type Grid struct {
	w, h  int
	cells []cell

	curRow, curCol int
	busy           bool

	attrs               map[int]hlAttr
	defFg, defBg, defSp int

	styles      map[int]uv.Style
	stylesValid bool

	modes   []modeInfo
	modeIdx int

	// mode is the current mode as Neovim names it: "normal", "insert",
	// "visual" and so on. The host surfaces it in the pane's label row.
	mode string

	canvas *lipgloss.Canvas
}

// NewGrid builds an empty grid of w by h cells.
func NewGrid(w, h int) *Grid {
	g := &Grid{
		attrs:  map[int]hlAttr{},
		styles: map[int]uv.Style{},
		defFg:  -1, defBg: -1, defSp: -1,
		canvas: lipgloss.NewCanvas(max(1, w), max(1, h)),
	}
	g.resize(w, h)
	return g
}

// Size is the grid's dimensions in cells, as Neovim last reported them.
func (g *Grid) Size() (int, int) { return g.w, g.h }

// Cursor is the cursor's column and row within the grid.
func (g *Grid) Cursor() (col, row int) { return g.curCol, g.curRow }

// CursorHidden reports whether Neovim has asked for the cursor to be hidden,
// which it does while it is busy.
func (g *Grid) CursorHidden() bool { return g.busy }

// Mode is the current mode's name, empty until Neovim has reported one.
func (g *Grid) Mode() string { return g.mode }

// Resize sets the size of the area the grid is painted into. It is separate
// from the cell buffer's own size, which only Neovim's grid_resize changes: the
// two disagree for the moment between the host learning its new rectangle and
// Neovim repainting at it.
func (g *Grid) Resize(w, h int) { g.canvas.Resize(max(1, w), max(1, h)) }

func (g *Grid) resize(w, h int) {
	w, h = max(1, w), max(1, h)
	next := make([]cell, w*h)
	for i := range next {
		next[i] = cell{text: " "}
	}
	// The overlap is carried over so a resize does not blank the pane in the
	// frames before Neovim has repainted it.
	for y := 0; y < min(h, g.h); y++ {
		for x := 0; x < min(w, g.w); x++ {
			next[y*w+x] = g.cells[y*g.w+x]
		}
	}
	g.w, g.h, g.cells = w, h, next
}

// Apply folds one redraw batch into the grid and reports whether it ended with
// a flush. Nothing may be painted before one: the first batch after attach is
// global setup carrying no grid content and no flush at all, and a half-applied
// batch is a torn frame.
func (g *Grid) Apply(events [][]any) (flushed bool) {
	for _, ev := range events {
		if len(ev) == 0 {
			continue
		}
		name, _ := ev[0].(string)
		calls := ev[1:]

		switch name {
		case "flush":
			flushed = true
		case "grid_resize":
			for _, c := range calls {
				if a := argsOf(c); len(a) >= 3 {
					g.resize(intOf(a[1]), intOf(a[2]))
				}
			}
		case "grid_clear":
			for i := range g.cells {
				g.cells[i] = cell{text: " "}
			}
		case "grid_line":
			for _, c := range calls {
				g.gridLine(argsOf(c))
			}
		case "grid_scroll":
			for _, c := range calls {
				g.gridScroll(argsOf(c))
			}
		case "grid_cursor_goto":
			for _, c := range calls {
				if a := argsOf(c); len(a) >= 3 {
					g.curRow, g.curCol = intOf(a[1]), intOf(a[2])
				}
			}
		case "default_colors_set":
			for _, c := range calls {
				if a := argsOf(c); len(a) >= 3 {
					g.defFg, g.defBg, g.defSp = intOf(a[0]), intOf(a[1]), intOf(a[2])
					g.stylesValid = false
				}
			}
		case "hl_attr_define":
			for _, c := range calls {
				g.hlAttrDefine(argsOf(c))
			}
		case "mode_info_set":
			for _, c := range calls {
				g.modeInfoSet(argsOf(c))
			}
		case "mode_change":
			for _, c := range calls {
				if a := argsOf(c); len(a) >= 2 {
					g.mode, g.modeIdx = stringOf(a[0]), intOf(a[1])
				}
			}
		case "busy_start":
			g.busy = true
		case "busy_stop":
			g.busy = false
		}
	}
	return flushed
}

func (g *Grid) gridLine(a []any) {
	if len(a) < 4 {
		return
	}
	row, col := intOf(a[1]), intOf(a[2])
	if row < 0 || row >= g.h {
		return
	}
	cells, _ := a[3].([]any)

	hl := 0
	for _, raw := range cells {
		c := argsOf(raw)
		if len(c) == 0 {
			continue
		}
		text := stringOf(c[0])
		// An omitted highlight id repeats the previous cell's, within this event
		// only, which is how Neovim keeps a run of same-styled cells small.
		if len(c) >= 2 {
			hl = intOf(c[1])
		}
		repeat := 1
		if len(c) >= 3 {
			repeat = intOf(c[2])
		}
		for i := 0; i < repeat && col < g.w; i++ {
			// Neovim sends "" for the right half of a double-width grapheme. The
			// left half already claims both columns, so the continuation is
			// skipped rather than painted as a blank over it.
			if text != "" {
				g.cells[row*g.w+col] = cell{text: text, hl: hl}
			}
			col++
		}
	}
}

func (g *Grid) gridScroll(a []any) {
	if len(a) < 7 {
		return
	}
	top, bot, left, right, rows := intOf(a[1]), intOf(a[2]), intOf(a[3]), intOf(a[4]), intOf(a[5])
	if rows == 0 {
		return
	}
	move := func(dst, src int) {
		if dst < 0 || dst >= g.h || src < 0 || src >= g.h {
			return
		}
		for x := left; x < right && x < g.w; x++ {
			g.cells[dst*g.w+x] = g.cells[src*g.w+x]
		}
	}
	if rows > 0 { // text moves up
		for y := top; y < bot-rows; y++ {
			move(y, y+rows)
		}
		return
	}
	for y := bot - 1; y >= top-rows; y-- { // text moves down
		move(y, y+rows)
	}
}

func (g *Grid) hlAttrDefine(a []any) {
	if len(a) < 2 {
		return
	}
	id := intOf(a[0])
	m, _ := a[1].(map[string]any)
	attr := hlAttr{fg: -1, bg: -1, sp: -1}
	for k, v := range m {
		switch k {
		case "foreground":
			attr.fg = intOf(v)
		case "background":
			attr.bg = intOf(v)
		case "special":
			attr.sp = intOf(v)
		case "reverse":
			attr.reverse = boolOf(v)
		case "bold":
			attr.bold = boolOf(v)
		case "italic":
			attr.italic = boolOf(v)
		case "strikethrough":
			attr.strikethrough = boolOf(v)
		case "underline":
			attr.underline = uv.UnderlineSingle
		case "undercurl":
			attr.underline = uv.UnderlineCurly
		case "underdouble":
			attr.underline = uv.UnderlineDouble
		case "underdotted":
			attr.underline = uv.UnderlineDotted
		case "underdashed":
			attr.underline = uv.UnderlineDashed
		}
	}
	g.attrs[id] = attr
	g.stylesValid = false
}

func (g *Grid) modeInfoSet(a []any) {
	if len(a) < 2 {
		return
	}
	list, _ := a[1].([]any)
	g.modes = g.modes[:0]
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		mi := modeInfo{shape: "block"}
		if s, ok := m["cursor_shape"].(string); ok {
			mi.shape = s
		}
		mi.blinkWait, mi.blinkOn, mi.blinkOff = intOf(m["blinkwait"]), intOf(m["blinkon"]), intOf(m["blinkoff"])
		g.modes = append(g.modes, mi)
	}
}

func (g *Grid) rebuildStyles() {
	clear(g.styles)
	for id, a := range g.attrs {
		g.styles[id] = g.resolve(a)
	}
	g.styles[0] = g.resolve(hlAttr{fg: -1, bg: -1, sp: -1})
	g.stylesValid = true
}

// resolve turns one highlight entry into the style a cell carrying it is
// painted with.
//
// A background equal to Neovim's own default is dropped rather than painted, so
// the pane sits on the terminal's background like every other pane in the TUI
// instead of becoming a solid block beside three that are not. Backgrounds that
// differ are still painted, which is what keeps the completion popup opaque and
// reading as though it floats.
func (g *Grid) resolve(a hlAttr) uv.Style {
	fg, bg := a.fg, a.bg
	if fg < 0 {
		fg = g.defFg
	}
	if bg < 0 {
		bg = g.defBg
	}
	if a.reverse {
		fg, bg = bg, fg
	}

	var s uv.Style
	if fg >= 0 {
		s.Fg = rgb(fg)
	}
	if bg >= 0 && !(bg == g.defBg && !a.reverse) {
		s.Bg = rgb(bg)
	}
	if a.sp >= 0 {
		s.UnderlineColor = rgb(a.sp)
	}
	s.Underline = a.underline
	if a.bold {
		s.Attrs |= uv.AttrBold
	}
	if a.italic {
		s.Attrs |= uv.AttrItalic
	}
	if a.strikethrough {
		s.Attrs |= uv.AttrStrikethrough
	}
	return s
}

// Render paints the grid and returns it as one styled block, one line per row
// of the area Resize was last given. A single scratch cell is reused for every
// cell of every frame: SetCell copies by value, so painting a frame allocates
// nothing per cell.
func (g *Grid) Render() string {
	if !g.stylesValid {
		g.rebuildStyles()
	}
	var scratch uv.Cell
	cw, ch := g.canvas.Width(), g.canvas.Height()
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			scratch.Content = " "
			scratch.Width = 1
			scratch.Style = g.styles[0]
			if y < g.h && x < g.w {
				c := g.cells[y*g.w+x]
				if c.text != "" {
					scratch.Content = c.text
				}
				scratch.Style = g.styles[c.hl]
			}
			g.canvas.SetCell(x, y, &scratch)
		}
	}
	return g.canvas.Render()
}

// CursorShape is the current mode's cursor shape, as Neovim names it.
func (g *Grid) CursorShape() string {
	if g.modeIdx < 0 || g.modeIdx >= len(g.modes) {
		return "block"
	}
	return g.modes[g.modeIdx].shape
}

// CursorBlinks reports whether the current mode's cursor blinks. All three
// timings have to be non-zero; Neovim's own default is a steady cursor.
func (g *Grid) CursorBlinks() bool {
	if g.modeIdx < 0 || g.modeIdx >= len(g.modes) {
		return false
	}
	m := g.modes[g.modeIdx]
	return m.blinkWait != 0 && m.blinkOn != 0 && m.blinkOff != 0
}

func rgb(v int) color.Color {
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xFF}
}

func argsOf(v any) []any {
	a, _ := v.([]any)
	return a
}

func intOf(v any) int {
	n, ok := toInt(v)
	if !ok {
		return -1
	}
	return n
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}
