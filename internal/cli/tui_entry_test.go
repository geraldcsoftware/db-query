package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/session"
)

// stubTerminal makes the terminal check report true and replaces the TUI
// launch with a recorder, so the terminal-only branch of Run can be exercised
// without a tty and without starting a Bubble Tea program.
func stubTerminal(t *testing.T) *session.CommonFlags {
	t.Helper()
	realIsTerminal, realLaunch := isTerminal, launchTUI
	t.Cleanup(func() { isTerminal, launchTUI = realIsTerminal, realLaunch })

	var got session.CommonFlags
	isTerminal = func(io.Writer) bool { return true }
	launchTUI = func(c session.CommonFlags, _ string, _, _ io.Writer) int {
		got = c
		return 0
	}
	return &got
}

// TestBareInvocationOpensTheTUI is the regression guard on the entry point:
// `db-query` with no arguments at all must reach the interactive mode on a
// terminal. Returning early on an empty argument list — before the terminal
// check — made the bare command the one invocation that could never open it,
// which is exactly how a user meets this program for the first time.
func TestBareInvocationOpensTheTUI(t *testing.T) {
	launched := stubTerminal(t)
	var stdout, stderr bytes.Buffer

	if code := Run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(nil) = %d, want 0 (the TUI's own exit code)", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("bare invocation printed to stderr: %q", stderr.String())
	}
	if launched.Host != "" {
		t.Errorf("no --host was given, so the TUI must be asked to resolve one, got %q", launched.Host)
	}
}

// TestBareInvocationAndSharedFlagsTakeTheSamePath pins the property the fix
// rests on: an empty argument list is not a special case, it is simply the
// case where parsing yields no command.
func TestBareInvocationAndSharedFlagsTakeTheSamePath(t *testing.T) {
	for _, args := range [][]string{nil, {"--host", "lionel"}} {
		launched := stubTerminal(t)
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%v) = %d, want 0", args, code)
		}
		want := ""
		if len(args) > 0 {
			want = "lionel"
		}
		if launched.Host != want {
			t.Errorf("Run(%v) launched the TUI with host %q, want %q", args, launched.Host, want)
		}
	}
}

// TestBareInvocationOffATerminalStillPrintsUsage keeps the other half of the
// branch honest: piped or redirected, there is nothing interactive to open.
func TestBareInvocationOffATerminalStillPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("Run(nil) off a terminal = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected usage on stderr, got %q", stderr.String())
	}
}
