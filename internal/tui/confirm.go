package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// confirm is a yes/no question printed inline, in the same visual language as
// the pickers it sits between. It is a Bubble Tea program of its own rather
// than a line read off stdin: the picker before it has just put the terminal
// back the way it found it, and going through the same event loop keeps key
// handling (Esc, ^c) identical either side of the question.
type confirm struct {
	// lines is the question, already styled, one entry per rendered row.
	lines []string
	hints []hint

	// yes records the answer. A confirm that was never answered — the program
	// failed, or the user pressed Esc — leaves it false, so declining is what
	// every path that is not an explicit yes resolves to.
	yes bool
}

// runConfirm drives a confirm to completion in its own program. It is a
// variable so tests can answer the question without a terminal, exactly as
// runPicker does for the pickers around it.
var runConfirm = func(c confirm) (confirm, error) {
	final, err := tea.NewProgram(c).Run()
	if err != nil {
		return confirm{}, err
	}
	return final.(confirm), nil
}

// newIntrospectConfirm asks whether to build the schema cache for a database
// that has none. Declining does not switch: the session's rule is that any
// database it lands on has a schema to browse, so the alternative to
// introspecting is choosing something else, not arriving without one.
func newIntrospectConfirm(database, decline string) confirm {
	return confirm{
		lines: []string{
			introValueStyle.Render(database) + introStyle.Render(" has no cached schema."),
			introStyle.Render("Introspect it now? This may take a moment on a large database."),
		},
		hints: []hint{
			{"Enter", "introspect and switch"},
			{"Esc", decline},
		},
	}
}

func (c confirm) Init() tea.Cmd { return nil }

func (c confirm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := c.update(msg)
	return m, cmd
}

// update is the unexported body Update delegates to, so tests can drive it
// without going through the tea.Model interface's Model-typed return.
func (c confirm) update(msg tea.Msg) (confirm, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return c, nil
	}
	switch keyMsg.String() {
	case "enter", "y":
		c.yes = true
		return c, tea.Quit
	case "esc", "n", "ctrl+c":
		return c, tea.Quit
	}
	return c, nil
}

func (c confirm) View() tea.View {
	var b strings.Builder
	for _, line := range c.lines {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + renderHints(c.hints) + "\n")
	return tea.NewView(b.String())
}
