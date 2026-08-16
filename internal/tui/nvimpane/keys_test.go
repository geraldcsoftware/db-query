package nvimpane

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNotationRendersKeysAsNeovimWritesThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.Key
		want string
	}{
		{"plain character", tea.Key{Code: 'a', Text: "a"}, "a"},
		{"digit", tea.Key{Code: '7', Text: "7"}, "7"},
		{"enter", tea.Key{Code: tea.KeyEnter}, "<CR>"},
		{"escape", tea.Key{Code: tea.KeyEscape}, "<Esc>"},
		{"backspace", tea.Key{Code: tea.KeyBackspace}, "<BS>"},
		{"tab", tea.Key{Code: tea.KeyTab}, "<Tab>"},
		{"space", tea.Key{Code: tea.KeySpace}, "<Space>"},
		{"page up", tea.Key{Code: tea.KeyPgUp}, "<PageUp>"},
		{"page down", tea.Key{Code: tea.KeyPgDown}, "<PageDown>"},
		{"delete", tea.Key{Code: tea.KeyDelete}, "<Del>"},
		{"arrow", tea.Key{Code: tea.KeyLeft}, "<Left>"},
		{"function key", tea.Key{Code: tea.KeyF5}, "<F5>"},

		{"ctrl chord", tea.Key{Code: 'w', Text: "w", Mod: tea.ModCtrl}, "<C-w>"},
		{"alt chord", tea.Key{Code: 'b', Text: "b", Mod: tea.ModAlt}, "<M-b>"},
		{"super chord", tea.Key{Code: 'k', Text: "k", Mod: tea.ModSuper}, "<D-k>"},
		{"ctrl on a named key", tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}, "<C-CR>"},
		{"two modifiers", tea.Key{Code: 'x', Text: "x", Mod: tea.ModCtrl | tea.ModAlt}, "<C-M-x>"},

		// A bare Shift is already carried by the shifted glyph, and Neovim reads
		// <S-a> as byte-identical to a plain A, so the character stands alone.
		{"shifted letter", tea.Key{Code: 'a', Text: "A", Mod: tea.ModShift}, "A"},
		{"shifted symbol", tea.Key{Code: '1', Text: "!", Mod: tea.ModShift}, "!"},
		{"shift with ctrl still names both", tea.Key{Code: 'a', Text: "A", Mod: tea.ModCtrl | tea.ModShift}, "<C-S-A>"},

		// The two characters that cannot be sent as themselves: < opens Neovim's
		// own notation, and a backslash escapes within it.
		{"less than", tea.Key{Code: '<', Text: "<"}, "<LT>"},
		{"backslash", tea.Key{Code: '\\', Text: "\\"}, "<Bslash>"},

		// Text is empty for a key the terminal reported by code alone.
		{"code without text", tea.Key{Code: 'z'}, "z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Notation(tc.key); got != tc.want {
				t.Errorf("Notation(%+v) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}
