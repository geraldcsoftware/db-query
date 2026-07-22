package credential

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// resolverTimeout bounds every backend CLI call so a locked vault or
// wedged agent produces a clear error instead of a hang.
const resolverTimeout = 15 * time.Second

// runBackend shells out to a resolver's backing CLI. env overlays the
// subprocess environment on top of os.Environ() (a resolver uses it to pass a
// secret the child reads from its environment, e.g. BWS_ACCESS_TOKEN — never
// on argv). Swappable in tests.
var runBackend = func(env map[string]string, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("%s CLI not found in PATH: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolverTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		merged := os.Environ()
		for k, v := range env {
			merged = append(merged, k+"="+v) // later duplicate wins in exec
		}
		cmd.Env = merged
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s timed out after %s (locked vault or unreachable backend?)", name, resolverTimeout)
		}
		msg := bytes.TrimSpace(errb.Bytes())
		if len(msg) > 0 {
			return nil, fmt.Errorf("%s: %s", name, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out.Bytes(), nil
}
