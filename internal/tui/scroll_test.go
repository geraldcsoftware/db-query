package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/geraldcsoftware/db-query/internal/savedquery"
	"github.com/geraldcsoftware/db-query/internal/schema"
)

// tablesNamed builds a catalogue of n single-column tables, named so a rendered
// row can be tied back to its index.
func tablesNamed(n int) []schema.Table {
	out := make([]schema.Table, n)
	for i := range out {
		out[i] = schema.Table{
			Schema:  "public",
			Name:    "t" + strconv.Itoa(i),
			Columns: []schema.Column{{Name: "c" + strconv.Itoa(i), DataType: "int8"}},
		}
	}
	return out
}

func down(n int, p schemaPane) schemaPane {
	for i := 0; i < n; i++ {
		p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	return p
}

// TestSchemaPaneScrollsToKeepCursorVisible is the property the pane exists to
// have: moving the cursor past the bottom of a pane shorter than the catalogue
// must bring it back into view rather than leave it drawn off the end.
func TestSchemaPaneScrollsToKeepCursorVisible(t *testing.T) {
	p := schemaPane{tables: tablesNamed(40), expanded: map[int]bool{}}
	p.setSize(10)

	for _, moves := range []int{0, 5, 9, 10, 25, 39} {
		p := down(moves, p)
		rows := strings.Split(ansi.Strip(p.view(30)), "\n")
		if len(rows) > 10 {
			t.Fatalf("%d moves: pane drew %d rows into 10", moves, len(rows))
		}
		want := "t" + strconv.Itoa(moves)
		var found bool
		for _, r := range rows {
			// Guard against t1 matching t10 by requiring the name to end the
			// word, which listRow pads away from its count.
			if strings.Contains(r, want+" ") {
				found = true
			}
		}
		if !found {
			t.Errorf("%d moves: cursor row %q is not on screen:\n%s", moves, want, strings.Join(rows, "\n"))
		}
	}
}

// TestSchemaPaneScrollAccountsForExpandedColumns pins the part that is easy to
// get wrong: the cursor indexes tables, but an expanded table above it adds
// rendered rows, so a scroll offset computed from the table index alone drifts.
func TestSchemaPaneScrollAccountsForExpandedColumns(t *testing.T) {
	p := schemaPane{tables: tablesNamed(20), expanded: map[int]bool{}}
	p.setSize(5)
	// Expand the first table, then walk down past the bottom of the pane.
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.cursorLine() != 0 {
		t.Fatalf("cursorLine at the top = %d, want 0", p.cursorLine())
	}
	p = down(6, p)
	// Six tables down, with one expanded table above, is line 7.
	if got := p.cursorLine(); got != 7 {
		t.Fatalf("cursorLine = %d, want 7 with one expanded table above", got)
	}
	rows := strings.Split(ansi.Strip(p.view(30)), "\n")
	if len(rows) != 5 {
		t.Fatalf("pane drew %d rows into 5", len(rows))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "t6 ") {
		t.Errorf("cursor row t6 is not on screen:\n%s", strings.Join(rows, "\n"))
	}
}

// TestSchemaPaneShortListDoesNotScroll guards the other direction: a catalogue
// that already fits must render from its first row, not be offset by leftover
// scroll state.
func TestSchemaPaneShortListDoesNotScroll(t *testing.T) {
	p := schemaPane{tables: tablesNamed(3), expanded: map[int]bool{}}
	p.setSize(10)
	p = down(2, p)
	rows := strings.Split(ansi.Strip(p.view(30)), "\n")
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want all 3", len(rows))
	}
	if !strings.Contains(rows[0], "t0 ") {
		t.Errorf("a list that fits must start at its first row, got %q", rows[0])
	}
}

// TestSchemaPaneCollapseDoesNotStrandTheOffset covers the case where the list
// shortens under a scrolled pane: collapsing every table must not leave the
// view showing blank rows past the end of the catalogue.
func TestSchemaPaneCollapseDoesNotStrandTheOffset(t *testing.T) {
	p := schemaPane{tables: tablesNamed(8), expanded: map[int]bool{}}
	p.setSize(4)
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyEnter}) // expand table 0
	p = down(7, p)                                       // scroll to the bottom
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyUp})
	for i := 0; i < 7; i++ { // walk back up to the top
		p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse table 0
	if p.scroll.offset != 0 {
		t.Errorf("offset = %d, want 0 once the cursor is back at the top", p.scroll.offset)
	}
}

func TestSavedPaneScrollsToKeepCursorVisible(t *testing.T) {
	var list []savedquery.SavedQuery
	for i := 0; i < 30; i++ {
		list = append(list, savedquery.SavedQuery{Category: "c", Name: "q" + strconv.Itoa(i)})
	}
	p := savedPane{queries: list}
	p.setSize(6)
	for i := 0; i < 20; i++ {
		p, _ = p.update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	rows := strings.Split(ansi.Strip(p.view(30)), "\n")
	if len(rows) != 6 {
		t.Fatalf("pane drew %d rows into 6", len(rows))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "c/q20") {
		t.Errorf("cursor row c/q20 is not on screen:\n%s", strings.Join(rows, "\n"))
	}
}

// TestListPanesGetTheirHeightFromTheLayout ties the scrolling above to the real
// geometry: without this wiring the panes would keep a zero height and never
// scroll at all, however correct listScroll is.
func TestListPanesGetTheirHeightFromTheLayout(t *testing.T) {
	m := newTestModel(t)
	for _, tc := range []struct {
		name string
		got  int
		p    pane
	}{
		{"schema", m.schema.scroll.height, paneSchema},
		{"saved", m.saved.scroll.height, paneSaved},
	} {
		want := contentRows(m.rects[tc.p])
		if tc.got != want {
			t.Errorf("%s pane height = %d, want %d (its rect less the label row)", tc.name, tc.got, want)
		}
		if want <= 0 {
			t.Errorf("%s pane got no content rows at %dx%d", tc.name, m.width, m.height)
		}
	}
}
