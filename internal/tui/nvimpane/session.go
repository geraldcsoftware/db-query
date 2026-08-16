// Package nvimpane embeds a Neovim child process as db-query's Query editor.
// It owns the process, the MessagePack RPC channel over its stdio, and the
// ext_linegrid state the host paints from; it knows nothing about db-query's
// panes, which is what keeps the renderer testable against synthetic redraw
// batches.
//
// Externalisation is deliberately limited to ext_linegrid. Neovim therefore
// composites its own command line, completion popup and floating windows into
// the single grid before the host ever sees them, so there is exactly one
// stream of cells to draw.
package nvimpane

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/neovim/go-client/nvim"
)

// minVersion is the floor a binary has to clear to be usable. Neovim's
// 'autocomplete' option and the function-source flags of 'complete', which the
// as-you-type completion is built on, do not exist before 0.12.0; below it
// there is no single code path that serves both, so the pane falls back to the
// textarea instead of carrying a version-gated Lua fork.
var minVersion = [3]int{0, 12, 0}

// ErrNoBinary reports that there is no nvim on PATH at all.
var ErrNoBinary = errors.New("no nvim on PATH")

// ErrTooOld reports a binary found and rejected for its version.
type ErrTooOld struct{ Got [3]int }

func (e ErrTooOld) Error() string {
	return fmt.Sprintf("nvim %d.%d.%d is below the %d.%d.%d floor",
		e.Got[0], e.Got[1], e.Got[2], minVersion[0], minVersion[1], minVersion[2])
}

// Batch is one redraw notification: every event Neovim bundled into it.
type Batch struct{ Events [][]any }

// Options configures one embedded session.
type Options struct {
	// Cols and Rows are the grid's initial size, in cells.
	Cols, Rows int

	// Candidates supplies the words completion offers. A nil source completes
	// nothing, which is a working pane with an empty popup rather than a broken
	// one.
	Candidates CandidateSource
}

// Session is one embedded Neovim child process, live for as long as the pane is.
type Session struct {
	nv   *nvim.Nvim
	chid int

	// Redraw carries every batch the "redraw" notification brings. The consumer
	// must drain it or Neovim's notification goroutine blocks on the send, which
	// is the intended backpressure: under a resize storm Neovim coalesces its own
	// redraws rather than the host throwing finished work away.
	Redraw chan Batch

	// Ended fires once, when the RPC connection closes for any reason. A nil
	// error means the host asked for it; anything else is Neovim dying under the
	// pane, which takes the whole TUI down with it.
	Ended chan error

	// done releases a redraw handler parked on the unbuffered send once the
	// session is shutting down. It is closed exactly once, by stop. The Redraw
	// channel itself is never closed: closing it under a parked sender would
	// panic inside Neovim's own notification goroutine.
	done chan struct{}

	// ops serialises everything the host sends to Neovim onto one goroutine.
	// Keys have to reach nvim_input in the order they were typed, and a
	// tea.Cmd per keystroke would race them; the same queue stops a buffer read
	// overtaking the keystrokes that should precede it. It is never closed, so a
	// late enqueue after shutdown cannot panic.
	ops chan func(*nvim.Nvim)

	// mu guards quitting, which stop sets before asking Neovim to exit so the
	// goroutine watching the connection can tell an ordinary detach from a crash.
	mu       sync.Mutex
	quitting bool
}

// Start spawns Neovim, gates it on version, registers its handlers, attaches
// the UI and pushes db-query's own required wiring.
//
// The order is load-bearing. RegisterHandler has to precede AttachUI: a
// notification arriving for an unregistered method is dropped and never
// replayed, and the first redraw batch follows attach immediately.
func Start(opts Options) (*Session, error) {
	bin, err := exec.LookPath("nvim")
	if err != nil {
		return nil, ErrNoBinary
	}

	// NVIM_APPNAME points Neovim's config directory at ~/.config/dbquery, so the
	// user's real Neovim config is never read, and -i NONE drops shada. There is
	// deliberately no -u, which is what still lets a user-created
	// ~/.config/dbquery/init.lua be sourced.
	//
	// shortmess+=I suppresses the intro screen, and belongs on the spawn line
	// rather than in the setup push below: the push lands after attach, by which
	// time two frames carrying the welcome screen have already reached a flush.
	// A --cmd command runs before any vimrc, so the option is in force before the
	// first paint and the intro is never drawn at all.
	args := []string{"--embed", "-i", "NONE", "--cmd", "set shortmess+=I"}

	nv, err := nvim.NewChildProcess(
		nvim.ChildProcessCommand(bin),
		nvim.ChildProcessArgs(args...),
		// ChildProcessEnv replaces the child's environment rather than extending
		// it, so the appname is appended to a copy of this process's own.
		nvim.ChildProcessEnv(append(os.Environ(), "NVIM_APPNAME=dbquery")),
		// The application owns Serve, because the moment it returns is the only
		// asynchronous signal that Neovim has gone.
		nvim.ChildProcessServe(false),
	)
	if err != nil {
		return nil, fmt.Errorf("spawn nvim: %w", err)
	}

	s := &Session{
		nv:     nv,
		Redraw: make(chan Batch),
		Ended:  make(chan error, 1),
		done:   make(chan struct{}),
		ops:    make(chan func(*nvim.Nvim), 256),
	}

	go func() {
		for op := range s.ops {
			op(nv)
		}
	}()

	go func() {
		err := nv.Serve()
		s.mu.Lock()
		deliberate := s.quitting
		s.mu.Unlock()
		switch {
		case deliberate:
			err = nil
		case err == nil:
			err = errors.New("neovim closed the channel without being asked to")
		}
		s.Ended <- err
	}()

	ver, err := apiVersion(nv)
	if err != nil {
		s.stop()
		return nil, fmt.Errorf("nvim api info: %w", err)
	}
	if less(ver, minVersion) {
		s.stop()
		return nil, ErrTooOld{Got: ver}
	}
	s.chid = nv.ChannelID()

	if err := s.registerHandlers(opts.Candidates); err != nil {
		s.stop()
		return nil, err
	}

	if err := nv.AttachUI(max(1, opts.Cols), max(1, opts.Rows), map[string]any{"ext_linegrid": true}); err != nil {
		s.stop()
		return nil, fmt.Errorf("attach ui: %w", err)
	}

	// One chunk, after attach, so it lands after any user init.lua and settles
	// the options db-query actually requires last.
	if err := nv.ExecLua(setupLua, nil, s.chid); err != nil {
		s.stop()
		return nil, fmt.Errorf("setup push: %w", err)
	}

	return s, nil
}

