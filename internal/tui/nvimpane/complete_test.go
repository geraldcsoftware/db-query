package nvimpane

import (
	"reflect"
	"testing"
)

// stubSource records the context it was asked about and answers with a fixed
// list, so the protocol either side of the candidate source can be tested
// without one.
type stubSource struct {
	qualifier, prefix string
	buflines          []string
	answer            []map[string]any
}

func (s *stubSource) Candidates(qualifier, prefix string, buflines []string) []map[string]any {
	s.qualifier, s.prefix, s.buflines = qualifier, prefix, buflines
	return s.answer
}

// TestFindstartReturnsWhereTheCompletedTextBegins covers the first of the two
// calls Neovim makes. The offset is zero-based bytes from the start of the
// line, and it is what decides how much of what was typed a chosen candidate
// replaces.
func TestFindstartReturnsWhereTheCompletedTextBegins(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		col  int // 1-based, as vim's col('.') reports it
		want int
	}{
		{"mid word", "select cust", 12, 7},
		{"start of line", "sel", 4, 0},
		{"after a space", "select ", 8, 7},
		{"after a dot", "select c.na", 12, 9},
		{"immediately after a dot", "select c.", 10, 9},
		{"empty line", "", 1, 0},
		{"underscores and digits are part of a word", "select order_2x", 16, 7},
		{"a column past the end of the line is clamped", "select", 40, 0},
		{"a column before the start is clamped", "select", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := complete(&stubSource{}, 1, tc.line, tc.col, []string{tc.line})
			if got != tc.want {
				t.Errorf("findstart on %q at col %d = %v, want %d", tc.line, tc.col, got, tc.want)
			}
		})
	}
}

// TestTheSecondCallCarriesTheContextTheSourceNeeds: the prefix is re-derived
// from the live line and column on every call, never taken from Neovim's base
// argument, which under refresh "always" stays frozen at the text captured on
// the session's first call.
func TestTheSecondCallCarriesTheContextTheSourceNeeds(t *testing.T) {
	for _, tc := range []struct {
		name              string
		line              string
		col               int
		qualifier, prefix string
	}{
		{"bare word", "select cust", 12, "", "cust"},
		{"qualified", "select c.na", 12, "c", "na"},
		{"qualified with nothing typed yet", "select c.", 10, "c", ""},
		{"a table name as the qualifier", "select customers.na", 20, "customers", "na"},
		{"no word under the cursor", "select ", 8, "", ""},
		{"a dot after a non-word is not a qualifier", "select 1.na", 12, "1", "na"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &stubSource{}
			buf := []string{tc.line, "from customers c"}
			complete(src, 0, tc.line, tc.col, buf)

			if src.qualifier != tc.qualifier || src.prefix != tc.prefix {
				t.Errorf("source saw qualifier %q prefix %q, want %q and %q",
					src.qualifier, src.prefix, tc.qualifier, tc.prefix)
			}
			if !reflect.DeepEqual(src.buflines, buf) {
				t.Errorf("source saw %q, want the whole buffer", src.buflines)
			}
		})
	}
}

// TestTheReplyAsksToBeCalledAgain: refresh "always" is what makes Neovim call
// back on every keystroke instead of filtering its first answer itself, which
// is what lets a qualifier appearing mid-word change the candidate set.
func TestTheReplyAsksToBeCalledAgain(t *testing.T) {
	want := []map[string]any{{"word": "customers"}}
	got, ok := complete(&stubSource{answer: want}, 0, "select cust", 12, nil).(map[string]any)
	if !ok {
		t.Fatalf("the second call answered with %T, want a dictionary", got)
	}
	if got["refresh"] != "always" {
		t.Errorf("refresh = %v, want always", got["refresh"])
	}
	if !reflect.DeepEqual(got["words"], want) {
		t.Errorf("words = %v, want the source's own answer", got["words"])
	}
}

// TestNoSourceIsAWorkingPaneWithAnEmptyPopup rather than a broken one: the
// reply still has to be the shape Neovim expects, or completion errors in the
// editor on every keystroke.
func TestNoSourceIsAWorkingPaneWithAnEmptyPopup(t *testing.T) {
	got, ok := complete(nil, 0, "select cust", 12, nil).(map[string]any)
	if !ok {
		t.Fatalf("answered with %T, want a dictionary", got)
	}
	words, ok := got["words"].([]map[string]any)
	if !ok || len(words) != 0 {
		t.Errorf("words = %v, want an empty list", got["words"])
	}
	if start := complete(nil, 1, "select cust", 12, nil); start != 7 {
		t.Errorf("findstart without a source = %v, want it answered anyway", start)
	}
}
