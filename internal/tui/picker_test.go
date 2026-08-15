package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func pickerText(p picker) string {
	return ansi.Strip(p.View().Content)
}

// TestStartupIntroExplainsWhyItIsAsking is the point of the intro block: a
// user who ran `db-query` with no arguments should not have to guess why a
// list appeared.
func TestStartupIntroExplainsWhyItIsAsking(t *testing.T) {
	p := newHostPicker([]string{"alpha", "beta"}, startupIntro("v1.2.3", "--host or --database was"))
	out := pickerText(p)
	for _, want := range []string{"db-query", "v1.2.3", "No --host or --database was given", "Configured hosts", "alpha", "beta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("picker view missing %q:\n%s", want, out)
		}
	}
}

// TestDatabasePickerNamesItsHost: the heading is what tells the user which
// host's databases these are, in a flow where the host was chosen a moment ago
// and is still on screen above.
func TestDatabasePickerNamesItsHost(t *testing.T) {
	p := newDatabasePicker([]string{"alpha"}, "alpha", "testpg", nil, hostChosenIntro("testpg"))
	out := pickerText(p)
	if !strings.Contains(out, "Databases on testpg") {
		t.Fatalf("view missing the host-named heading:\n%s", out)
	}
	if !strings.Contains(out, "Host") {
		t.Fatalf("view missing the record of the host already chosen:\n%s", out)
	}
	if strings.Contains(out, "no session to open yet") {
		t.Fatalf("the second picker repeated the first one's explanation:\n%s", out)
	}
}

// filterRow isolates the picker's filter line from the footer, which carries
// its own "type to filter" hint and would otherwise match on the word alone.
func filterRow(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "filter") && !strings.Contains(line, "move") {
			return line, true
		}
	}
	return "", false
}

func TestPickerShowsTheFilterOnlyOnceSomethingIsTyped(t *testing.T) {
	p := newHostPicker([]string{"alpha", "beta"}, nil)
	if row, ok := filterRow(pickerText(p)); ok {
		t.Fatalf("the filter line is drawn before anything is typed into it: %q", row)
	}
	p, _ = p.update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	out := pickerText(p)
	row, ok := filterRow(out)
	if !ok || !strings.Contains(row, "b") {
		t.Fatalf("view missing the active filter:\n%s", out)
	}
	if strings.Contains(out, "alpha") {
		t.Fatalf("view still lists a name the filter excludes:\n%s", out)
	}
}

// TestPickerEnterOnNoMatchesDoesNothing: an empty chosen name is how the
// caller reads "the user backed out", so a filter matching nothing must not
// quit the picker and claim that.
func TestPickerEnterOnNoMatchesDoesNothing(t *testing.T) {
	p := newHostPicker([]string{"alpha", "beta"}, nil)
	for _, r := range "zzz" {
		p, _ = p.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	p, cmd := p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.chosen != "" {
		t.Fatalf("chosen = %q, want nothing selected", p.chosen)
	}
	if cmd != nil {
		t.Fatal("Enter quit the picker with no match under the cursor")
	}
}

// TestPickerFiltersThenSelects is the whole feature in one pass: type enough
// to narrow a long list to the wanted name, then take it.
func TestPickerFiltersThenSelects(t *testing.T) {
	p := newHostPicker([]string{"prod-eu", "staging", "prod-us"}, nil)
	for _, r := range "us" {
		p, _ = p.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	p, cmd := p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.chosen != "prod-us" {
		t.Fatalf("chosen = %q, want prod-us", p.chosen)
	}
	if cmd == nil {
		t.Fatal("expected a command signalling selection")
	}
}
