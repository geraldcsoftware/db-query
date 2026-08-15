package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// picker is a tea.Model wrapping a chooser for the startup flow: Enter selects
// the highlighted name, Esc quits without choosing. Choosing a host and
// choosing a database are the same interaction, so they are the same widget —
// only the intro, the heading and the list differ.
type picker struct {
	// prompt is the picker's identity rather than its display text: it is what
	// tells one startup picker from the other in a flow that may run both, and
	// the heading beside it is free to name the host it is listing without
	// changing what the flow can recognise.
	prompt  string
	heading string

	// intro is the block drawn above the heading. Only the first picker of a
	// flow carries the full explanation; a second one carries a one-line
	// confirmation of what the first chose, because Bubble Tea's inline
	// renderer leaves each finished picker on screen and repeating the block
	// would read as a stutter.
	intro []string

	chooser
	chosen string

	// width is the terminal's, once a tea.WindowSizeMsg arrives. The fallback
	// renders sanely on the narrowest terminal anyone is likely to have, and
	// the cap keeps a full-screen terminal from stretching short names across
	// a metre of whitespace.
	width int
}

// The prompt doubles as the picker's identity: it is what tells one startup
// picker from the other in a flow that may run both.
const (
	hostPrompt     = "Select a host:"
	databasePrompt = "Select a database:"
)

const (
	// pickerRows caps how tall a startup picker grows. It is inline output, so
	// every row it draws is a row of the user's scrollback it keeps after the
	// TUI exits; a long list scrolls within this window rather than printing
	// two hundred database names into the terminal's history.
	pickerRows = 10

	pickerDefaultWidth = 60
	pickerMaxWidth     = 72
)

func newHostPicker(names []string, intro []string) picker {
	return picker{
		prompt:  hostPrompt,
		heading: "Configured hosts",
		intro:   intro,
		chooser: newChooser(names, pickerRows),
		width:   pickerDefaultWidth,
	}
}

// newDatabasePicker opens the cursor on current when the host's configured
// database is among the names, so keeping it is one keystroke. marks tags the
// databases with no cached schema, so the cost of a choice is visible before it
// is made rather than at the confirmation after it.
func newDatabasePicker(names []string, current, host string, marks map[string]string, intro []string) picker {
	p := picker{
		prompt:  databasePrompt,
		heading: "Databases on " + host,
		intro:   intro,
		chooser: newChooser(names, pickerRows),
		width:   pickerDefaultWidth,
	}
	p.marks = marks
	p.cursorTo(current)
	return p
}

// startupIntro explains why the tool is asking before it asks: what build this
// is, what the invocation left unresolved, and what to do about it. missing
// names the flags that were not given, so the line reads as a description of
// this invocation rather than as boilerplate.
func startupIntro(version, missing string) []string {
	name := appNameStyle.Render("db-query")
	if version != "" {
		name += " " + appVersionStyle.Render(version)
	}
	return []string{
		name,
		introStyle.Render("No " + missing + " given, so there is no session to open yet."),
		introStyle.Render("Choose below to start one. Nothing is written back to your config."),
	}
}

// hostChosenIntro is the second picker's intro: a one-line record of what the
// first one settled, so the database list reads as the next step of one flow.
func hostChosenIntro(host string) []string {
	return []string{introLabelStyle.Render("Host") + "  " + introValueStyle.Render(host)}
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
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		p.width = min(max(size.Width, 1), pickerMaxWidth)
		return p, nil
	}
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil
	}
	switch keyMsg.String() {
	case "enter":
		// A filter matching nothing leaves no name to take; the keystroke is
		// dropped rather than quitting with an empty choice, which the caller
		// reads as "the user backed out".
		if name, ok := p.selected(); ok {
			p.chosen = name
			return p, tea.Quit
		}
		return p, nil
	case "esc", "ctrl+c":
		return p, tea.Quit
	}
	p.chooser, _ = p.chooser.update(keyMsg)
	return p, nil
}

// View leaves every terminal feature at its default: the picker is a short
// prompt printed inline, so it wants neither the alternate screen nor mouse
// reporting, both of which the main model's View turns on for itself.
func (p picker) View() tea.View {
	var b strings.Builder
	for _, line := range p.intro {
		b.WriteString(line + "\n")
	}
	if len(p.intro) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(p.headingRow() + "\n")
	if p.filter != "" {
		b.WriteString(filterPromptStyle.Render(" filter ") + filterTextStyle.Render(p.filter) + "\n")
	}
	for _, row := range p.view(p.width) {
		// Rows are padded to the full width so the selected one draws as a bar.
		// That padding is worth nothing on the rows without a bar, and this is
		// inline output the user keeps in their scrollback, so it goes — the
		// selection bar's own trailing spaces sit inside its escape sequences
		// and survive the trim.
		b.WriteString(strings.TrimRight(row, " ") + "\n")
	}
	b.WriteString("\n" + renderHints(pickerHints) + "\n")
	return tea.NewView(b.String())
}

// headingRow names the list and counts it, the count right-aligned against the
// same edge the rows end on so the two line up.
func (p picker) headingRow() string {
	head := pickerHeadingStyle.Render(p.heading)
	count := listMetaStyle.Render(p.countLabel())
	gap := p.width - ansi.StringWidth(p.heading) - ansi.StringWidth(p.countLabel()) - 1
	if gap < 1 {
		return head
	}
	return head + strings.Repeat(" ", gap) + count
}
