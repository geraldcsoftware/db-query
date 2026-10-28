package tui

import (
	"context"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/session"
)

type idleAdapter struct{ adapter.Adapter }

func (idleAdapter) Open(context.Context, credential.Credential, config.HostConfig) (adapter.Conn, error) {
	return idleConn{}, nil
}

type idleConn struct{ adapter.Conn }

func (idleConn) Usable() bool { return true }
func (idleConn) Close() error { return nil }

// TestQuitDoesNotWaitOnAStuckRun pins that leaving the program is bounded: a
// run the driver is slow to abandon holds the session, and Close waits for it,
// so the exit path must stop waiting rather than hang the terminal.
func TestQuitDoesNotWaitOnAStuckRun(t *testing.T) {
	s := session.New(session.Resolved{Adapter: idleAdapter{}})
	stuck, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		_ = s.With(context.Background(), func(adapter.Conn) error {
			close(stuck)
			<-release
			return nil
		})
	}()
	<-stuck

	start := time.Now()
	closeBounded(s, 100*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("closing behind a stuck run took %s", took)
	}
}
