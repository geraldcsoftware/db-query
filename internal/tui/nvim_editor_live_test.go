package tui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/neovim/go-client/nvim"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// The tests in this file drive a real Neovim. They skip on a machine that has
// none, or one below the version floor, which is the same gate the pane itself
// uses: a developer running 0.11 sees skips rather than failures, exactly as a
// user there sees the textarea rather than an error.

// livePane is the real model driven without a terminal: Update is called from
// the test's goroutine and from the editor's redraw pump, which Bubble Tea
// would otherwise serialise for us, so a mutex stands in for the event loop.
type livePane struct {
	t  *testing.T
	mu sync.Mutex
	m  model

	// frames is every painted frame of the editor, in order. Counting them is
	// how a claim about what is drawn before the setup push lands can be tested
	// at all: a screenshot taken at the wrong moment proves nothing either way.
	frames []string
}

func newLivePane(t *testing.T) *livePane {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DB_QUERY_QUERIES_DIR", t.TempDir())

	editor, err := newNvimEditor()
	if err != nil {
		t.Skipf("no usable neovim: %v", err)
	}
	t.Cleanup(editor.close)

	p := &livePane{t: t}
	p.m = newModel(session.Resolved{}, session.CommonFlags{}, "1.2.3", nil, editor)
	p.m.width, p.m.height = 100, 30
	p.m.focus = paneQuery
	p.m.recomputeLayout()

	editor.start(func(msg tea.Msg) {
		p.mu.Lock()
		defer p.mu.Unlock()
		next, _ := p.m.Update(msg)
		p.m = next.(model)
		if _, ok := msg.(nvimRedrawMsg); ok {
			p.frames = append(p.frames, p.m.query.view(true))
		}
	})
	p.settle()
	return p
}

// editor is the pane's editor as its concrete type, for the handful of checks
// that need to reach past the seam.
func (p *livePane) editor() *nvimEditor {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m.query.(*nvimEditor)
}

// send applies one message the way the event loop would.
func (p *livePane) send(msg tea.Msg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next, _ := p.m.Update(msg)
	p.m = next.(model)
}

// input feeds Neovim a string in its own notation, bypassing key translation.
// Tests about what Neovim then does are clearer written in Neovim's language.
func (p *livePane) input(keys string) {
	done := make(chan struct{})
	p.editor().sess.Do(func(nv *nvim.Nvim) {
		_, _ = nv.Input(keys)
		close(done)
	})
	<-done
}

// lua evaluates a chunk in the live instance and decodes its result.
func (p *livePane) lua(chunk string, out any) {
	p.t.Helper()
	errc := make(chan error, 1)
	p.editor().sess.Do(func(nv *nvim.Nvim) { errc <- nv.ExecLua(chunk, out) })
	if err := <-errc; err != nil {
		p.t.Fatalf("lua %q: %v", chunk, err)
	}
}

// settle waits for the redraw stream to go quiet, which is as close to "Neovim
// has finished reacting" as a UI client can get: there is no acknowledgement to
// wait on, only the absence of further frames.
func (p *livePane) settle() {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		before := len(p.frames)
		p.mu.Unlock()
		time.Sleep(120 * time.Millisecond)
		p.mu.Lock()
		after := len(p.frames)
		p.mu.Unlock()
		if after == before && after > 0 {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("the pane never settled: %d frames painted", after)
		}
	}
}

// value is the buffer as the host sees it, through the mirror.
func (p *livePane) value() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m.query.value()
}

