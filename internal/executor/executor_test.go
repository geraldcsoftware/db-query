package executor

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRunSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Invocation{
		Argv:  []string{"cat"},
		Stdin: strings.NewReader("hello over stdin"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "hello over stdin" {
		t.Fatalf("res = %+v", res)
	}
}

// A client that ran and exited nonzero is data, not a Go error — the
// schema-error detection path depends on this fork.
func TestRunNonzeroExitIsData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Invocation{Argv: []string{"sh", "-c", "echo oops >&2; exit 3"}})
	if err != nil {
		t.Fatalf("nonzero exit must not be a Go error, got %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "oops") {
		t.Fatalf("stderr = %q", res.Stderr)
	}
}

func TestRunFailToStartIsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Run(ctx, Invocation{Argv: []string{"definitely-not-a-binary-xyz"}})
	if err == nil || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("want starting error, got %v", err)
	}
}

func TestRunEmptyInvocation(t *testing.T) {
	if _, err := Run(context.Background(), Invocation{}); err == nil {
		t.Fatal("want error for empty argv")
	}
}

func TestRunTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, Invocation{Argv: []string{"sleep", "30"}})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not kill the child promptly")
	}
}

func TestRunEnvOverlay(t *testing.T) {
	t.Setenv("DBQ_BASE_VAR", "from-shell")
	t.Setenv("DBQ_OVERRIDDEN", "shell-value")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Invocation{
		Argv: []string{"sh", "-c", "printf '%s|%s|%s' \"$DBQ_BASE_VAR\" \"$DBQ_OVERRIDDEN\" \"$DBQ_NEW\""},
		Env:  map[string]string{"DBQ_OVERRIDDEN": "overlay-value", "DBQ_NEW": "added"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "from-shell|overlay-value|added" {
		t.Fatalf("child env = %q", got)
	}
}

func TestMergeEnvDeterministicDedup(t *testing.T) {
	base := []string{"A=1", "B=2", "A=0", "MALFORMED"}
	merged := mergeEnv(base, map[string]string{"B": "9", "C": "3"})
	sort.Strings(merged)
	got := strings.Join(merged, ",")
	// A deduped, overlay wins for B, malformed entries dropped.
	want := "A=0,B=9,C=3"
	if got != want {
		t.Fatalf("mergeEnv = %q, want %q", got, want)
	}
}
