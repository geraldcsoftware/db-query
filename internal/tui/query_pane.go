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

func (q *queryPane) setValue(v string) { q.area.SetValue(v) }

func (q queryPane) view() string { return q.area.View() }
