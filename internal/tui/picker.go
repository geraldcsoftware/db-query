package tui

import (
	tea "charm.land/bubbletea/v2"
)

// picker is a minimal tea.Model listing names under a prompt; Enter selects
// the highlighted one, Esc quits without choosing. Choosing a host and
// choosing a database are the same interaction, so they are the same widget:
// only the prompt and the list differ.
type picker struct {
	prompt string
	names  []string
	cursor int
	chosen string
}

// The prompt doubles as the picker's identity: it is what tells one startup
// picker from the other in a flow that may run both.
const (
	hostPrompt     = "Select a host:"
	databasePrompt = "Select a database:"
)

func newHostPicker(names []string) picker {
	return picker{prompt: hostPrompt, names: names}
}

// newDatabasePicker starts the cursor on current when the host's configured
// database is among the names, so keeping it is one keystroke.
func newDatabasePicker(names []string, current string) picker {
	p := picker{prompt: databasePrompt, names: names}
	for i, name := range names {
		if name == current {
			p.cursor = i
			break
		}
	}
	return p
}

// runPicker drives a picker to completion in a Bubble Tea program of its own
// and returns the finished model. It is a variable so tests can exercise the
// startup flow's branching without a terminal for a program to attach to.
var runPicker = func(p picker) (picker, error) {
	final, err := tea.NewProgram(p).Run()
	if err != nil {
		return picker{}, err
	}
	return final.(picker), nil
}

func (p picker) Init() tea.Cmd { return nil }

func (p picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := p.update(msg)
	return m, cmd
}

// update is the unexported body Update delegates to, so tests can drive it
// without going through the tea.Model interface's Model-typed return.
func (p picker) update(msg tea.Msg) (picker, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil
	}
	switch keyMsg.String() {
	case "up":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down":
		if p.cursor < len(p.names)-1 {
			p.cursor++
		}
	case "enter":
		p.chosen = p.names[p.cursor]
		return p, tea.Quit
	case "esc", "ctrl+c":
		return p, tea.Quit
	}
	return p, nil
}

// View leaves every terminal feature at its default: the picker is a short
// prompt printed inline, so it wants neither the alternate screen nor mouse
// reporting, both of which the main model's View turns on for itself.
func (p picker) View() tea.View {
	var b []byte
	b = append(b, p.prompt+"\n\n"...)
	for i, name := range p.names {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		b = append(b, cursor+name+"\n"...)
	}
	b = append(b, "\n(enter to select, esc to quit)\n"...)
	return tea.NewView(string(b))
}
