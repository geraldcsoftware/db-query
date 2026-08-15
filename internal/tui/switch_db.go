package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/dblist"
	"github.com/geraldcsoftware/db-query/internal/schema"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// switcherState is which of the popup's three screens is showing. They are a
// sequence, not a menu: a database with a cached schema goes straight from
// choosing to switched, and only one without stops at the other two.
type switcherState int

const (
	switcherChoosing switcherState = iota
	switcherConfirming
	switcherIntrospecting
)

const (
	// switcherRows is how many database rows the popup shows at once. It
	// floats over the panes rather than replacing them, so it stays small
	// enough to leave the session visible around it.
	switcherRows  = 8
	switcherWidth = 54
)

// dbSwitcher is the in-session database switcher: the same chooser the startup
// picker uses, wrapped in the modal states that stand between picking a name
// and the session actually moving to it.
type dbSwitcher struct {
	state switcherState
	chooser

	// pending is the database awaiting confirmation, or being introspected. It
	// is not the session's database until the switch is applied.
	pending string

	// loading is true until the live listing lands. The popup opens on the
	// cached names immediately, so this reports a refresh in progress rather
	// than an empty list.
	loading bool

	// err is the last failure, shown inside the box: a listing that could not
	// run, or an introspection that failed or was abandoned.
	err string
}

// setNames replaces the list when the live listing lands, holding the cursor on
// the name it was on. A refresh that arrives a second after the popup opened
// must not move the highlight out from under a user already reaching for Enter.
func (s *dbSwitcher) setNames(names []string, host config.HostConfig) {
	held, _ := s.selected()
	s.names = names
	s.marks = schemaMarks(host, names)
	s.cursor = 0
	s.follow()
	if held != "" {
		s.cursorTo(held)
	}
}

// databaseListMsg carries the live catalogue listing back to the popup.
type databaseListMsg struct {
	names []string
	err   error
}

// introspectDoneMsg carries the outcome of an in-session introspection. It
// names its database because the popup may have been dismissed and reopened on
// another one while it ran.
type introspectDoneMsg struct {
	database string
	err      error
}

// listDatabases runs the catalogue listing against a resolved host and
// refreshes the completion cache on the way past, exactly as the `databases`
// command does. It goes through the TUI's own ctx-aware execute rather than
// session.ListDatabases because a listing that cannot be cancelled would hold
// the popup open past the point the user gave up on it.
func listDatabases(ctx context.Context, r session.Resolved) ([]string, error) {
	rows, _, err := execute(ctx, r, r.Adapter.ListDatabasesSQL())
	if err != nil {
		return nil, err
	}
	names := session.DatabaseNames(rows)
	// A cache that cannot be written costs completion its freshness and
	// nothing else; the names in hand are still good.
	_ = dblist.Write(dblist.CachePath(r.Host.Name), names)
	return names, nil
}

// introspectInto builds the schema cache for another database on this host.
// The session is taken by value, so pointing it elsewhere for one query cannot
// disturb the session the panes are still showing.
func introspectInto(ctx context.Context, r session.Resolved, database string) error {
	r.Host.Database = database
	rows, _, err := execute(ctx, r, r.Adapter.IntrospectSQL())
	if err != nil {
		return err
	}
	return schema.Write(schema.CachePath(r.Host.Host, database), rows)
}

// openSwitcher opens the popup on the cached database names and asks for a
// fresh listing behind it. Opening on the cache is what makes F2 feel
// instant: the listing is a subprocess against a possibly-distant host, and
// waiting for it before drawing anything would make the key feel broken.
func (m model) openSwitcher() (tea.Model, tea.Cmd) {
	names, _ := dblist.Read(dblist.CachePath(m.session.Host.Name)) // absent cache is an empty list, not an error
	s := dbSwitcher{chooser: newChooser(names, switcherRows), loading: true}
	s.marks = schemaMarks(m.session.Host, names)
	s.cursorTo(m.session.Host.Database)
	m.switcher, m.switcherOpen = s, true

	r, timeout := m.session, m.flags.Timeout
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		names, err := listDatabases(ctx, r)
		return databaseListMsg{names: names, err: err}
	}
}

