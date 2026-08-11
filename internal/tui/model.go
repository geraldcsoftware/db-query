package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// pane identifies one of the four fixed panes in the 2x2 focus grid:
//
//	Schema  | Query
//	Saved   | Results
type pane int

const (
	paneSchema pane = iota
	paneSaved
	paneQuery
	paneResults
)

// model is the root Bubble Tea model. It owns the session (resolved once at
// startup, per design §4), the focus grid, and each pane's layout rectangle
// (recomputed on tea.WindowSizeMsg and stored so mouse clicks can be
// hit-tested against it without recomputing on every click).
type model struct {
	session session.Resolved
	flags   session.CommonFlags
	stdout  io.Writer

	focus  pane
	width  int
	height int

	// running is false until a query starts executing; cancelRunning stops
	// a run without exiting the program. This task's tests never trigger a
	// run, so running stays at its zero value throughout.
	running bool

	// rects holds each pane's on-screen bounding box, set by
	// recomputeLayout. Used only by setFocusAt's hit-testing.
	rects map[pane]rect
}

type rect struct{ x0, y0, x1, y1 int }

func (r rect) contains(x, y int) bool {
	return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1
}

func newModel(r session.Resolved, c session.CommonFlags, stdout io.Writer) model {
	return model{session: r, flags: c, stdout: stdout, focus: paneSchema}
}

func (m model) Init() tea.Cmd { return nil }

// recomputeLayout splits the terminal into a 2x2 grid: Schema/Saved stacked
// on the left half, Query/Results stacked on the right half — matching the
// spec's §5 layout. Called on tea.WindowSizeMsg; no-op (v1 does not reflow
// mid-session, spec §2) if called again with the same dimensions, but is
// always safe to call.
func (m *model) recomputeLayout() {
	leftW, rightW := m.width/2, m.width-m.width/2
	topH, botH := m.height/2, m.height-m.height/2
	m.rects = map[pane]rect{
		paneSchema:  {0, 0, leftW, topH},
		paneSaved:   {0, topH, leftW, topH + botH},
		paneQuery:   {leftW, 0, leftW + rightW, topH},
		paneResults: {leftW, topH, leftW + rightW, topH + botH},
	}
}

// setFocusAt sets focus to whichever pane's rectangle contains (x, y), the
// coordinates of a mouse click. No-op if the click lands outside every
// rectangle (should not happen once rects tile the full window, but a
// resize race is possible before the first recomputeLayout).
func (m *model) setFocusAt(x, y int) {
	for p, r := range m.rects {
		if r.contains(x, y) {
			m.focus = p
			return
		}
	}
}

// focusLeft/Right/Up/Down move focus one step in the 2x2 grid, clamping at
// the edge rather than wrapping — an accidental extra ctrl+h at the
// leftmost column stays put instead of jumping to the far side.
func (m *model) focusLeft() {
	if m.focus == paneQuery {
		m.focus = paneSchema
	} else if m.focus == paneResults {
		m.focus = paneSaved
	}
}

func (m *model) focusRight() {
	if m.focus == paneSchema {
		m.focus = paneQuery
	} else if m.focus == paneSaved {
		m.focus = paneResults
	}
}

func (m *model) focusUp() {
	if m.focus == paneSaved {
		m.focus = paneSchema
	} else if m.focus == paneResults {
		m.focus = paneQuery
	}
}

func (m *model) focusDown() {
	if m.focus == paneSchema {
		m.focus = paneSaved
	} else if m.focus == paneQuery {
		m.focus = paneResults
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.recomputeLayout()
		return m, nil

	case tea.MouseMsg:
		x, y := mouseXY(msg)
		m.setFocusAt(x, y)
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyCtrlC:
			if m.running {
				return m.cancelRunning()
			}
			return m, tea.Quit
		case tea.KeyCtrlH:
			m.focusLeft()
			return m, nil
		case tea.KeyCtrlL:
			m.focusRight()
			return m, nil
		case tea.KeyCtrlK:
			m.focusUp()
			return m, nil
		case tea.KeyCtrlJ:
			m.focusDown()
			return m, nil
		}
	}
	return m, nil
}

func (m model) View() string {
	label := func(p pane, title string) string {
		mark := "  "
		if m.focus == p {
			mark = "> "
		}
		return mark + "[" + title + "]"
	}
	return label(paneSchema, "Schema") + "  " + label(paneQuery, "Query") + "\n" +
		label(paneSaved, "Saved") + "  " + label(paneResults, "Results") + "\n"
}

// cancelRunning stops the in-flight run, if any, without quitting the
// program. The real implementation lives in run.go.
func (m model) cancelRunning() (tea.Model, tea.Cmd) {
	m.running = false
	return m, nil
}

// mouseXY extracts click coordinates from a tea.MouseMsg. tea.MouseMsg is an
// alias for tea.MouseEvent, which carries X, Y directly (bubbletea v1.3.10).
func mouseXY(msg tea.MouseMsg) (x, y int) {
	return msg.X, msg.Y
}
