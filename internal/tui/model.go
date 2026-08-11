package tui

import (
	"context"
	"errors"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

	// version is the build's version string, shown in the top bar. It is
	// passed in from the caller because internal/cli imports this package
	// and so cannot be imported back.
	version string

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

func newModel(r session.Resolved, c session.CommonFlags, version string, stdout io.Writer) model {
	m := model{session: r, flags: c, version: version, stdout: stdout, focus: paneSchema, query: newQueryPane(), schema: newSchemaPane(r.Host), saved: newSavedPane(), runner: execute, results: resultsPane{}}
	// Lay out against viewSize's fallback dimensions so rects and the
	// textarea are already consistent with the first frame, which may render
	// before the initial tea.WindowSizeMsg arrives.
	m.recomputeLayout()
	return m
}

func (m model) Init() tea.Cmd { return nil }

// viewSize is the terminal size to lay out against: the dimensions from the
// last tea.WindowSizeMsg, falling back to a conservative 80x24 before the
// first one arrives so an early frame still renders something sane.
func (m model) viewSize() (w, h int) {
	w, h = m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

// recomputeLayout rebuilds the pane rectangles for the current terminal size
// and resizes the Query pane's textarea to fit its own rectangle.
func (m *model) recomputeLayout() {
	w, h := m.viewSize()
	m.rects = layoutRects(w, h)
	r := m.rects[paneQuery]
	// The textarea gets the pane's full width and every row below its label.
	m.query.setSize(max(0, r.x1-r.x0), max(0, r.y1-r.y0-1))
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

	case tea.MouseClickMsg:
		// View enables cell-motion reporting, so motion, wheel and release
		// events arrive too — as their own message types, which this case does
		// not match. Of the clicks that do reach here, only the left button
		// moves focus.
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		m.setFocusAt(msg.X, msg.Y)
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return m, m.quit()
		case "ctrl+c":
			if m.running {
				return m.cancelRunning()
			}
			return m, m.quit()
		case "ctrl+h":
			m.focusLeft()
			return m, nil
		case "ctrl+l":
			m.focusRight()
			return m, nil
		case "ctrl+k":
			m.focusUp()
			return m, nil
		case "ctrl+j":
			m.focusDown()
			return m, nil
		case "pgup":
			m.results.pageUp()
			return m, nil
		case "pgdown":
			m.results.pageDown()
			return m, nil
		// Ctrl+Enter and F5 are one action deliberately reachable two ways. A
		// terminal's legacy key encoding sends CR for plain Enter and for
		// Ctrl+Enter alike, so the chord is only a distinct event where the
		// Kitty keyboard protocol is negotiated — which View requests, and which
		// not every terminal answers. F5 is the fallback that keeps the run
		// action reachable on the ones that do not (spec §7).
		case "ctrl+enter", "f5":
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
			if msg.String() == "enter" {
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

	case tea.PasteMsg:
		// Bracketed paste is on by default and arrives as its own message
		// rather than as a key press, so it needs routing by hand. The Query
		// pane is the only pane that takes text, so a paste anywhere else is
		// dropped rather than silently landing in the buffer.
		if m.focus == paneQuery {
			var cmd tea.Cmd
			m.query, cmd = m.query.update(msg)
			return m, cmd
		}
		return m, nil

	case queryResultMsg:
		m.running = false
		// Invoking the CancelFunc, rather than only dropping it, releases the
		// context.WithTimeout timer startRun armed; otherwise it stays alive
		// until the full --timeout elapses.
		if m.cancel != nil {
			m.cancel()
		}
		m.cancel = nil
		switch {
		case errors.Is(msg.err, context.Canceled):
			// The user asked for this, so it is not an error: leave the
			// Results pane empty and say so in the status strip (spec §8).
			m.results.clear()
			m.statusMsg = "query cancelled"
			m.statusGen++
			return m, clearStatusAfter(m.statusGen)
		case errors.Is(msg.err, context.DeadlineExceeded):
			// A --timeout expiry is a genuine failure nobody asked for, so it
			// belongs on the error path (spec §9), not the cancellation one.
			m.results.showError("query timed out after " + m.flags.Timeout.String())
			return m, nil
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

// quit cancels any in-flight run before asking Bubble Tea to exit. The held
// CancelFunc is the only path to the adapter's child process (started with
// exec.CommandContext), so skipping it would leave a slow psql/sqlcmd running
// after the TUI is gone.
func (m *model) quit() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	return tea.Quit
}

// View renders one full screen: a top bar, a body of two columns separated by
// rules, and a bottom bar. Its content is exactly the terminal's height in
// lines and no line exceeds its width — Bubble Tea's renderer resolves an
// over-tall view by keeping only its last height lines, which would silently
// push the top bar and the upper panes off screen on the first large result.
func (m model) View() tea.View {
	w, h := m.viewSize()
	lay := computeLayout(w, h)

	rows := make([]string, h)
	rows[0] = fitLine(m.topBar(w), w)
	if h >= 2 {
		rows[h-1] = fitLine(m.bottomBar(w), w)
	}
	// A body needs both bars, both full-width rules, and a row of its own; any
	// shorter and the screen is the two bars alone.
	if lay.bodyH > 0 {
		rows[1] = fullRule(w, lay.ruleX, ruleTeeDown)
		rows[h-2] = fullRule(w, lay.ruleX, ruleTeeUp)
		copy(rows[lay.bodyTop:], m.bodyRows(lay))
	}
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	return screen(strings.Join(rows, "\n"))
}

// bodyRows renders the rows between the two full-width rules: the sidebar
// column, the vertical rule carrying whichever junction the row calls for, and
// the main column. Each column is built as its own block of fixed-width lines,
// so concatenating them keeps the main column aligned.
func (m model) bodyRows(lay layout) []string {
	main := m.mainColumn(lay)
	if lay.ruleX < 0 {
		return main
	}
	sidebar := m.sidebarColumn(lay)
	out := make([]string, lay.bodyH)
	for i := range out {
		y := lay.bodyTop + i
		junction := junctionAt(y == lay.sidebarRuleY, y == lay.mainRuleY)
		out[i] = sidebar[i] + ruleStyle.Render(junction) + main[i]
	}
	return out
}

// sidebarColumn stacks Schema over Saved, divided by the sidebar's own rule.
func (m model) sidebarColumn(lay layout) []string {
	r := lay.rects[paneSchema]
	out := m.paneBlock(paneSchema, "SCHEMA", "", m.schema.view(r.x1-r.x0), r)
	if lay.sidebarRuleY >= 0 {
		out = append(out, hRule(r.x1-r.x0))
	}
	saved := lay.rects[paneSaved]
	return append(out, m.paneBlock(paneSaved, "SAVED", "", m.saved.view(saved.x1-saved.x0), saved)...)
}

// mainColumn stacks Query over Results, divided by the main column's rule.
func (m model) mainColumn(lay layout) []string {
	// The Query pane's textarea keeps a visible cursor only while it is the
	// focused pane; key routing is gated separately in Update.
	q := m.query
	if m.focus != paneQuery {
		q = q.blurred()
	}
	r := lay.rects[paneQuery]
	out := m.paneBlock(paneQuery, "QUERY", "", q.view(), r)
	if lay.mainRuleY >= 0 {
		out = append(out, hRule(r.x1-r.x0))
	}
	results := lay.rects[paneResults]
	return append(out, m.paneBlock(paneResults, "RESULTS", m.results.meta(), m.results.view(), results)...)
}

// screen pairs rendered content with the terminal features the TUI runs on:
// the alternate screen, and cell-motion mouse reporting so a click can pick a
// pane. Bubble Tea reads both off the view it is handed each frame, which is
// why they live here rather than as options on the program.
func screen(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// KeyboardEnhancements is deliberately left at its zero value. Bubble Tea
	// always asks the terminal for the Kitty protocol's key disambiguation,
	// which is what reports Ctrl+Enter as a key of its own rather than as the
	// bare CR that plain Enter also sends; the field only adds enhancements on
	// top of that, and nothing here wants key release events or alternate key
	// codes.
	return v
}

// paneBlock renders one pane as exactly as many lines as its rectangle is
// tall, each exactly its rectangle's width: a label row, then the pane's own
// content clipped to the rows below it. Content longer than the rectangle is
// cut off at the bottom — there is no per-pane scrolling, so a result page
// taller than the Results pane is paged through with PgUp/PgDn or shrunk with
// DB_QUERY_TUI_PAGE_SIZE.
//
// meta is the pane's own summary, right-aligned on the label row; panes with
// nothing to summarise pass an empty string.
func (m model) paneBlock(p pane, label, meta, content string, r rect) []string {
	w, h := r.x1-r.x0, r.y1-r.y0
	if w <= 0 || h <= 0 {
		return nil
	}
	lines := append([]string{paneLabelRow(label, meta, w, p == m.focus)},
		strings.Split(strings.TrimRight(content, "\n"), "\n")...)
	return fitBlock(lines, w, h)
}

// paneLabelRow draws a pane's heading: the focus marker and the uppercase
// section label, with the pane's summary right-aligned against its far edge.
// The marker is what carries focus through ansi.Strip, so a terminal that
// cannot show colour still says which pane the next keystroke reaches; the
// accent colour is the second, redundant cue.
func paneLabelRow(label, meta string, w int, focused bool) string {
	marker, style := " ", paneLabelStyle
	if focused {
		marker, style = focusMarker, paneLabelFocusedStyle
	}
	head := style.Render(marker + label)
	gap := w - ansi.StringWidth(marker+label) - ansi.StringWidth(meta) - 1
	if meta == "" || gap < 1 {
		return fitLine(head, w)
	}
	return head + strings.Repeat(" ", gap) + paneMetaStyle.Render(meta) + " "
}

// fitLine forces one line to exactly w cells. Truncation is ANSI-aware so
// clipping a styled line (the textarea's, for one) cannot cut an escape
// sequence in half and bleed its colour across the rest of the screen.
func fitLine(s string, w int) string {
	s = ansi.Truncate(s, w, "")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// fitBlock forces lines into a block of exactly h rows of exactly w cells.
func fitBlock(lines []string, w, h int) []string {
	out := make([]string, h)
	for i := range out {
		var s string
		if i < len(lines) {
			s = lines[i]
		}
		out[i] = fitLine(s, w)
	}
	return out
}

// indentLines shifts a block one cell right, matching the indent the panes
// give their own rows.
func indentLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = " " + l
	}
	return strings.Join(lines, "\n")
}

// topBar identifies the build and the resolved connection so both stay visible
// regardless of which pane has focus. The connection is right-aligned against
// the terminal's width and leads with the database, the field a user re-reads
// before running something destructive.
func (m model) topBar(w int) string {
	left := appNameStyle.Render("db-query")
	if m.version != "" {
		left += " " + appVersionStyle.Render(m.version)
	}
	right := databaseStyle.Render(m.session.Host.Database+" ●") + " " +
		connectionStyle.Render(m.session.Host.Provider) +
		connectionSep.Render(" · ") +
		connectionStyle.Render(m.session.Host.Host)
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		// The connection yields to the app name, which is clipped last.
		return left + " " + ansi.Truncate(right, max(0, w-ansi.StringWidth(left)-1), "")
	}
	return left + strings.Repeat(" ", gap) + right
}

// bottomBar shows a transient status message when one is set, a running
// indicator while a query is in flight (without which an in-flight run is
// indistinguishable from an idle screen with empty results), and otherwise
// the keybinding hint.
func (m model) bottomBar(w int) string {
	switch {
	case m.statusMsg != "":
		return statusStyle.Render(m.statusMsg)
	case m.running:
		return runningStyle.Render("running…") + hintSepStyle.Render(" · ") +
			hintKeyStyle.Render("^c") + " " + hintDescStyle.Render("cancel")
	default:
		return bottomBarHint(w)
	}
}