// waitForFrame polls until the painted frame carries want. Completion in
// particular cannot be waited on any other way: Neovim collects its candidates
// on a decaying time slice, giving a function source up to about a second, so
// the popup arrives some frames after the keystroke that asked for it.
func (p *livePane) waitForFrame(want string) string {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		frame := ansi.Strip(p.m.query.view(true))
		p.mu.Unlock()
		if strings.Contains(frame, want) {
			return frame
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("waited for %q, last frame was:\n%s", want, frame)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestLivePanePaintsTheBufferItIsTyped is the whole loop in one: keys routed
// through the real Update reach Neovim, Neovim's redraw comes back, the grid
// folds it in, and the text appears in the frame the host would draw.
func TestLivePanePaintsTheBufferItIsTyped(t *testing.T) {
	p := newLivePane(t)

	for _, k := range "iselect id from customers" {
		p.send(tea.KeyPressMsg{Code: k, Text: string(k)})
	}
	p.settle()

	if got := p.value(); got != "select id from customers" {
		t.Fatalf("buffer mirror = %q, want the typed line", got)
	}
	frame := ansi.Strip(p.m.query.view(true))
	if !strings.Contains(frame, "select id from customers") {
		t.Fatalf("the painted frame does not carry the typed line:\n%s", frame)
	}
	// The line-number gutter is the one piece of Neovim's own chrome the pane
	// keeps, so its absence would mean the setup push never landed.
	if !strings.Contains(frame, "1 select") {
		t.Errorf("the line-number gutter is missing from the frame:\n%s", frame)
	}
}

// TestLivePaneTakesEscRatherThanQuitting is decision 8 against the real editor,
// which is the only thing that can show Esc doing its actual job. The stubbed
// routing tests prove the host lets it through; this proves what it does when
// it lands.
func TestLivePaneTakesEscRatherThanQuitting(t *testing.T) {
	p := newLivePane(t)

	p.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
	p.settle()
	p.mu.Lock()
	inInsert := p.m.query.meta()
	p.mu.Unlock()
	if inInsert != "INSERT" {
		t.Fatalf("mode = %q after i, want INSERT", inInsert)
	}

	p.mu.Lock()
	next, cmd := p.m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	p.m = next.(model)
	p.mu.Unlock()
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("Esc quit the program instead of reaching the editor")
		}
	}
	p.settle()

	p.mu.Lock()
	defer p.mu.Unlock()
	if got := p.m.query.meta(); got != "NORMAL" {
		t.Fatalf("mode = %q after Esc, want NORMAL", got)
	}
}

// TestLivePanePaintsTheCompletionPopup is the renderer's hardest case, and the
// reason nothing is externalised beyond ext_linegrid: Neovim composites the
// popup into the same grid as the text, so it reaches the host as ordinary
// cells it already knows how to paint.
//
// It pins the rule that decides whether a candidate is ever seen at all.
// Neovim filters what a source returns against the text already typed, and does
// so case sensitively — 'ignorecase' does not relax it — so a candidate that
// does not literally begin with the typed characters is dropped before the
// popup is drawn.
func TestLivePanePaintsTheCompletionPopup(t *testing.T) {
	p := newLivePane(t)

	for _, c := range []struct{ typed, want string }{
		{"sel", "select"},
		{"SEL", "SELECT"},
		{"Wh", "Where"},
	} {
		p.input("<C-e><Esc>ggdGi" + c.typed)
		frame := p.waitForFrame(c.want + " v keyword")
		t.Logf("typed %q, popup offered %q", c.typed, c.want)
		if !strings.Contains(frame, c.typed) {
			t.Errorf("the buffer lost the typed text %q:\n%s", c.typed, frame)
		}
	}
}

// TestLivePaneNeverPaintsTheWelcomeScreen is the reason shortmess is set on the
// spawn line rather than in the setup push. The push lands after the UI
// attaches, by which point Neovim has already had two frames in which to draw
// its intro, so the only way to know is to count every frame ever painted.
func TestLivePaneNeverPaintsTheWelcomeScreen(t *testing.T) {
	p := newLivePane(t)

	p.mu.Lock()
	defer p.mu.Unlock()
	for i, f := range p.frames {
		if strings.Contains(ansi.Strip(f), "Nvim is open source") {
			t.Fatalf("frame %d of %d carries the welcome screen:\n%s", i, len(p.frames), f)
		}
	}
	if len(p.frames) == 0 {
		t.Fatal("no frames were painted at all, so nothing was proved")
	}
	t.Logf("%d frames painted, none carrying the welcome screen", len(p.frames))
}

// TestLivePaneGivesTheWindowEveryRow guards the two options that decide how much
// of a short pane is usable SQL. laststatus = 0 does not reclaim the
// command-line row; only cmdheight = 0 does, and without it a three-row pane is
// a label, a blank command line and one line of query.
func TestLivePaneGivesTheWindowEveryRow(t *testing.T) {
	p := newLivePane(t)

	var height int
	p.lua(`return vim.api.nvim_win_get_height(0)`, &height)
	_, rows := p.editor().grid.Size()
	if height != rows {
		t.Errorf("the window holds %d of the grid's %d rows, want all of them", height, rows)
	}

	var eob string
	p.lua(`return vim.opt.fillchars:get().eob or '~'`, &eob)
	if eob != " " {
		t.Errorf("end-of-buffer filler = %q, want it blanked", eob)
	}
}

// TestLivePaneSetValueRoundTrips covers the path the Saved pane loads a query
// through: the host writes the buffer, and reads back what it wrote.
func TestLivePaneSetValueRoundTrips(t *testing.T) {
	p := newLivePane(t)

	const sql = "select *\nfrom orders\nwhere placed_at > now() - interval '1 day'"
	p.mu.Lock()
	p.m.query.setValue(sql)
	p.mu.Unlock()
	p.settle()

	if got := p.value(); got != sql {
		t.Fatalf("value after setValue = %q, want %q", got, sql)
	}
	var lines []string
	p.lua(`return vim.api.nvim_buf_get_lines(0, 0, -1, false)`, &lines)
	if got := strings.Join(lines, "\n"); got != sql {
		t.Fatalf("neovim's own buffer = %q, want %q", got, sql)
	}
}