// updateSwitcher routes a keystroke while the popup is open. Nothing reaches
// the panes behind it — that is what makes it modal — and each state answers to
// its own small set of keys.
func (m model) updateSwitcher(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.switcher.state {
	case switcherIntrospecting:
		// Only the cancel key is live. A wait the user agreed to should not be
		// dismissable by a stray keystroke, which would leave the introspection
		// running with nothing on screen to say so.
		if msg.String() == "ctrl+c" {
			return m.cancelIntrospect()
		}
		return m, nil

	case switcherConfirming:
		switch msg.String() {
		case "enter", "y":
			return m.startIntrospect()
		case "esc", "n", "ctrl+c":
			m.switcher.state = switcherChoosing
			m.switcher.pending = ""
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "esc", "f2", "ctrl+c":
		return m.closeSwitcher(), nil
	case "enter":
		name, ok := m.switcher.selected()
		if !ok {
			return m, nil // a filter matching nothing has nothing to take
		}
		if name == m.session.Host.Database {
			// Already here. Closing without touching the session spares the
			// Results pane a pointless clear.
			return m.closeSwitcher(), nil
		}
		if needsIntrospect(m.session.Host, name) {
			m.switcher.state = switcherConfirming
			m.switcher.pending = name
			m.switcher.err = ""
			return m, nil
		}
		return m.applyDatabase(name)
	}
	m.switcher.chooser, _ = m.switcher.chooser.update(msg)
	return m, nil
}

func (m model) closeSwitcher() model {
	m.switcherOpen = false
	m.switcher = dbSwitcher{}
	return m
}

// startIntrospect builds the pending database's schema cache as a command, so
// the event loop keeps running while it does. The popup shows the wait and
// swallows keys for its duration: from the user's side it is the blocking
// operation they agreed to, but the program can still redraw, resize and be
// cancelled.
func (m model) startIntrospect() (tea.Model, tea.Cmd) {
	database := m.switcher.pending
	m.switcher.state = switcherIntrospecting
	m.switcher.err = ""

	ctx, cancel := context.WithTimeout(context.Background(), m.flags.Timeout)
	m.introspectCancel = cancel
	r := m.session
	return m, func() tea.Msg {
		return introspectDoneMsg{database: database, err: introspectInto(ctx, r, database)}
	}
}

// cancelIntrospect asks the running introspection to stop. The state change
// waits for the introspectDoneMsg that follows, since the child process takes
// a moment to die and reporting it finished before it has is a lie the next
// keystroke could catch out.
func (m model) cancelIntrospect() (tea.Model, tea.Cmd) {
	if m.introspectCancel != nil {
		m.introspectCancel()
	}
	return m, nil
}

// applyDatabase re-points the session and rebuilds everything that was scoped
// to the database it is leaving.
//
// The Schema pane is rebuilt because its cache is keyed on host+database. The
// Results pane is cleared because rows fetched from one database must never sit
// under a top bar naming another. The Query buffer deliberately survives:
// running the same statement somewhere else is the commonest reason to switch
// at all. Saved queries are not database-scoped, so they are left alone.
//
// Bumping runGen is what disowns a query still in flight against the old
// database: its result carries the old generation and is dropped on arrival.
func (m model) applyDatabase(database string) (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	// The cancelled run's own message will be discarded as stale, so it can no
	// longer be what clears this flag.
	m.running = false
	m.runGen++

	m.session.Host.Database = database
	m.schema = newSchemaPane(m.session.Host)
	m.schema.setSize(contentRows(m.rects[paneSchema]))
	m.results.clear()

	m = m.closeSwitcher()
	m.statusMsg = "switched to " + database
	m.statusGen++
	return m, clearStatusAfter(m.statusGen)
}

// view renders the popup's box. Every state draws the same frame under the same
// heading, so moving through them does not read as three unrelated dialogs.
//
// Every line is padded to exactly the body width before the border goes on,
// rather than leaving the box to size itself: a row carrying its "no schema"
// mark is already full width, and letting lipgloss wrap it would break one
// database across two lines.
func (s dbSwitcher) view(host string, width int) string {
	body := max(20, min(switcherWidth, width-8))

	lines := []string{
		pickerHeadingStyle.Render("Switch database"),
		introLabelStyle.Render("Host") + "  " + introValueStyle.Render(host),
		"",
	}

	switch s.state {
	case switcherConfirming:
		lines = append(lines,
			introValueStyle.Render(s.pending)+introStyle.Render(" has no cached schema."),
			introStyle.Render("Introspect it now? This may take a moment."),
			"",
			renderHints([]hint{{"Enter", "introspect and switch"}, {"Esc", "keep " + host}}),
		)

	case switcherIntrospecting:
		lines = append(lines,
			runningStyle.Render("introspecting "+s.pending+"…"),
			"",
			renderHints([]hint{{"^c", "cancel"}}),
		)

	default:
		switch {
		case s.loading && len(s.names) == 0:
			lines = append(lines, hintStyle.Render("listing databases…"))
		default:
			if s.filter != "" {
				lines = append(lines, filterPromptStyle.Render(" filter ")+filterTextStyle.Render(s.filter))
			}
			lines = append(lines, s.chooser.view(body)...)
		}
		if s.err != "" {
			lines = append(lines, errorStyle.Render(ansi.Truncate(s.err, body, "…")))
		}
		lines = append(lines, "",
			renderHints([]hint{{"↑/↓", "move"}, {"type", "filter"}, {"Enter", "switch"}, {"Esc", "close"}}))
	}

	for i, l := range lines {
		lines[i] = fitLine(l, body)
	}
	return switcherBoxStyle.Render(strings.Join(lines, "\n"))
}

// overlay composites the popup over the rendered screen, centred. lipgloss does
// the cell-level work, which is the point: the base screen is full of styled
// runs, and splicing a box into it by hand would mean cutting escape sequences
// at exactly the right column on every row the box covers.
//
// A Compositor rather than a Canvas: a Layer's X/Y offsets are honoured only
// when it is flattened by one, and Canvas.Compose draws every layer at the
// origin regardless of what it was offset to.
func overlay(base, box string, w, h int) string {
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(box).X(max(0, (w-bw)/2)).Y(max(0, (h-bh)/2)).Z(1),
	).Render()
}
