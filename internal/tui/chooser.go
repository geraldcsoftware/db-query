package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// chooser is the list-selection widget behind both the startup pickers and the
// in-session database switcher: a filterable list of names drawn over a window
// that follows a cursor which cycles at both ends.
//
// The cursor indexes the *filtered* list, never names directly — every
// operation on it goes through matches(), so a filter that shortens the list
// cannot strand the cursor past the end of what is on screen.
type chooser struct {
	names []string

	// marks tags a name with a short note drawn against the right edge of its
	// row — "no schema", for a database that has never been introspected.
	// Names absent from the map render without one.
	marks map[string]string

	cursor int
	filter string
	scroll listScroll
}

// chooserCursor leads the row under the cursor. The selection bar already
// spans the row's full width, so the glyph is the redundant second cue that
// survives a terminal rendering without colour — the same belt-and-braces the
// focused pane's label row uses.
const (
	chooserCursor = "❯ "
	chooserIndent = "  "
)

func newChooser(names []string, height int) chooser {
	c := chooser{names: names}
	c.setHeight(height)
	return c
}

// setHeight records how many rows the list has to draw into and keeps the
// cursor inside them.
func (c *chooser) setHeight(h int) {
	c.scroll.setHeight(h)
	c.follow()
}

func (c *chooser) follow() { c.scroll.follow(c.cursor, len(c.matches())) }

// matches is the names the current filter admits, in the order they were
// given. Matching is a case-insensitive substring test rather than a fuzzy
// one: for lists this size an unambiguous rule beats a clever one, and a user
// who types "prod" means the names with "prod" in them, not the ones whose
// letters can be found in order.
func (c chooser) matches() []string {
	if c.filter == "" {
		return c.names
	}
	needle := strings.ToLower(c.filter)
	out := make([]string, 0, len(c.names))
	for _, n := range c.names {
		if strings.Contains(strings.ToLower(n), needle) {
			out = append(out, n)
		}
	}
	return out
}

// selected is the name under the cursor, if the filter leaves one there.
func (c chooser) selected() (string, bool) {
	m := c.matches()
	if c.cursor < 0 || c.cursor >= len(m) {
		return "", false
	}
	return m[c.cursor], true
}

// move steps the cursor by delta, wrapping at both ends. Wrapping is the list
// convention — it is what `gum choose` does — and deliberately differs from the
// pane focus grid, which clamps: an accidental extra ^h there should stay put
// rather than jump to the far side of the screen, whereas running off the
// bottom of a list means "back to the top".
func (c *chooser) move(delta int) {
	n := len(c.matches())
	if n == 0 {
		c.cursor = 0
		return
	}
	c.cursor = ((c.cursor+delta)%n + n) % n
	c.follow()
}

// page steps by a whole screen, clamping instead of wrapping: PgDn is a request
// to travel a long way down the list, and wrapping it to the top would overshoot
// the end the user was heading for.
func (c *chooser) page(delta int) {
	n := len(c.matches())
	if n == 0 {
		c.cursor = 0
		return
	}
	c.cursor = min(max(c.cursor+delta, 0), n-1)
	c.follow()
}

// setFilter replaces the filter and returns the cursor to the first match.
// Holding the cursor on whatever it was on is worse: that name may not have
// survived the filter, and a cursor landing somewhere unpredictable is harder
// to use than one that always starts at the top of what is now on screen.
func (c *chooser) setFilter(s string) {
	c.filter = s
	c.cursor = 0
	c.follow()
}

// cursorTo puts the cursor on name when the list holds it, so a chooser can
// open on the choice already in effect and keeping it is one keystroke.
func (c *chooser) cursorTo(name string) {
	for i, n := range c.matches() {
		if n == name {
			c.cursor = i
			c.follow()
			return
		}
	}
}

// update applies one keystroke's navigation or filter edit and reports whether
// it consumed the key. Selection and dismissal are deliberately left to the
// owner: the startup picker quits its program on them, the switcher popup
// commits a database switch, and those are not the same action.
func (c chooser) update(msg tea.KeyPressMsg) (chooser, bool) {
	switch msg.String() {
	case "up", "ctrl+p":
		c.move(-1)
	case "down", "ctrl+n":
		c.move(1)
	case "pgup":
		c.page(-c.scroll.height)
	case "pgdown":
		c.page(c.scroll.height)
	case "home":
		c.page(-len(c.names))
	case "end":
		c.page(len(c.names))
	case "backspace":
		if c.filter != "" {
			c.setFilter(c.filter[:len(c.filter)-1])
		}
	case "ctrl+u":
		c.setFilter("")
	default:
		// Key.Text is non-empty only for a keystroke that produced printable
		// characters, which is exactly the test for "this should go into the
		// filter": a bare letter types, ^d and F2 do not.
		if msg.Text == "" {
			return c, false
		}
		c.setFilter(c.filter + msg.Text)
	}
	return c, true
}

// view renders the visible slice of the list into rows w cells wide. An empty
// result is a row saying so rather than nothing at all, so a filter that
// matches no name reads as a filter with no matches instead of a broken list.
func (c chooser) view(w int) []string {
	m := c.matches()
	if len(m) == 0 {
		return []string{indentLines(hintStyle.Render("no matches"))}
	}
	rows := make([]string, 0, len(m))
	for i, name := range m {
		marker := chooserIndent
		if i == c.cursor {
			marker = chooserCursor
		}
		rows = append(rows, listRow(w, i == c.cursor, marker, name, c.marks[name]))
	}
	return c.scroll.window(rows)
}

// countLabel summarises the list for the heading row: the match count against
// the total once a filter narrows it, the plain total otherwise.
func (c chooser) countLabel() string {
	total := len(c.names)
	if c.filter == "" {
		return strconv.Itoa(total)
	}
	return strconv.Itoa(len(c.matches())) + "/" + strconv.Itoa(total)
}
