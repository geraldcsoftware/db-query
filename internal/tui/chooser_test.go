package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func typeFilter(c chooser, s string) chooser {
	for _, r := range s {
		c, _ = c.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return c
}

func TestChooserFilterIsCaseInsensitiveSubstring(t *testing.T) {
	c := newChooser([]string{"Reporting", "prod_eu", "staging", "PRODUCTION"}, 10)
	c = typeFilter(c, "prod")
	if got := c.matches(); !reflect.DeepEqual(got, []string{"prod_eu", "PRODUCTION"}) {
		t.Fatalf("matches = %v, want the two prod databases in list order", got)
	}
}

// TestChooserFilterResetsTheCursor: the name the cursor was on may not survive
// the filter, so it starts again at the first match rather than landing
// somewhere the user cannot predict.
func TestChooserFilterResetsTheCursor(t *testing.T) {
	c := newChooser([]string{"alpha", "beta", "gamma"}, 10)
	c.move(2)
	if c.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 before filtering", c.cursor)
	}
	c = typeFilter(c, "a")
	if c.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after the filter changed", c.cursor)
	}
	if name, _ := c.selected(); name != "alpha" {
		t.Fatalf("selected = %q, want alpha", name)
	}
}

// TestChooserSelectionFollowsTheFilteredList is the off-by-one this widget
// exists to prevent: with a filter applied the cursor indexes the matches, not
// the original slice, so Enter cannot return the name that happens to sit at
// the same position in the unfiltered list.
func TestChooserSelectionFollowsTheFilteredList(t *testing.T) {
	c := newChooser([]string{"alpha", "beta", "gamma", "delta"}, 10)
	c = typeFilter(c, "a") // alpha, beta, gamma, delta all contain "a"
	c = typeFilter(c, "m") // "am" leaves gamma alone
	if got := c.matches(); !reflect.DeepEqual(got, []string{"gamma"}) {
		t.Fatalf("matches = %v, want [gamma]", got)
	}
	if name, ok := c.selected(); !ok || name != "gamma" {
		t.Fatalf("selected = %q/%v, want gamma/true", name, ok)
	}
}

func TestChooserCursorWrapsAtBothEnds(t *testing.T) {
	c := newChooser([]string{"alpha", "beta", "gamma"}, 10)
	c.move(-1)
	if c.cursor != 2 {
		t.Fatalf("cursor = %d after up from the top, want 2 (wrapped to the end)", c.cursor)
	}
	c.move(1)
	if c.cursor != 0 {
		t.Fatalf("cursor = %d after down from the end, want 0 (wrapped to the top)", c.cursor)
	}
}

// TestChooserPageClampsInsteadOfWrapping: PgDn means "a long way down", and
// wrapping it to the top would overshoot the end it was heading for.
func TestChooserPageClampsInsteadOfWrapping(t *testing.T) {
	c := newChooser([]string{"a", "b", "c", "d", "e"}, 2)
	c.page(10)
	if c.cursor != 4 {
		t.Fatalf("cursor = %d, want 4 (clamped to the last row)", c.cursor)
	}
	c.page(-10)
	if c.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (clamped to the first row)", c.cursor)
	}
}

func TestChooserBackspaceAndClearEditTheFilter(t *testing.T) {
	c := newChooser([]string{"alpha", "beta"}, 10)
	c = typeFilter(c, "alp")
	c, _ = c.update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if c.filter != "al" {
		t.Fatalf("filter = %q, want al", c.filter)
	}
	c, _ = c.update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if c.filter != "" {
		t.Fatalf("filter = %q, want it cleared", c.filter)
	}
	if got := c.matches(); len(got) != 2 {
		t.Fatalf("matches = %v, want both names back", got)
	}
}

// TestChooserDoesNotConsumeKeysItHasNoUseFor: the switcher popup layers its own
// bindings over this widget, so a key it does not handle has to come back
// unclaimed rather than being swallowed.
func TestChooserDoesNotConsumeKeysItHasNoUseFor(t *testing.T) {
	c := newChooser([]string{"alpha"}, 10)
	_, consumed := c.update(tea.KeyPressMsg{Code: tea.KeyF2})
	if consumed {
		t.Fatal("F2 was consumed by the chooser; the owner would never see it")
	}
}

func TestChooserSelectedIsEmptyWhenTheFilterMatchesNothing(t *testing.T) {
	c := newChooser([]string{"alpha", "beta"}, 10)
	c = typeFilter(c, "zzz")
	if name, ok := c.selected(); ok {
		t.Fatalf("selected = %q/%v, want an empty selection", name, ok)
	}
	rows := c.view(40)
	if len(rows) != 1 || !strings.Contains(ansi.Strip(rows[0]), "no matches") {
		t.Fatalf("view = %q, want a single 'no matches' row", rows)
	}
}

func TestChooserCountLabelShowsMatchesAgainstTotal(t *testing.T) {
	c := newChooser([]string{"alpha", "beta", "gamma"}, 10)
	if got := c.countLabel(); got != "3" {
		t.Fatalf("countLabel = %q, want 3 when nothing is filtered", got)
	}
	c = typeFilter(c, "a")
	if got := c.countLabel(); got != "3/3" {
		t.Fatalf("countLabel = %q, want 3/3", got)
	}
	c = typeFilter(c, "lph")
	if got := c.countLabel(); got != "1/3" {
		t.Fatalf("countLabel = %q, want 1/3", got)
	}
}

func TestChooserRendersMarksAgainstTheirNames(t *testing.T) {
	c := newChooser([]string{"alpha", "beta"}, 10)
	c.marks = map[string]string{"beta": "no schema"}
	rows := c.view(40)
	if strings.Contains(ansi.Strip(rows[0]), "no schema") {
		t.Fatal("alpha carries a mark it was not given")
	}
	if !strings.Contains(ansi.Strip(rows[1]), "no schema") {
		t.Fatalf("beta row = %q, want its mark", ansi.Strip(rows[1]))
	}
}

// TestChooserWindowsALongListAroundTheCursor: the startup picker is inline
// output, so a two-hundred-name list must scroll within its window rather than
// print itself into the user's scrollback.
func TestChooserWindowsALongListAroundTheCursor(t *testing.T) {
	names := make([]string, 50)
	for i := range names {
		names[i] = "db" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	c := newChooser(names, 5)
	c.move(30)
	rows := c.view(40)
	if len(rows) != 5 {
		t.Fatalf("view returned %d rows, want the window's 5", len(rows))
	}
	if !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), names[30]) {
		t.Fatal("the cursor's row is not inside the window")
	}
}
