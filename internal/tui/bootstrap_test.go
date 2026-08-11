package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHostPickerEnterSelects(t *testing.T) {
	p := newHostPicker([]string{"alpha", "beta", "gamma"})
	// Move down once (alpha -> beta), then select.
	var cmd tea.Cmd
	p, cmd = p.update(tea.KeyMsg{Type: tea.KeyDown})
	p, cmd = p.update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.chosen != "beta" {
		t.Fatalf("chosen = %q, want beta", p.chosen)
	}
	if cmd == nil {
		t.Fatal("expected a command signalling selection")
	}
}

func TestHostPickerEscQuits(t *testing.T) {
	p := newHostPicker([]string{"alpha"})
	_, cmd := p.update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", msg)
	}
}
