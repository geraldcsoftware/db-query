package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/tui/nvimpane"
)

// TestAMachineWithoutNeovimGetsTheTextarea is the fallback, and it has to be
// silent: nothing to configure, nothing to read, and exactly the pane db-query
// shipped before the embedded editor existed.
func TestAMachineWithoutNeovimGetsTheTextarea(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory, so no binary is found

	editor, notice := newQueryEditor()
	t.Cleanup(editor.close)

	if _, ok := editor.(*textareaEditor); !ok {
		t.Fatalf("editor = %T, want the textarea", editor)
	}
	if notice != "" {
		t.Errorf("the fallback said %q, and it is meant to say nothing", notice)
	}
	if editor.modal() {
		t.Error("the textarea reported itself modal, which would hand it keys it has no use for")
	}
}

// TestAnUnusableBinaryFallsBackToo: the gate is one branch, so a binary that is
// too old, is not Neovim, or cannot answer all land in the same place.
func TestAnUnusableBinaryFallsBackToo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nvim"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	editor, notice := newQueryEditor()
	t.Cleanup(editor.close)

	if _, ok := editor.(*textareaEditor); !ok {
		t.Fatalf("editor = %T, want the textarea", editor)
	}
	if notice != "" {
		t.Errorf("the fallback said %q", notice)
	}
}

// TestTheEmbeddedEditorNoticesAnAbsentUserConfig: the file is optional and
// db-query never writes it, so its absence is worth one line in the status
// strip and nothing more.
func TestTheEmbeddedEditorNoticesAnAbsentUserConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	editor, notice := newQueryEditor()
	t.Cleanup(editor.close)
	if _, ok := editor.(*nvimEditor); !ok {
		t.Skipf("no usable neovim on this machine, so there is nothing to notice")
	}
	if !strings.Contains(notice, nvimpane.ConfigPath()) {
		t.Errorf("notice = %q, want it to name the file the user may create", notice)
	}
}

// TestAKeyReleaseNeverReachesNeovim: nvim_input has no notion of one, so
// forwarding a release would type the key a second time.
//
// The editor here has no session at all, which is the assertion: anything the
// release path did touch would be a nil dereference. The key press below proves
// the check is not passing for want of a path to Neovim in the first place.
func TestAKeyReleaseNeverReachesNeovim(t *testing.T) {
	editor := &nvimEditor{grid: nvimpane.NewGrid(8, 1)}

	if cmd := editor.update(tea.KeyReleaseMsg{Code: 'a', Text: "a"}); cmd != nil {
		t.Errorf("a key release produced a command: %v", cmd)
	}

	if !panics(func() { editor.update(tea.KeyPressMsg{Code: 'a', Text: "a"}) }) {
		t.Error("a key press did not go looking for Neovim, so the release proved nothing")
	}
}

// TestARedrawIsAppliedWithoutTouchingNeovim: the frame is rebuilt from the
// batch alone, which is what lets it be applied on the event loop's goroutine
// while the RPC stream carries on.
func TestARedrawIsAppliedWithoutTouchingNeovim(t *testing.T) {
	editor := &nvimEditor{grid: nvimpane.NewGrid(8, 1)}

	editor.update(nvimRedrawMsg{Events: [][]any{
		{"grid_line", []any{int64(1), int64(0), int64(0), []any{
			[]any{"o", int64(0)}, []any{"k", int64(0)},
		}}},
		{"flush"},
	}})
	if !strings.Contains(editor.view(true), "ok") {
		t.Errorf("the frame is %q, want the batch painted into it", editor.view(true))
	}
}

func panics(f func()) (did bool) {
	defer func() { did = recover() != nil }()
	f()
	return false
}
