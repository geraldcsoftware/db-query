package tui

import (
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// queryPane wraps bubbles/textarea: the editable SQL buffer. Plain Enter
// inserts a newline (textarea's default); this pane does not intercept
// Enter combinations — running the query, if wired, belongs one level up
// in model.Update, since it would need to dispatch a tea.Cmd this pane
// has no business owning.
type queryPane struct {
	area textarea.Model
}

func newQueryPane() queryPane {
	ta := textarea.New()
	ta.Placeholder = "select ..."
	ta.Focus()
	return queryPane{area: ta}
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
