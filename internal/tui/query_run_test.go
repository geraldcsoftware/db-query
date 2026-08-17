package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// answerRunText feeds the model a queryTextMsg as the editor's command would.
// The command it returns is deliberately not run: it is either a status timer
// or the run itself, and both take longer than the state change under test.
func answerRunText(t *testing.T, m model, msg queryTextMsg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

// failingRunner fails the test if a run is ever dispatched to it.
func failingRunner(t *testing.T) func(context.Context, session.Resolved, string) (adapter.Rows, bool, error) {
	t.Helper()
	return func(_ context.Context, _ session.Resolved, sql string) (adapter.Rows, bool, error) {
		t.Errorf("a run was dispatched with %q, and none should have been", sql)
		return adapter.Rows{}, false, nil
	}
}

// neverFinishingRunner stands in for a run in flight: dispatched, and still
// running when the test looks at the model.
func neverFinishingRunner() func(context.Context, session.Resolved, string) (adapter.Rows, bool, error) {
	return func(ctx context.Context, _ session.Resolved, _ string) (adapter.Rows, bool, error) {
		<-ctx.Done()
		return adapter.Rows{}, false, ctx.Err()
	}
}

// TestAnEmptyQueryIsNotRun: an empty statement costs a round trip to the
// database to be told nothing, and leaves a Results pane that looks like a run
// which returned nothing rather than one that never happened.
func TestAnEmptyQueryIsNotRun(t *testing.T) {
	for _, sql := range []string{"", "   ", "\n\n\t "} {
		m := newTestModel(t)
		m.runner = failingRunner(t)

		got := answerRunText(t, m, queryTextMsg{sql: sql})
		if got.running {
			t.Errorf("%q started a run", sql)
		}
		if !strings.Contains(got.statusMsg, "nothing to run") {
			t.Errorf("%q left the status strip saying %q", sql, got.statusMsg)
		}
	}
}

// TestAFailedReadIsNotAFailedQuery: nothing ran, so the reason belongs in the
// status strip. Putting it in the Results pane would read as a database error
// and would throw away the result already showing there.
func TestAFailedReadIsNotAFailedQuery(t *testing.T) {
	m := newTestModel(t)
	m.runner = failingRunner(t)
	m.results.showRows(rowsOf(3))

	got := answerRunText(t, m, queryTextMsg{err: errors.New("channel closed")})
	if got.running {
		t.Error("a failed read started a run")
	}
	if !strings.Contains(got.statusMsg, "channel closed") {
		t.Errorf("status = %q, want the reason the read failed", got.statusMsg)
	}
	if got.results.errText != "" {
		t.Errorf("the Results pane was made to show %q, which no query produced", got.results.errText)
	}
	if len(got.results.rows.Rows) != 3 {
		t.Error("the result already showing was thrown away by a read that never ran anything")
	}
}

// TestTheResultsPaneSaysWhenOnlyPartOfTheBufferRan: once the selection is gone
// nothing else on screen says which of the buffer's statements produced these
// rows, and the pane's own summary reports the outcome, never the statement.
func TestTheResultsPaneSaysWhenOnlyPartOfTheBufferRan(t *testing.T) {
	t.Setenv("DB_QUERY_TUI_PAGE_SIZE", "")

	whole := newTestModel(t)
	whole.runner = neverFinishingRunner()
	whole = answerRunText(t, whole, queryTextMsg{sql: "select 1"})
	whole.results.showRows(rowsOf(3))
	if got := strings.Join(whole.resultsMeta(), metaSep); got != "3 rows" {
		t.Errorf("a whole-buffer run summarised as %q, want the outcome alone", got)
	}

	part := newTestModel(t)
	part.runner = neverFinishingRunner()
	part = answerRunText(t, part, queryTextMsg{sql: "select 1", selection: true})
	part.results.showRows(rowsOf(3))
	if got := strings.Join(part.resultsMeta(), metaSep); got != "selection · 3 rows" {
		t.Errorf("a selection run summarised as %q", got)
	}

	// The marker outlives an empty result and an error, which are exactly the
	// outcomes where knowing what ran matters most.
	part.results.showError("syntax error at or near \"slect\"")
	if got := strings.Join(part.resultsMeta(), metaSep); got != "selection" {
		t.Errorf("a failed selection run summarised as %q", got)
	}

	// The Schema pane's own shortcut says so too, since its SQL is in no pane.
	preview := newTestModel(t)
	preview.runner = neverFinishingRunner()
	preview.startRun("SELECT * FROM orders LIMIT 100;", "table preview")
	preview.results.showRows(rowsOf(1))
	if got := strings.Join(preview.resultsMeta(), metaSep); got != "table preview · 1 row" {
		t.Errorf("a preview run summarised as %q", got)
	}
}

// TestTheSelectionMarkerIsOnTheRenderedLabelRow ties the summary to the frame,
// since a summary the layout has no room for is not a summary anyone reads.
func TestTheSelectionMarkerIsOnTheRenderedLabelRow(t *testing.T) {
	m := newTestModel(t)
	m.runner = neverFinishingRunner()
	m = answerRunText(t, m, queryTextMsg{sql: "select 1", selection: true})
	m.running = false
	m.results.showRows(rowsOf(3))

	r := m.rects[paneResults]
	row := strings.Split(ansi.Strip(m.View().Content), "\n")[r.y0]
	if !strings.Contains(row, "selection") {
		t.Errorf("the Results label row does not say what ran:\n%q", row)
	}
}

// TestANewRunReplacesTheMarker: the marker describes the run showing, so a
// whole-buffer run after a selection must not still claim to be one.
func TestANewRunReplacesTheMarker(t *testing.T) {
	m := newTestModel(t)
	m.runner = neverFinishingRunner()

	m = answerRunText(t, m, queryTextMsg{sql: "select 1", selection: true})
	m.running = false
	m = answerRunText(t, m, queryTextMsg{sql: "select 2"})
	m.results.showRows(rowsOf(1))

	if got := strings.Join(m.resultsMeta(), metaSep); got != "1 row" {
		t.Errorf("summary = %q, want no claim about a selection", got)
	}
}
