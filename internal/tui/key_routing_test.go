package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// modalStub stands in for an editor with modes of its own. Routing is decided
// entirely on the host's side of the seam, so driving these cases through a
// real Neovim would prove nothing further and cost a child process apiece.
// Everything except modal() and the record of what arrived is the textarea's,
// which keeps value() meaningful for the cases that check nothing was eaten.
type modalStub struct {
	*textareaEditor
	got []string
}

func newModalStub() *modalStub { return &modalStub{textareaEditor: newTextareaEditor()} }

func (e *modalStub) modal() bool { return true }

func (e *modalStub) update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		e.got = append(e.got, k.String())
	}
	return e.textareaEditor.update(msg)
}

func (e *modalStub) forwarded(name string) bool {
	for _, k := range e.got {
		if k == name {
			return true
		}
	}
	return false
}

// modalModel is a test model whose Query pane holds a modal editor, focused
// unless a case moves it.
func modalModel(t *testing.T) (model, *modalStub) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DB_QUERY_QUERIES_DIR", t.TempDir())
	ed := newModalStub()
	m := newModel(testResolved(t), bootstrapFlags(""), "v1", nil, ed)
	m.width, m.height = 100, 40
	m.recomputeLayout()
	m.focus = paneQuery
	return m, ed
}

// press sends one key by its Bubble Tea name.
func pressKey(m model, name string) (model, tea.Cmd) {
	next, cmd := m.Update(namedKey(name))
	return next.(model), cmd
}

func namedKey(name string) tea.KeyPressMsg {
	codes := map[string]rune{
		"esc": tea.KeyEscape, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"f2": tea.KeyF2, "f5": tea.KeyF5, "f10": tea.KeyF10, "enter": tea.KeyEnter,
	}
	if c, ok := codes[name]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	if rest, ok := strings.CutPrefix(name, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModCtrl}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestModalQueryPaneKeepsOnlyTheReservedKeys is the reserved set: these are
// taken by the host and never reach the editor, and everything outside the set
// is forwarded untouched.
func TestModalQueryPaneKeepsOnlyTheReservedKeys(t *testing.T) {
	reserved := []string{"f2", "f5", "ctrl+h", "ctrl+j", "ctrl+k", "ctrl+l", "f10"}
	for _, name := range reserved {
		m, ed := modalModel(t)
		if _, _ = pressKey(m, name); ed.forwarded(name) {
			t.Errorf("%s reached the editor, but the host reserves it", name)
		}
	}

	// Esc, Ctrl+C and the paging keys are the four decision 8 hands over, and
	// the ordinary ones stand for everything else.
	forwarded := []string{"esc", "ctrl+c", "pgup", "pgdown", "enter", "a", "ctrl+w", "ctrl+d"}
	for _, name := range forwarded {
		m, ed := modalModel(t)
		_, cmd := pressKey(m, name)
		if !ed.forwarded(name) {
			t.Errorf("%s did not reach the editor", name)
		}
		if isQuit(cmd) {
			t.Errorf("%s quit the program instead of reaching the editor", name)
		}
	}
}

// TestEscAndCtrlCStillQuitFromEveryOtherPane: only the Query pane's own editor
// takes these over, so the three panes that have no editor keep them.
func TestEscAndCtrlCStillQuitFromEveryOtherPane(t *testing.T) {
	for _, focus := range []pane{paneSchema, paneSaved, paneResults} {
		for _, name := range []string{"esc", "ctrl+c"} {
			m, _ := modalModel(t)
			m.focus = focus
			if _, cmd := pressKey(m, name); !isQuit(cmd) {
				t.Errorf("%s with focus %v did not quit", name, focus)
			}
		}
	}
}

// TestF10QuitsFromEveryPane: the Query pane needs a quit that is not Esc, and a
// quit that works everywhere is one fewer thing to remember than a quit that
// works in one pane.
func TestF10QuitsFromEveryPane(t *testing.T) {
	for _, focus := range []pane{paneSchema, paneSaved, paneQuery, paneResults} {
		m, _ := modalModel(t)
		m.focus = focus
		if _, cmd := pressKey(m, "f10"); !isQuit(cmd) {
			t.Errorf("F10 with focus %v did not quit", focus)
		}
	}
}

// TestCtrlCCancelsARunningQueryEvenInTheEditor: a query still in flight is the
// more urgent thing to stop, so cancelling outranks the editor's claim.
func TestCtrlCCancelsARunningQueryEvenInTheEditor(t *testing.T) {
	m, ed := modalModel(t)
	m.running = true
	cancelled := false
	m.cancel = func() { cancelled = true }

	next, cmd := pressKey(m, "ctrl+c")
	if !cancelled {
		t.Fatal("Ctrl+C did not cancel the in-flight run")
	}
	if ed.forwarded("ctrl+c") {
		t.Error("Ctrl+C reached the editor while a query was running")
	}
	if isQuit(cmd) {
		t.Error("Ctrl+C quit the program instead of cancelling the run")
	}
	if next.focus != paneQuery {
		t.Errorf("focus = %v after cancelling, want the Query pane", next.focus)
	}
}

// TestPagingReachesTheResultsPaneFromEveryPaneButTheEditor: paging from
// anywhere is a convenience worth keeping, and the editor is the one place the
// keys already mean something.
func TestPagingReachesTheResultsPaneFromEveryPaneButTheEditor(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "") // the 100-row default, so 432 rows page

	m, ed := modalModel(t)
	m.focus = paneResults
	m.results.showRows(rowsOf(432))
	next, _ := pressKey(m, "pgdown")
	if next.results.page != 1 {
		t.Errorf("page = %d after PgDn outside the editor, want 1", next.results.page)
	}

	next.focus = paneQuery
	after, _ := pressKey(next, "pgdown")
	if after.results.page != 1 {
		t.Errorf("page = %d after PgDn in the editor, want it left alone", after.results.page)
	}
	if !ed.forwarded("pgdown") {
		t.Error("PgDn did not reach the editor")
	}
}

