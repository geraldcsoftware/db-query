package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// model is the root Bubble Tea model for the interactive session. This is a
// minimal placeholder that gives tui.Run a concrete model to launch; the
// four-pane focus grid (Schema, Saved, Query, Results) is built out on top
// of this shape in a later task.
type model struct {
	session session.Resolved
	flags   session.CommonFlags
	stdout  io.Writer
}

func newModel(r session.Resolved, c session.CommonFlags, stdout io.Writer) model {
	return model{session: r, flags: c, stdout: stdout}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.Type == tea.KeyCtrlC || keyMsg.Type == tea.KeyEsc {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() string {
	return "db-query\n"
}
