package session

import (
	"context"
	"sync"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// Session is a resolved host together with the one connection every run
// against it shares. The connection is opened on first use, so a command that
// never reaches the server never connects, and it is replaced when the adapter
// reports it unusable, so one cancelled run does not strand the next.
//
// Runs are serialised: a Session is safe to share between goroutines, and a
// second run waits for the first. Close it when done.
type Session struct {
	Resolved

	mu     sync.Mutex
	conn   adapter.Conn
	opened bool // a connection has been opened at some point
}

// New wraps r in a Session. Nothing is opened until the first run.
func New(r Resolved) *Session { return &Session{Resolved: r} }

// With runs f on the session's connection, opening or replacing it first. It
// makes a Session an adapter.Runner, which is how a classifier that asks the
// engine's planner reaches the server.
func (s *Session) With(ctx context.Context, f func(adapter.Conn) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && !s.conn.Usable() {
		_ = s.conn.Close() // it is being discarded; how it closes changes nothing
		s.conn = nil
	}
	if s.conn == nil {
		c, err := s.Adapter.Open(ctx, s.Cred, s.Host)
		if err != nil {
			return err
		}
		s.conn = c
		s.opened = true
	}
	return f(s.conn)
}

// Opened reports whether a connection was opened at any point in the
// session's life, including one since closed or replaced.
func (s *Session) Opened() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened
}

// Run runs q on the session's connection and returns its result sets with the
// server's report on each statement, and how long the run took once the
// connection was open. A submission the adapter refuses as given is refused
// before the connection is opened, so it never reaches the server.
func (s *Session) Run(ctx context.Context, q adapter.Query) (adapter.Result, error) {
	if err := s.Adapter.Check(q); err != nil {
		return adapter.Result{}, err
	}
	var res adapter.Result
	err := s.With(ctx, func(c adapter.Conn) error {
		start := time.Now()
		var err error
		res, err = c.Execute(ctx, q)
		res.Elapsed = time.Since(start)
		return err
	})
	return res, err
}

// Close closes the connection, if one was opened. A Session can be used again
// after Close; the next run opens a new connection.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}
