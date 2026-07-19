package credential

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// resolverTimeout bounds every backend CLI call so a locked vault or
// wedged agent produces a clear error instead of a hang.
const resolverTimeout = 15 * time.Second

// runBackend shells out to a resolver's backing CLI. Swappable in tests.
var runBackend = func(name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("%s CLI not found in PATH: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolverTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
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
