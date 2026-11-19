package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/pgfake"
)

// stubTerminal makes the terminal check report true, so the terminal-only
// branches of Run can be exercised without a tty.
func stubTerminal(t *testing.T) {
	t.Helper()
	realIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = realIsTerminal })
	isTerminal = func(io.Writer) bool { return true }
}

// TestBareInvocationOnATerminalPrintsUsage pins that running no command with
// nothing on stdin is a usage error even on a terminal, with or without shared
// flags: there is nothing to run, so nothing opens.
func TestBareInvocationOnATerminalPrintsUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--host", "lionel"}} {
		stubTerminal(t)
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 1 {
			t.Fatalf("Run(%v) on a terminal = %d, want 1", args, code)
		}
		if !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("Run(%v): expected usage on stderr, got %q", args, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%v) printed to stdout: %q", args, stdout.String())
		}
	}
}

// TestBareInvocationOffATerminalPrintsUsage is the same outcome with stdout
// piped or redirected.
func TestBareInvocationOffATerminalPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("Run(nil) off a terminal = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected usage on stderr, got %q", stderr.String())
	}
}

func TestPipedSQLRunsWithNoCommand(t *testing.T) {
	seedSchemaCache(t)
	needsClassifier(t)
	srv := fakePG(t, replyAll(pgfake.Rows([]string{"ok"}, []string{"1"})))
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	stubTerminal(t)

	f, err := os.Open(writeFile(t, t.TempDir(), "q.sql", "SELECT 1", 0o600))
	if err != nil {
		t.Fatal(err)
	}
	realStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = realStdin; f.Close() })
	// stdout is a terminal; stdin is the redirected file.
	isTerminal = func(w io.Writer) bool { return w != io.Writer(os.Stdin) }

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--host", "testpg", "--config", cfg}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d err=%q", code, stderr.String())
	}
	if q := srv.Queries(); len(q) != 1 || q[0].SQL != "SELECT 1" {
		t.Fatalf("stdin SQL not run; sent %+v", q)
	}
}
