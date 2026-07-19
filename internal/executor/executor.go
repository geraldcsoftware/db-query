// Package executor is the provider-blind bottom of the stack: adapters
// build an Invocation, Run executes it and returns raw bytes. It knows
// nothing about providers, credentials, or output formats.
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Invocation is everything needed to run a client once.
type Invocation struct {
	Argv  []string          // Argv[0] is the client binary
	Env   map[string]string // overlay applied on top of os.Environ()
	Stdin io.Reader         // the SQL, piped — never on argv
}

// RawResult is the unparsed outcome of a client run.
type RawResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run executes the invocation under ctx (callers always pass a deadline —
// no invocation runs unbounded). "Failed to start" (binary not found,
// permission denied) is a Go error: the tool malfunctioned. "Ran and
// exited nonzero" is NOT an error: it is data the caller interprets
// (schema-error detection depends on this distinction).
func Run(ctx context.Context, inv Invocation) (RawResult, error) {
	if len(inv.Argv) == 0 {
		return RawResult{}, fmt.Errorf("empty invocation")
	}
	cmd := exec.CommandContext(ctx, inv.Argv[0], inv.Argv[1:]...)
	cmd.Env = mergeEnv(os.Environ(), inv.Env)
	cmd.Stdin = inv.Stdin

	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb

	err := cmd.Run()
	res := RawResult{Stdout: out.Bytes(), Stderr: errb.Bytes()}

	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		if ctx.Err() == context.DeadlineExceeded {
			return res, fmt.Errorf("%s timed out: %w", inv.Argv[0], ctx.Err())
		}
		res.ExitCode = ee.ExitCode()
		return res, nil // ran, returned nonzero — that's data, not a failure
	case err != nil:
		return res, fmt.Errorf("starting %s: %w", inv.Argv[0], err)
	default:
		return res, nil
	}
}

// mergeEnv deduplicates base+overlay into a deterministic environment.
// Duplicate-key resolution in a child process is platform-dependent, so
// we build a map (overlay wins) and flatten. os.Environ() stays the base
// — it carries PATH and the direnv-loaded vars env: depends on.
func mergeEnv(base []string, overlay map[string]string) []string {
	m := make(map[string]string, len(base)+len(overlay))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	for k, v := range overlay {
		m[k] = v // overlay wins
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