// TestTheFallbackPaneKeepsTodaysKeys is the promise the textarea fallback
// makes: a machine without a usable Neovim gets exactly the pane db-query
// shipped before, keys included.
func TestTheFallbackPaneKeepsTodaysKeys(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "")
	m := newTestModel(t)
	m.focus = paneQuery
	m.results.showRows(rowsOf(432))

	if _, cmd := pressKey(m, "esc"); !isQuit(cmd) {
		t.Error("Esc no longer quits from the textarea pane")
	}
	next, _ := pressKey(m, "pgdown")
	if next.results.page != 1 {
		t.Errorf("page = %d, want PgDn to still page the Results pane", next.results.page)
	}
}

// TestTheHintBarMatchesTheKeysThePaneActuallyHas: the bar is the only place a
// user is told the keys changed, so it has to change with them.
func TestTheHintBarMatchesTheKeysThePaneActuallyHas(t *testing.T) {
	m, _ := modalModel(t)
	// Wide enough that nothing is dropped for want of room, so what is absent
	// is absent by decision rather than by clipping.
	const w = 140

	bar := ansi.Strip(m.bottomBar(w))
	if !strings.Contains(bar, "F10 quit") {
		t.Errorf("the editor's bar does not advertise its quit:\n%s", bar)
	}
	for _, gone := range []string{"Esc quit", "PgUp", "load/expand"} {
		if strings.Contains(bar, gone) {
			t.Errorf("the editor's bar still advertises %q, which now belongs to the editor:\n%s", gone, bar)
		}
	}

	m.focus = paneResults
	bar = ansi.Strip(m.bottomBar(w))
	for _, want := range []string{"Esc quit", "PgUp", "load/expand"} {
		if !strings.Contains(bar, want) {
			t.Errorf("the bar lost %q outside the editor:\n%s", want, bar)
		}
	}
}
