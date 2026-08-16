package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/schema"
	"github.com/geraldcsoftware/db-query/internal/tui/nvimpane"
)

// queryEditor is the Query pane's editable SQL buffer. There are two
// implementations: an embedded Neovim instance, and the textarea db-query falls
// back to on a machine with no usable Neovim. Everything that differs between
// them is behind this interface, so model.Update and model.View route to the
// pane without asking which one is there.
//
// Both implementations are pointer types held by interface value, so the model
// copies Bubble Tea makes on every Update all share one editor.
type queryEditor interface {
	// start hands the editor a way to push messages into the event loop from
	// goroutines of its own. It is called once, after the program exists and
	// before it runs, since nothing may be sent into a program that does not.
	start(send func(tea.Msg))

	// update feeds the editor one message routed to it: a key press, a paste,
	// or a message the editor itself sent through start's callback.
	update(msg tea.Msg) tea.Cmd

	// setSize fits the editor to the room the layout gives the Query pane, in
	// cells, excluding the pane's own label row.
	setSize(w, h int)

	setValue(v string)

	// runText asks the editor for the SQL a run should execute: the visual
	// selection where one is live, otherwise the whole buffer. The answer
	// arrives as a queryTextMsg, because an editor that has to ask another
	// process cannot answer on the event loop's own goroutine.
	runText() tea.Cmd

	// setSchema hands the editor the catalogue completion should offer. It is
	// called at startup and again whenever a database switch rebuilds it; an
	// editor without completion ignores it.
	setSchema(tables []schema.Table)

	// view renders the editor's content, one line per row of the size it was
	// last given. focused is a display concern only: which pane receives key
	// messages is decided in model.Update.
	view(focused bool) string

	// meta is the editor's summary for the right of the pane's label row, empty
	// when it has nothing to say.
	meta() string

	// modal reports whether the editor has modes of its own, and so needs the
	// keys the host would otherwise reserve for itself: Esc above all, which
	// leaves a mode rather than the program, and PgUp and PgDown, which scroll
	// the buffer. A plain editor has no use for any of them, and keeping them
	// with the host is what makes the fallback exactly the pane db-query
	// shipped before.
	modal() bool

	// cursor places the terminal's real cursor, given the screen coordinates of
	// the editor's top-left cell. A nil return leaves the cursor alone, which is
	// what an editor drawing its own cursor into its content wants.
	cursor(x0, y0 int) *tea.Cursor

	// keepsTrailingCells reports whether the editor's rows carry paint in their
	// trailing spaces. A frame's rows are otherwise trimmed of trailing spaces
	// before being drawn, which would strip that paint away with them.
	keepsTrailingCells() bool

	// close releases whatever the editor owns. Calling it twice is safe.
	close()
}

// newQueryEditor builds the Query pane's editor: an embedded Neovim wherever
// one can run, and the textarea everywhere else.
//
// The fallback is permanent for the session and silent. A machine with no nvim
// on PATH, or one below the version floor, gets exactly the pane db-query
// shipped before, with nothing to configure and nothing to read.
//
// The second return is a notice for the status strip, empty when there is
// nothing to say.
func newQueryEditor() (queryEditor, string) {
	ed, err := newNvimEditor()
	if err != nil {
		return newTextareaEditor(), ""
	}
	if !nvimpane.HasConfig() {
		return ed, "no " + nvimpane.ConfigPath() + ": the Query editor is running on Neovim's own defaults"
	}
	return ed, ""
}
