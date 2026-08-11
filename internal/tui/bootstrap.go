package tui

import (
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// bootstrap resolves the session's host/credential once. If c.Host is
// already known (flag, DB_QUERY_HOST, or config default already applied by
// the caller), it calls session.Setup directly. Otherwise it runs a
// name-only host picker to completion first — picking a name behaves
// exactly as if --host <name> had been passed; nothing is written back to
// config or the environment (design §4).
func bootstrap(c session.CommonFlags, stderr io.Writer) (session.Resolved, int) {
	if c.Host != "" {
		return session.Setup(c, stderr)
	}
	cfg, err := session.LoadConfig(c.Config)
	if err != nil {
		fmt.Fprintln(stderr, "db-query: "+err.Error())
		return session.Resolved{}, 1
	}
	names := cfg.HostNames()
	if len(names) == 0 {
		fmt.Fprintln(stderr, "db-query: no hosts configured; add one under [hosts.<name>] first")
		return session.Resolved{}, 1
	}
	p := newHostPicker(names)
	final, err := tea.NewProgram(p).Run()
	if err != nil {
		fmt.Fprintln(stderr, "db-query: "+err.Error())
		return session.Resolved{}, 1
	}
	chosen := final.(hostPicker).chosen
	if chosen == "" {
		return session.Resolved{}, 0 // user quit the picker; not an error, just nothing to do
	}
	c.Host = chosen
	return session.Setup(c, stderr)
}

// hostPicker is a minimal tea.Model listing configured host names; Enter
// selects the highlighted one, Esc quits without choosing.
type hostPicker struct {
	names  []string
	cursor int
	chosen string
}

func newHostPicker(names []string) hostPicker {
	return hostPicker{names: names}
}

func (p hostPicker) Init() tea.Cmd { return nil }

func (p hostPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := p.update(msg)
	return m, cmd
}

// update is the unexported body Update delegates to, so tests can drive it
// without going through the tea.Model interface's Model-typed return.
func (p hostPicker) update(msg tea.Msg) (hostPicker, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch keyMsg.Type {
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
	case tea.KeyDown:
		if p.cursor < len(p.names)-1 {
			p.cursor++
		}
	case tea.KeyEnter:
		p.chosen = p.names[p.cursor]
		return p, tea.Quit
	case tea.KeyEsc, tea.KeyCtrlC:
		return p, tea.Quit
	}
	return p, nil
}

func (p hostPicker) View() string {
	var b []byte
	b = append(b, "Select a host:\n\n"...)
	for i, name := range p.names {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		b = append(b, cursor+name+"\n"...)
	}
	b = append(b, "\n(enter to select, esc to quit)\n"...)
	return string(b)
}