// TestLivePaneReportsNeovimLeavingUnasked is decision 13's crash path. Neovim
// ending without the host asking has to reach the model as a fatal, because a
// Query pane with no editor behind it is not a state the TUI can carry on in.
func TestLivePaneReportsNeovimLeavingUnasked(t *testing.T) {
	p := newLivePane(t)

	p.editor().sess.Do(func(nv *nvim.Nvim) { _ = nv.Command("qall!") })

	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		fatal := p.m.fatal
		p.mu.Unlock()
		if fatal != nil {
			t.Logf("reported as: %v", fatal)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("neovim went and the model never learned of it")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestLivePaneSurvivesAHostileUserConfig is the claim decision 12 makes for
// pushing db-query's wiring after any user init.lua, tested rather than assumed.
// The config here does the worst a plausible bad edit can: it assigns wholesale
// every option the pane depends on, dropping the flags the push appends.
func TestLivePaneSurvivesAHostileUserConfig(t *testing.T) {
	dir := t.TempDir()
	writeUserConfig(t, dir, `
vim.o.cmdheight  = 1
vim.o.laststatus = 2
vim.o.ruler      = true
vim.opt.shortmess = 'ltToOCF'
vim.opt.fillchars = { eob = '~' }
vim.bo.filetype = 'text'
vim.g.sql_type_default = 'mysql'
vim.api.nvim_create_autocmd('FileType', {
  pattern = 'sql',
  callback = function() vim.o.cmdheight = 1 end,
})
`)
	p := newLivePane(t)

	var opt struct {
		Cmdheight  int    `msgpack:"cmdheight"`
		Laststatus int    `msgpack:"laststatus"`
		Filetype   string `msgpack:"filetype"`
		Eob        string `msgpack:"eob"`
		Shortmess  string `msgpack:"shortmess"`
		Dialect    string `msgpack:"dialect"`
	}
	p.lua(`return {
	  cmdheight  = vim.o.cmdheight,
	  laststatus = vim.o.laststatus,
	  filetype   = vim.bo.filetype,
	  eob        = vim.opt.fillchars:get().eob or '~',
	  shortmess  = vim.o.shortmess,
	  dialect    = vim.g.sql_type_default,
	}`, &opt)

	for _, c := range []struct {
		what      string
		got, want any
	}{
		{"cmdheight", opt.Cmdheight, 0},
		{"laststatus", opt.Laststatus, 0},
		{"filetype", opt.Filetype, "sql"},
		{"end-of-buffer filler", opt.Eob, " "},
		{"sql dialect", opt.Dialect, "sqlanywhere"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v — the push did not win over the user config", c.what, c.got, c.want)
		}
	}
	if !strings.Contains(opt.Shortmess, "I") {
		t.Errorf("shortmess = %q, want the intro flag back after the config dropped it", opt.Shortmess)
	}
	// The config's own directory has to be the one Neovim actually read, or the
	// test proves nothing at all.
	var config string
	p.lua(`return vim.fn.stdpath('config')`, &config)
	if want := filepath.Join(dir, "dbquery"); config != want {
		t.Fatalf("neovim read its config from %q, want %q", config, want)
	}
}

// TestLivePaneCannotStopAConfigChangingThingsLater is the other half of the same
// claim, and the honest limit on it. Running last beats anything a user config
// sets while it loads; it beats nothing a user config arranges to run afterwards.
func TestLivePaneCannotStopAConfigChangingThingsLater(t *testing.T) {
	writeUserConfig(t, t.TempDir(), `
vim.api.nvim_create_autocmd('InsertEnter', {
  callback = function() vim.o.cmdheight = 1 end,
})
`)
	p := newLivePane(t)

	var before int
	p.lua(`return vim.o.cmdheight`, &before)
	if before != 0 {
		t.Fatalf("cmdheight = %d before the autocommand fired, want 0", before)
	}

	p.input("i")
	p.settle()

	var after int
	p.lua(`return vim.o.cmdheight`, &after)
	if after != 1 {
		t.Fatalf("cmdheight = %d after InsertEnter, want 1: the limit this test records has moved", after)
	}
	t.Log("a deferred autocommand in the user config does override the push, as it must — the push runs once, at startup")
}

// writeUserConfig plants an init.lua where the pane will read it, by pointing
// the config root at a directory of the test's own. The pane spawns Neovim with
// NVIM_APPNAME=dbquery, so the file belongs under a dbquery directory there.
func writeUserConfig(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "dbquery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.lua"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)
}
