package tui

import (
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// queryPane wraps bubbles/textarea: the editable SQL buffer. Plain Enter
// inserts a newline (textarea's default); this pane does not intercept
// Enter combinations — running the query belongs one level up in
// model.Update, since it dispatches a tea.Cmd this pane has no business
// owning.
type queryPane struct {
	area textarea.Model
}

func newQueryPane() queryPane {
	ta := textarea.New()
	ta.Placeholder = "select ..."
	ta.ShowLineNumbers = true
	// The line-number gutter is the editor's only gutter, and it brings its own
	// padding cell on each side — the pane's indent included. A prompt glyph
	// would sit beside it as a second gutter, for nothing.
	ta.Prompt = ""
	ta.SetStyles(queryStyles())
	ta.Focus()
	return queryPane{area: ta}
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

func (q queryPane) update(msg tea.Msg) (queryPane, tea.Cmd) {
	var cmd tea.Cmd
	q.area, cmd = q.area.Update(msg)
	return q, cmd
}

func (q queryPane) value() string { return q.area.Value() }

// blurred returns a copy of the pane whose textarea shows no cursor, for
// rendering while another pane holds focus. It is a display concern only:
// which pane receives key messages is decided in model.Update.
func (q queryPane) blurred() queryPane {
	q.area.Blur()
	return q
}

// setSize fits the textarea to the space the layout gives the Query pane.
// Both dimensions are floored at 1 because a zero-sized textarea has no row
// to draw its cursor on.
func (q *queryPane) setSize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	q.area.SetWidth(w)
	q.area.SetHeight(h)
}

func (q *queryPane) setValue(v string) { q.area.SetValue(v) }

func (q queryPane) view() string { return q.area.View() }
