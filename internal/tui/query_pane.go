package tui

import (
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// textareaEditor is the fallback Query editor: a bubbles textarea, with no vim
// mode, no highlighting and no completion. It is what db-query used before the
// embedded editor existed and what it still uses wherever Neovim cannot run.
//
// Plain Enter inserts a newline, the textarea's own default; running the query
// belongs one level up in model.Update, which dispatches a tea.Cmd this editor
// has no business owning.
type textareaEditor struct {
	area textarea.Model
}

func newTextareaEditor() *textareaEditor {
	ta := textarea.New()
	ta.Placeholder = "select ..."
	ta.ShowLineNumbers = true
	// The line-number gutter is the editor's only gutter, and it brings its own
	// padding cell on each side — the pane's indent included. A prompt glyph
	// would sit beside it as a second gutter, for nothing.
	ta.Prompt = ""
	ta.SetStyles(queryStyles())
	ta.Focus()
	return &textareaEditor{area: ta}
}

// queryStyles recolours the textarea into the pane palette: line numbers as
// muted chrome, SQL as body text. The cursor line is left unhighlighted — the
// cursor itself already marks it, and a highlight bar would compete with the
// selection bars the sidebar uses to mean something else.
func queryStyles() textarea.Styles {
	s := textarea.DefaultDarkStyles()
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.Base = lipgloss.NewStyle()
		st.Text = lipgloss.NewStyle().Foreground(colorText)
		st.Prompt = lipgloss.NewStyle()
		st.LineNumber = lipgloss.NewStyle().Foreground(colorMuted)
		st.CursorLineNumber = lipgloss.NewStyle().Foreground(colorMuted)
		st.CursorLine = lipgloss.NewStyle().Foreground(colorText)
		st.EndOfBuffer = lipgloss.NewStyle().Foreground(colorRule)
		st.Placeholder = lipgloss.NewStyle().Foreground(colorMuted)
	}
	s.Cursor.Color = colorAccent
	return s
}

// start takes no callback: everything the textarea does happens on the event
// loop's own goroutine.
func (q *textareaEditor) start(func(tea.Msg)) {}

func (q *textareaEditor) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	q.area, cmd = q.area.Update(msg)
	return cmd
}

func (q *textareaEditor) value() string     { return q.area.Value() }
func (q *textareaEditor) setValue(v string) { q.area.SetValue(v) }

// setSize fits the textarea to the space the layout gives the Query pane.
// Both dimensions are floored at 1 because a zero-sized textarea has no row
// to draw its cursor on.
func (q *textareaEditor) setSize(w, h int) {
	q.area.SetWidth(max(1, w))
	q.area.SetHeight(max(1, h))
}

// view draws the buffer. Blurring hides the cursor while another pane holds
// focus, which is the only thing focus changes here.
func (q *textareaEditor) view(focused bool) string {
	if focused {
		q.area.Focus()
	} else {
		q.area.Blur()
	}
	return q.area.View()
}

// meta is empty: a plain textarea has no mode and nothing else worth a summary.
func (q *textareaEditor) meta() string { return "" }

// modal is false: the textarea has no modes, so Esc, Ctrl+C, PgUp and PgDown
// all keep the meanings the rest of the TUI gives them.
func (q *textareaEditor) modal() bool { return false }

// cursor is nil because the textarea draws its own cursor into the content it
// renders rather than asking the host to place the terminal's.
func (q *textareaEditor) cursor(int, int) *tea.Cursor { return nil }

// keepsTrailingCells is false: the textarea paints foregrounds only, so a
// trailing run of spaces carries nothing and trimming it costs nothing.
func (q *textareaEditor) keepsTrailingCells() bool { return false }

func (q *textareaEditor) close() {}