// registerHandlers wires every method Neovim may call back on. All of them are
// registered before AttachUI, including the ones Neovim cannot reach until the
// setup push has run, so the rule has a single home.
func (s *Session) registerHandlers(src CandidateSource) error {
	handlers := []struct {
		method string
		fn     any
	}{
		{"redraw", func(updates ...[]any) {
			select {
			case s.Redraw <- Batch{Events: updates}:
			case <-s.done:
			}
		}},
		{"dbq_complete", func(findstart int, line string, col int, buflines []string) (any, error) {
			return complete(src, findstart, line, col, buflines), nil
		}},
	}
	for _, h := range handlers {
		if err := s.nv.RegisterHandler(h.method, h.fn); err != nil {
			return fmt.Errorf("register %s: %w", h.method, err)
		}
	}
	return nil
}

// ChannelID is the RPC channel Neovim calls the host back on.
func (s *Session) ChannelID() int { return s.chid }

// Done is closed once the session is shutting down. Anything selecting on the
// session's channels selects on this too, so nothing parks forever.
func (s *Session) Done() <-chan struct{} { return s.done }

// Do queues one call against Neovim. Every Do runs in order on a goroutine of
// the session's own, so the caller — the UI event loop — never blocks on a
// round trip and never reorders one call against another.
func (s *Session) Do(op func(*nvim.Nvim)) {
	select {
	case s.ops <- op:
	case <-s.done:
	}
}

// SetText replaces the buffer. It is queued like every other call, so a read
// enqueued after it is guaranteed to see what was written.
func (s *Session) SetText(text string) {
	lines := strings.Split(text, "\n")
	repl := make([][]byte, len(lines))
	for i, l := range lines {
		repl[i] = []byte(l)
	}
	s.Do(func(nv *nvim.Nvim) { _ = nv.SetBufferLines(0, 0, -1, true, repl) })
}

// Stop ends the session: Neovim is asked to quit before the channel is closed,
// so an ordinary shutdown reports nothing rather than the exit status a bare
// close produces. Calling it twice is safe.
func (s *Session) Stop() error { return s.stop() }

func (s *Session) stop() error {
	s.mu.Lock()
	already := s.quitting
	s.quitting = true
	s.mu.Unlock()
	if already {
		return nil
	}
	close(s.done)

	// The reply never arrives, because Neovim exits before it can send one, so
	// the session-closed error this returns is the expected outcome.
	_ = s.nv.Command("qa!")
	return s.nv.Close()
}

// apiVersion asks Neovim for its API metadata and reads the version out of it.
func apiVersion(nv *nvim.Nvim) ([3]int, error) {
	info, err := nv.APIInfo()
	if err != nil {
		return [3]int{}, err
	}
	return versionOf(info)
}

// versionOf digs the version out of nvim_get_api_info's reply. The plain
// major/minor/patch integers are what the gate compares; the prerelease and
// build fields alongside them describe a build, not an API level. Every shape
// that is not those three integers is an error rather than a zero version, so a
// reply this does not understand falls back to the textarea instead of being
// read as an ancient Neovim.
func versionOf(info []any) ([3]int, error) {
	if len(info) < 2 {
		return [3]int{}, errors.New("api info too short")
	}
	meta, ok := info[1].(map[string]any)
	if !ok {
		return [3]int{}, errors.New("api info metadata is not a map")
	}
	ver, ok := meta["version"].(map[string]any)
	if !ok {
		return [3]int{}, errors.New("api info carries no version map")
	}
	var out [3]int
	for i, key := range []string{"major", "minor", "patch"} {
		n, ok := toInt(ver[key])
		if !ok {
			return [3]int{}, fmt.Errorf("version.%s is %T, not an integer", key, ver[key])
		}
		out[i] = n
	}
	return out, nil
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}
