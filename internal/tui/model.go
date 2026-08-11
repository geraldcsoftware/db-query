package tui

import (
	"context"
	"errors"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/adapter"
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

	// query is the Query pane's editable SQL buffer.
	query queryPane

	// schema is the Schema pane's cached table/column browser.
	schema schemaPane

	// saved is the Saved-queries pane's cached list of stored queries.
	saved savedPane

	// running is false until a query starts executing; cancelRunning stops
	// a run without exiting the program.
	running bool

	// runner performs a single query run; it defaults to execute and is
	// swapped out in tests so run dispatch can be tested without invoking a
	// real adapter subprocess.
	runner func(ctx context.Context, r session.Resolved, sql string) (adapter.Rows, bool, error)

	// cancel stops the currently in-flight run, if any. It is set by
	// startRun and invoked by cancelRunning; nil whenever running is false.
	cancel context.CancelFunc

	// statusMsg is transient text shown in the status strip (e.g. a
	// rejected single-flight attempt or a cancellation notice). statusGen
	// increments on every new status message so a stale clearStatusMsg
	// timer from an older message cannot erase a newer one.
	statusMsg string
	statusGen int

	// results holds the last completed run's outcome for display in the
	// Results pane.
	results resultsPane

	// rects holds each pane's on-screen bounding box, set by
	// recomputeLayout. Used only by setFocusAt's hit-testing.
	rects map[pane]rect
}

type rect struct{ x0, y0, x1, y1 int }

func (r rect) contains(x, y int) bool {
	return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1
}

func newModel(r session.Resolved, c session.CommonFlags, stdout io.Writer) model {
	return model{session: r, flags: c, stdout: stdout, focus: paneSchema, query: newQueryPane(), schema: newSchemaPane(r.Host), saved: newSavedPane(), runner: execute, results: resultsPane{}}
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

// schemaRunSQL returns the provider-native preview query for the Schema
// pane's currently selected table, and whether one is selected.
func (m model) schemaRunSQL() (string, bool) {
	t, ok := m.schema.selectedTable()
	if !ok {
		return "", false
	}
	name := t.Name
	if t.Schema != "" {
		name = t.Schema + "." + t.Name
	}
	return m.session.Adapter.PreviewSQL(name), true
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
		case tea.KeyPgUp:
			m.results.pageUp()
			return m, nil
		case tea.KeyPgDown:
			m.results.pageDown()
			return m, nil
		// Ctrl+Enter's exact tea.KeyMsg encoding is not consistent across terminal
		// emulators — some report it identically to plain Enter, and Bubble Tea's
		// enhanced keyboard protocol (Kitty) is required to disambiguate on
		// terminals that support it. tea.KeyCtrlAt (NUL, 0x00) is one common
		// encoding; verify against the actual terminal(s) in use with a throwaway
		// debug print of msg.String() and adjust the matched key.Type/String()
		// value here if it differs. F5 (tea.KeyF5) is wired to the identical branch
		// specifically as the reliable fallback the spec calls for, so the run
		// action is always reachable even where Ctrl+Enter does not arrive as a
		// distinct event.
		case tea.KeyF5, tea.KeyCtrlAt:
			switch m.focus {
			case paneSchema:
				if sql, ok := m.schemaRunSQL(); ok {
					return m, m.startRun(sql)
				}
			case paneQuery:
				return m, m.startRun(m.query.value())
			}
			return m, nil
		}
		if m.focus == paneQuery {
			var cmd tea.Cmd
			m.query, cmd = m.query.update(msg)
			return m, cmd
		}
		if m.focus == paneSchema {
			var cmd tea.Cmd
			m.schema, cmd = m.schema.update(msg)
			return m, cmd
		}
		if m.focus == paneSaved {
			if msg.Type == tea.KeyEnter {
				if sq, ok := m.saved.selected(); ok {
					m.query.setValue(sq.SQL)
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.saved, cmd = m.saved.update(msg)
			return m, cmd
		}
		return m, nil

	case queryResultMsg:
		m.running = false
		m.cancel = nil
		switch {
		case errors.Is(msg.err, context.Canceled), errors.Is(msg.err, context.DeadlineExceeded):
			m.results.clear()
			m.statusMsg = "query cancelled"
			m.statusGen++
			return m, clearStatusAfter(m.statusGen)
		case msg.err != nil:
			text := msg.err.Error()
			if msg.schemaErr {
				text += "\nhint: the schema may have changed — re-run 'db-query introspect' to rebuild the schema cache"
			}
			m.results.showError(text)
			return m, nil
		default:
			m.results.showRows(msg.rows)
			return m, nil
		}

	case clearStatusMsg:
		if msg.gen == m.statusGen {
			m.statusMsg = ""
		}
		return m, nil
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

// mouseXY extracts click coordinates from a tea.MouseMsg. tea.MouseMsg is an
// alias for tea.MouseEvent, which carries X, Y directly (bubbletea v1.3.10).
func mouseXY(msg tea.MouseMsg) (x, y int) {
	return msg.X, msg.Y
}
