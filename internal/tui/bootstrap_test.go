package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHostPickerEnterSelects(t *testing.T) {
	p := newHostPicker([]string{"alpha", "beta", "gamma"})
	// Move down once (alpha -> beta), then select.
	var cmd tea.Cmd
	p, cmd = p.update(tea.KeyPressMsg{Code: tea.KeyDown})
	p, cmd = p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.chosen != "beta" {
		t.Fatalf("chosen = %q, want beta", p.chosen)
	}
	if cmd == nil {
		t.Fatal("expected a command signalling selection")
	}
}

func TestHostPickerEscQuits(t *testing.T) {
	p := newHostPicker([]string{"alpha"})
	_, cmd := p.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", msg)
	}
}
