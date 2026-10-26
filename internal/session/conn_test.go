package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/pgfake"
)

func setupSession(t *testing.T, h pgfake.Handler) (*Session, *pgfake.Server) {
	t.Helper()
	t.Setenv("DBQ_SESSION_TEST_PW", "pw")
	srv := fakePG(t, h)
	r, code := Setup(CommonFlags{Host: "testpg", Config: testConfig(t), Output: "text", Timeout: 10 * time.Second}, io.Discard)
	if code != 0 {
		t.Fatalf("setup code=%d", code)
	}
	s := New(r)
	t.Cleanup(func() { s.Close() })
	return s, srv
}

func TestSessionOpensLazilyAndOnce(t *testing.T) {
	s, srv := setupSession(t, func(pgfake.Query) pgfake.Reply {
		return pgfake.Rows([]string{"n"}, []string{"1"})
	})
	if srv.Connections() != 0 {
		t.Fatal("a session must not connect before its first run")
	}
	for range 3 {
		if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT 1"}); err != nil {
			t.Fatal(err)
		}
	}
	if srv.Connections() != 1 {
		t.Fatalf("connections = %d, want 1", srv.Connections())
	}
}

func TestSessionReplacesAConnectionLeftInDoubt(t *testing.T) {
	s, srv := setupSession(t, func(q pgfake.Query) pgfake.Reply {
		if strings.Contains(q.SQL, "sleep") {
			return pgfake.Reply{Delay: 10 * time.Second}
		}
		return pgfake.Rows([]string{"n"}, []string{"1"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Execute(ctx, adapter.Query{SQL: "SELECT pg_sleep(10)"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	// The cancelled run's connection is discarded and the next run gets a
	// fresh one rather than an error.
	if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatalf("the run after a cancel failed: %v", err)
	}
	if srv.Connections() != 2 {
		t.Fatalf("connections = %d, want 2", srv.Connections())
	}
}

func TestSessionSerialisesRuns(t *testing.T) {
	s, _ := setupSession(t, func(pgfake.Query) pgfake.Reply {
		return pgfake.Rows([]string{"n"}, []string{"1"})
	})
	// pgconn is not safe for concurrent use, so overlapping runs would race
	// on it; under -race this fails if the session does not serialise them.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT 1"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestSessionCanRunAfterClose(t *testing.T) {
	s, srv := setupSession(t, func(pgfake.Query) pgfake.Reply {
		return pgfake.Rows([]string{"n"}, []string{"1"})
	})
	if err := s.Close(); err != nil {
		t.Fatalf("closing an unopened session: %v", err)
	}
	if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if srv.Connections() != 2 {
		t.Fatalf("connections = %d, want 2", srv.Connections())
	}
}

func TestSessionRefusesBeforeConnecting(t *testing.T) {
	s, srv := setupSession(t, func(pgfake.Query) pgfake.Reply { return pgfake.Reply{} })
	var le *adapter.LocalError
	if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT $1"}); !errors.As(err, &le) {
		t.Fatalf("err = %v, want a LocalError", err)
	}
	// A classifier that decides from the text never makes the session
	// connect either, whatever it decides.
	_, _ = s.Adapter.Classify(context.Background(), "SELECT 1", nil, s)
	if srv.Connections() != 0 {
		t.Fatalf("a refused submission connected: connections = %d", srv.Connections())
	}
}

// stubAdapter opens stubConns and counts how often.
type stubAdapter struct {
	adapter.Adapter // never reached beyond Open and Check
	opened          []*stubConn
}

func (a *stubAdapter) Check(adapter.Query) error { return nil }

func (a *stubAdapter) Open(context.Context, credential.Credential, config.HostConfig) (adapter.Conn, error) {
	c := &stubConn{}
	a.opened = append(a.opened, c)
	return c, nil
}

type stubConn struct {
	dead   bool // the server has dropped it; only a ping can tell
	pinged int
	ran    int
}

func (c *stubConn) Execute(context.Context, adapter.Query) ([]adapter.Rows, error) {
	if c.dead {
		return nil, errors.New("driver: bad connection")
	}
	c.ran++
	return nil, nil
}
func (c *stubConn) Usable() bool { return true }
func (c *stubConn) Ping(context.Context) error {
	c.pinged++
	if c.dead {
		return errors.New("EOF")
	}
	return nil
}
func (c *stubConn) Close() error { return nil }

func TestSessionReplacesAConnectionDroppedWhileIdle(t *testing.T) {
	old := idleCheck
	idleCheck = 0
	defer func() { idleCheck = old }()

	a := &stubAdapter{}
	s := New(Resolved{Adapter: a})
	ctx := context.Background()
	if _, err := s.Execute(ctx, adapter.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	a.opened[0].dead = true // dropped by the server while idle
	if _, err := s.Execute(ctx, adapter.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatalf("the first run after an idle drop failed: %v", err)
	}
	if len(a.opened) != 2 || a.opened[1].ran != 1 {
		t.Fatalf("opened %d connections; want the dead one replaced before the run", len(a.opened))
	}
}

func TestSessionDoesNotPingABusyConnection(t *testing.T) {
	a := &stubAdapter{}
	s := New(Resolved{Adapter: a})
	for range 3 {
		if _, err := s.Execute(context.Background(), adapter.Query{SQL: "SELECT 1"}); err != nil {
			t.Fatal(err)
		}
	}
	if a.opened[0].pinged != 0 {
		t.Fatalf("pinged %d times between back-to-back runs", a.opened[0].pinged)
	}
}
