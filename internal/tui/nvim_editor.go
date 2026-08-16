package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/neovim/go-client/nvim"

	"github.com/geraldcsoftware/db-query/internal/tui/nvimpane"
)

// nvimRedrawMsg carries one redraw batch from the goroutine that owns the RPC
// stream into the event loop, where it is applied on the same goroutine as
// every other change to the pane.
type nvimRedrawMsg nvimpane.Batch

// nvimEndedMsg reports that the embedded editor's Neovim has gone. A nil error
// means db-query asked it to; anything else took the pane with it.
type nvimEndedMsg struct{ err error }

// nvimEditor is the Query pane backed by an embedded Neovim: a child process
// driven over MessagePack RPC, whose screen arrives as ext_linegrid events and
// is painted into the pane's own rectangle.
type nvimEditor struct {
	sess *nvimpane.Session
	grid *nvimpane.Grid

	// frame is the last painted grid, rebuilt only when a batch ends in a flush
	// or the pane is resized. Neovim sends a batch in pieces, so painting before
	// the flush that closes it would show a half-drawn screen.
	frame string
}

// newNvimEditor spawns the child process and brings the pane up. Every failure
// — no binary, too old a binary, a refused attach — is the caller's cue to fall
// back to the textarea for the rest of the session.
func newNvimEditor() (*nvimEditor, error) {
	const cols, rows = 80, 24 // corrected by the first setSize, which follows at once

	sess, err := nvimpane.Start(nvimpane.Options{
		Cols: cols, Rows: rows,
		Candidates: sqlKeywords{},
	})
	if err != nil {
		return nil, err
	}
	return &nvimEditor{sess: sess, grid: nvimpane.NewGrid(cols, rows)}, nil
}

// start hands the redraw stream and the end-of-session signal to goroutines of
// their own, each pushing into the event loop.
//
// The send is deliberately unbuffered all the way through, and the redraw
// goroutine blocks while it is in flight. That backpressure is the design: a
// resize storm reaches Neovim's own coalescing rather than the host discarding
// batches it has already been given.
func (e *nvimEditor) start(send func(tea.Msg)) {
	go func() {
		for {
			select {
			case b := <-e.sess.Redraw:
				send(nvimRedrawMsg(b))
			case <-e.sess.Done():
				return
			}
		}
	}()
	go func() {
		select {
		case err := <-e.sess.Ended:
			send(nvimEndedMsg{err: err})
		case <-e.sess.Done():
		}
	}()
}

func (e *nvimEditor) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case nvimRedrawMsg:
		if e.grid.Apply(msg.Events) {
			e.frame = e.grid.Render()
		}

	case tea.KeyPressMsg:
		keys := nvimpane.Notation(tea.Key(msg))
		e.sess.Do(func(nv *nvim.Nvim) { _, _ = nv.Input(keys) })

	case tea.KeyReleaseMsg:
		// nvim_input has no notion of a key release.

	case tea.PasteMsg:
		// nvim_paste rather than nvim_input, which is subject to mappings and
		// would read a pasted "dd" in normal mode as a delete. Phase -1 is a
		// paste arriving whole rather than in streamed chunks.
		content := msg.Content
		e.sess.Do(func(nv *nvim.Nvim) { _, _ = nv.Paste(content, false, -1) })
	}
	return nil
}

func (e *nvimEditor) setSize(w, h int) {
	w, h = max(1, w), max(1, h)
	e.grid.Resize(w, h)
	// Repaint at the new size straight away rather than waiting for Neovim to
	// answer, so the pane is never a frame of the old width inside the new rect.
	e.frame = e.grid.Render()
	e.sess.Do(func(nv *nvim.Nvim) { _ = nv.TryResizeUI(w, h) })
}

func (e *nvimEditor) value() string     { return e.sess.Text() }
func (e *nvimEditor) setValue(v string) { e.sess.SetText(v) }

// view returns the last painted frame. focus changes nothing about it: Neovim
// draws no focus indication of its own, and the pane's label row and the
// terminal cursor already carry it.
func (e *nvimEditor) view(bool) string { return e.frame }

// meta names the vim mode, which is the one thing about this editor's state
// that is invisible in its own content.
func (e *nvimEditor) meta() string {
	mode := e.grid.Mode()
	if mode == "" {
		return ""
	}
	return strings.ToUpper(mode)
}

func (e *nvimEditor) cursor(x0, y0 int) *tea.Cursor {
	if e.grid.CursorHidden() {
		return nil
	}
	col, row := e.grid.Cursor()
	c := tea.NewCursor(x0+col, y0+row)
	c.Shape = cursorShape(e.grid.CursorShape())
	c.Blink = e.grid.CursorBlinks()
	return c
}

// keepsTrailingCells is true because a trailing run of spaces on a grid row can
// carry paint: a visual selection or the completion popup runs to the pane's
// edge, and both are background colours on otherwise blank cells.
func (e *nvimEditor) keepsTrailingCells() bool { return true }

func (e *nvimEditor) close() { _ = e.sess.Stop() }

func cursorShape(name string) tea.CursorShape {
	switch name {
	case "horizontal":
		return tea.CursorUnderline
	case "vertical":
		return tea.CursorBar
	}
	return tea.CursorBlock
}

// sqlKeywords completes SQL's own vocabulary. It is the candidate source that
// needs nothing from the connection, so it is what the pane offers while no
// schema-aware source is wired in.
type sqlKeywords struct{}

var sqlKeywordList = []string{
	"AND", "AS", "ASC", "BETWEEN", "BY", "CASE", "COUNT", "CREATE", "DELETE",
	"DESC", "DISTINCT", "ELSE", "END", "EXISTS", "FROM", "FULL", "GROUP",
	"HAVING", "ILIKE", "IN", "INNER", "INSERT", "INTO", "IS", "JOIN", "LEFT",
	"LIKE", "LIMIT", "NOT", "NULL", "OFFSET", "ON", "OR", "ORDER", "OUTER",
	"RETURNING", "RIGHT", "SELECT", "SET", "SUM", "THEN", "UNION", "UPDATE",
	"VALUES", "WHEN", "WHERE", "WITH",
}

// Candidates offers nothing behind a qualifier: a keyword never follows a dot,
// and what does — a table's columns — needs a schema this source does not have.
func (sqlKeywords) Candidates(qualifier, prefix string, _ []string) []map[string]any {
	out := []map[string]any{}
	if qualifier != "" {
		return out
	}
	for _, k := range sqlKeywordList {
		if len(prefix) > len(k) || !strings.EqualFold(k[:len(prefix)], prefix) {
			continue
		}
		out = append(out, map[string]any{"word": keywordInTypedCase(prefix, k), "kind": "v", "menu": "keyword"})
	}
	return out
}

// keywordInTypedCase builds the word a keyword is offered as.
//
// Neovim filters what a completion source returns against the text already
// typed, and does so case sensitively: a candidate that does not literally
// begin with the typed characters is dropped before the popup is drawn, and
// 'ignorecase' does not relax it. So the typed characters are kept exactly as
// typed and only the rest is supplied — in upper case, which is the case
// db-query writes its own generated SQL in, unless the prefix says otherwise.
func keywordInTypedCase(prefix, keyword string) string {
	rest := keyword[len(prefix):]
	if prefix != strings.ToUpper(prefix) {
		rest = strings.ToLower(rest)
	}
	return prefix + rest
}
