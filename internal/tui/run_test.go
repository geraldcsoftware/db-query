package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// controlledRunner lets a test decide exactly when a run "completes" and
// records how many times it was invoked and whether the context it was
// given was already cancelled by the time it returned.
type controlledRunner struct {
	mu       sync.Mutex
	calls    int
	release  chan struct{}
	canceled bool
}

func (r *controlledRunner) run(ctx context.Context, _ session.Resolved, _ string) (adapter.Rows, bool, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	<-r.release
	if ctx.Err() != nil {
		r.mu.Lock()
		r.canceled = true
		r.mu.Unlock()
		return adapter.Rows{}, false, ctx.Err()
	}
	return adapter.Rows{Columns: []string{"id"}}, false, nil
}

func newRunnerTestModel(t *testing.T, r *controlledRunner) model {
	m := newTestModel(t)
	m.runner = r.run
	return m
}

func TestStartRunSetsRunningAndClearsResults(t *testing.T) {
	r := &controlledRunner{release: make(chan struct{})}
	defer close(r.release)
	m := newRunnerTestModel(t, r)
	m.query.setValue("select 1")
	m.focus = paneQuery

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Plain Enter only edits the buffer; the run itself is dispatched by
	// startRun, which is what this test is about. Ctrl+Enter's own routing
	// through Update is covered by triggers_test.go.
	mm := updated.(model)
	cmd = mm.startRun(mm.query.value())
	if !mm.running {
		t.Fatal("running must be true immediately")
	}
	if mm.results.hasContent() {
		t.Fatal("results pane must be cleared immediately on run start")
	}
	if cmd == nil {
		t.Fatal("startRun must return a command")
	}
}

func TestSingleFlightRejectsSecondRun(t *testing.T) {
	r := &controlledRunner{release: make(chan struct{})}
	defer close(r.release)
	m := newRunnerTestModel(t, r)
	m.startRun("select 1")
	m.running = true // startRun's own effect, set explicitly since startRun returns a new value

	_ = m.startRun("select 2")
	r.mu.Lock()
	calls := r.calls
	r.mu.Unlock()
	// startRun itself only builds the tea.Cmd; the runner is invoked when the
	// returned Cmd executes. Assert the second startRun does not even try by
	// checking the model's status message was set instead.
	if m.statusMsg == "" {
		t.Fatal("expected a status message on a rejected second run")
	}
	_ = calls
}

func TestCtrlCCancelsRunningQuery(t *testing.T) {
	r := &controlledRunner{release: make(chan struct{})}
	m := newRunnerTestModel(t, r)
	m.running = true
	canceled := false
	m.cancel = func() { canceled = true }

	updated, _ := m.Update(key("ctrl+c"))
	if !canceled {
		t.Fatal("Ctrl+C while running must call the stored cancel func")
	}
	if updated.(model).focus != paneQuery {
		t.Fatalf("focus after cancel = %v, want paneQuery", updated.(model).focus)
	}
	close(r.release)
}

func TestQueryResultMsgCancelledClearsRunningNoContent(t *testing.T) {
	m := newTestModel(t)
	m.running = true
	m.cancel = func() {}
	updated, _ := m.Update(queryResultMsg{err: context.Canceled})
	mm := updated.(model)
	if mm.running {
		t.Fatal("running must clear on a cancelled result")
	}
	if mm.cancel != nil {
		t.Fatal("cancel must clear to nil on a cancelled result")
	}
	if mm.results.hasContent() {
		t.Fatal("results pane must stay empty after a cancel")
	}
	if mm.statusMsg != "query cancelled" {
		t.Fatalf("statusMsg = %q, want %q", mm.statusMsg, "query cancelled")
	}
}

// TestQueryResultMsgTimeoutIsAnErrorNotACancellation separates the two
// context errors: a cancellation is something the user asked for, a --timeout
// expiry is a failure they did not, and reporting the latter as "query
// cancelled" leaves no trace of what actually went wrong.
func TestQueryResultMsgTimeoutIsAnErrorNotACancellation(t *testing.T) {
	m := newTestModel(t)
	m.flags.Timeout = 30 * time.Second
	m.running = true
	m.cancel = func() {}
	updated, _ := m.Update(queryResultMsg{err: fmt.Errorf("psql: %w", context.DeadlineExceeded)})
	mm := updated.(model)
	if mm.statusMsg == "query cancelled" {
		t.Fatal("a timeout must not be reported as a user cancellation")
	}
	if !mm.results.hasContent() {
		t.Fatal("a timeout must surface on the Results pane's error path")
	}
	if got := mm.results.view(); !strings.Contains(got, "timed out after 30s") {
		t.Fatalf("results = %q, want the timeout duration", got)
	}
}

func TestQueryResultMsgInvokesTheHeldCancelFunc(t *testing.T) {
	// A completed run's context.WithTimeout timer stays armed until the full
	// --timeout elapses unless its CancelFunc is actually called.
	for name, msg := range map[string]queryResultMsg{
		"success": {rows: adapter.Rows{Columns: []string{"id"}}},
		"error":   {err: errors.New("boom")},
	} {
		m := newTestModel(t)
		m.running = true
		canceled := false
		m.cancel = func() { canceled = true }
		updated, _ := m.Update(msg)
		if !canceled {
			t.Errorf("%s: the held CancelFunc must be invoked, not merely dropped", name)
		}
		if updated.(model).cancel != nil {
			t.Errorf("%s: cancel must clear to nil", name)
		}
	}
}

func TestQueryResultMsgErrorShowsInResults(t *testing.T) {
	m := newTestModel(t)
	m.running = true
	m.cancel = func() {}
	updated, _ := m.Update(queryResultMsg{err: errors.New("boom")})
	mm := updated.(model)
	if mm.running {
		t.Fatal("running must clear on any terminal result")
	}
	if mm.cancel != nil {
		t.Fatal("cancel must clear to nil on any terminal result")
	}
	if !mm.results.hasContent() {
		t.Fatal("results pane must show the error text")
	}
}

func TestQueryResultTimingOut(t *testing.T) {
	// Guards against a test-suite hang if startRun's returned Cmd is ever
	// accidentally made synchronous in a future edit.
	r := &controlledRunner{release: make(chan struct{})}
	defer close(r.release)
	m := newRunnerTestModel(t, r) // built here: t.Setenv is not safe off the test goroutine
	done := make(chan struct{})
	go func() {
		_ = m.startRun("select 1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startRun must return immediately, not block on the runner")
	}
}
