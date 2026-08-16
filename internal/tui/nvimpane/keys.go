package nvimpane

import (
	tea "charm.land/bubbletea/v2"
)

// keyNames maps Bubble Tea's special key codes onto Neovim's <> notation. Keys
// absent from it are printable characters, which carry themselves.
var keyNames = map[rune]string{
	tea.KeyEnter: "CR", tea.KeyTab: "Tab", tea.KeyBackspace: "BS",
	tea.KeyEscape: "Esc", tea.KeySpace: "Space",
	tea.KeyUp: "Up", tea.KeyDown: "Down", tea.KeyLeft: "Left", tea.KeyRight: "Right",
	tea.KeyHome: "Home", tea.KeyEnd: "End", tea.KeyPgUp: "PageUp", tea.KeyPgDown: "PageDown",
	tea.KeyInsert: "Insert", tea.KeyDelete: "Del",
	tea.KeyF1: "F1", tea.KeyF2: "F2", tea.KeyF3: "F3", tea.KeyF4: "F4", tea.KeyF5: "F5",
	tea.KeyF6: "F6", tea.KeyF7: "F7", tea.KeyF8: "F8", tea.KeyF9: "F9",
	tea.KeyF10: "F10", tea.KeyF11: "F11", tea.KeyF12: "F12",
}

// literalEscapes are the two characters that cannot be sent as themselves: <
// opens Neovim's own notation, and a backslash escapes within it.
var literalEscapes = map[rune]string{'<': "<LT>", '\\': "<Bslash>"}

// Notation renders one key press as Neovim input notation.
//
// A bare Shift on a printable character is already carried by the shifted
// glyph, and Neovim reads <S-a> as byte-identical to a plain A, so only Ctrl,
// Alt and Super take the <mod-key> form. Bubble Tea normalises the terminal's
// legacy and Kitty encodings into the same Key before this sees one, so there
// is no protocol branching here.
func Notation(k tea.Key) string {
	if name, ok := keyNames[k.Code]; ok {
		return "<" + modPrefix(k) + name + ">"
	}

	text := k.Text
	if text == "" {
		text = string(k.Code)
	}

	if k.Mod&^tea.ModShift == 0 {
		if esc, ok := literalEscapes[k.Code]; ok {
			return esc
		}
		return text
	}

	if esc, ok := literalEscapes[k.Code]; ok {
		text = esc
	}
	return "<" + modPrefix(k) + text + ">"
}

func modPrefix(k tea.Key) string {
	var mod string
	if k.Mod.Contains(tea.ModCtrl) {
		mod += "C-"
	}
	if k.Mod.Contains(tea.ModAlt) {
		mod += "M-"
	}
	if k.Mod.Contains(tea.ModShift) {
		mod += "S-"
	}
	if k.Mod.Contains(tea.ModSuper) {
		mod += "D-"
	}
	return mod
}
