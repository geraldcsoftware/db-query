// Package pgfake is a PostgreSQL server for tests: just enough of the wire
// protocol for the postgres adapter to connect, authenticate, run statements
// and cancel them, with every reply decided by the test.
//
// It stands where a stub psql on PATH used to. Each test supplies a Handler
// that answers one statement at a time, and reads back what arrived: the
// statements with their bound parameters, the startup parameters (database,
// user, application name, options) and the password.
package pgfake

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// Query is one statement as the server received it.
type Query struct {
	SQL string
	// Params holds the bound values in position order, nil for a NULL.
	Params [][]byte
	// ParamOIDs holds the declared parameter types, 0 where the client left
	// the server to infer one.
	ParamOIDs []uint32
	// Simple marks a statement sent through the simple protocol, which the
	// adapter uses only for transaction control.
	Simple bool
}

// Reply is a Handler's answer to one statement. A zero Reply is a statement
// that succeeded with no result columns.
type Reply struct {
	Columns []string
	Rows    [][]*string

	// OIDs, when set, declares each column's type; a column without one is
	// text (OID 25).
	OIDs []uint32

	// Code and Message, when Code is set, make the statement fail with that
	// SQLSTATE. Detail and Hint ride along when set.
	Code, Message, Detail, Hint string

	// Delay holds the reply back, as a slow statement would. A cancel request
	// from the client ends the wait with SQLSTATE 57014.
	Delay time.Duration
}

// Handler answers one statement.
type Handler func(Query) Reply

// Rows is a Reply carrying a result set, for the common case of a handler
// that returns columns and text values. Each row is given as its values; use
// Null for a NULL.
func Rows(columns []string, rows ...[]string) Reply {
	r := Reply{Columns: columns}
	for _, row := range rows {
		out := make([]*string, len(row))
		for i, v := range row {
			if v != Null {
				s := v
				out[i] = &s
			}
		}
		r.Rows = append(r.Rows, out)
	}
	return r
}

// Null stands for a NULL value in a row passed to Rows.
const Null = "\x00null\x00"

// Error is a Reply that fails with the given SQLSTATE and message.
func Error(code, message string) Reply { return Reply{Code: code, Message: message} }

// Server is a running fake. Its methods are safe to call while it serves.
type Server struct {
	// Port is where it listens, on 127.0.0.1.
	Port int

	handler  Handler
	listener net.Listener

	mu        sync.Mutex
	queries   []Query
	startup   map[string]string
	password  string
	cancels   map[uint32]context.CancelFunc
	nextPID   uint32
	connCount int
	wg        sync.WaitGroup
}

// Start runs a fake until the test ends.
func Start(t testing.TB, h Handler) *Server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Port:     l.Addr().(*net.TCPAddr).Port,
		handler:  h,
		listener: l,
		cancels:  map[uint32]context.CancelFunc{},
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(func() {
		l.Close()
		s.mu.Lock()
		for _, cancel := range s.cancels {
			cancel()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

// Queries returns every statement received so far, in arrival order,
// transaction control included.
func (s *Server) Queries() []Query {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Query(nil), s.queries...)
}

// SQL returns the text of every statement received so far, in order.
func (s *Server) SQL() []string {
	var out []string
	for _, q := range s.Queries() {
		out = append(out, q.SQL)
	}
	return out
}

// Startup returns the startup parameters of the most recent connection.
func (s *Server) Startup() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.startup {
		out[k] = v
	}
	return out
}

// Password returns the password the most recent connection sent.
func (s *Server) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// Connections reports how many connections have completed startup.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connCount
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			s.serve(c)
		}()
	}
}

func (s *Server) serve(c net.Conn) {
	be := pgproto3.NewBackend(c, c)
	var startup *pgproto3.StartupMessage
	for startup == nil {
		msg, err := be.ReceiveStartupMessage()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *pgproto3.SSLRequest, *pgproto3.GSSEncRequest:
			if _, err := c.Write([]byte("N")); err != nil {
				return
			}
		case *pgproto3.CancelRequest:
			s.mu.Lock()
			if cancel, ok := s.cancels[m.ProcessID]; ok {
				cancel()
			}
			s.mu.Unlock()
			return
		case *pgproto3.StartupMessage:
			startup = m
		default:
			return
		}
	}

	be.Send(&pgproto3.AuthenticationCleartextPassword{})
	if be.Flush() != nil || be.SetAuthType(pgproto3.AuthTypeCleartextPassword) != nil {
		return
	}
	msg, err := be.Receive()
	if err != nil {
		return
	}
	pw, ok := msg.(*pgproto3.PasswordMessage)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.startup = startup.Parameters
	s.password = pw.Password
	s.nextPID++
	pid := s.nextPID
	s.cancels[pid] = cancel
	s.connCount++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.cancels, pid)
		s.mu.Unlock()
	}()

	key := make([]byte, 4)
	binary.BigEndian.PutUint32(key, pid)
	be.Send(&pgproto3.AuthenticationOk{})
	for _, p := range [][2]string{
		{"server_version", "17.0"},
		{"client_encoding", "UTF8"},
		{"standard_conforming_strings", "on"},
		{"DateStyle", "ISO, MDY"},
	} {
		be.Send(&pgproto3.ParameterStatus{Name: p[0], Value: p[1]})
	}
	be.Send(&pgproto3.BackendKeyData{ProcessID: pid, SecretKey: key})

	tx := byte('I')
	be.Send(&pgproto3.ReadyForQuery{TxStatus: tx})
	if be.Flush() != nil {
		return
	}

	var pending *Query
	failed := false
	for {
		msg, err := be.Receive()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *pgproto3.Terminate:
			return
		case *pgproto3.Query:
			q := Query{SQL: m.String, Simple: true}
			s.record(q)
			tag := strings.ToUpper(strings.TrimSpace(m.String))
			switch tag {
			case "BEGIN":
				tx = 'T'
			case "COMMIT", "ROLLBACK":
				tx = 'I'
			default:
				r := s.handler(q)
				if r.Code != "" {
					sendError(be, r)
					if tx == 'T' {
						tx = 'E'
					}
					be.Send(&pgproto3.ReadyForQuery{TxStatus: tx})
					if be.Flush() != nil {
						return
					}
					continue
				}
			}
			be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
			be.Send(&pgproto3.ReadyForQuery{TxStatus: tx})
			if be.Flush() != nil {
				return
			}
		case *pgproto3.Parse:
			pending = &Query{SQL: m.Query, ParamOIDs: append([]uint32(nil), m.ParameterOIDs...)}
			failed = false
		case *pgproto3.Bind:
			if pending != nil {
				for _, p := range m.Parameters {
					if p == nil {
						pending.Params = append(pending.Params, nil)
					} else {
						pending.Params = append(pending.Params, append([]byte{}, p...))
					}
				}
			}
		case *pgproto3.Describe, *pgproto3.Execute:
			// Answered whole at Sync.
		case *pgproto3.Sync:
			if pending != nil && !failed {
				q := *pending
				s.record(q)
				r := s.handler(q)
				if r.Delay > 0 {
					select {
					case <-time.After(r.Delay):
					case <-ctx.Done():
						r = Error("57014", "canceling statement due to user request")
					}
				}
				if r.Code != "" {
					sendError(be, r)
					if tx == 'T' {
						tx = 'E'
					}
				} else {
					sendResult(be, r)
				}
			}
			pending = nil
			be.Send(&pgproto3.ReadyForQuery{TxStatus: tx})
			if be.Flush() != nil {
				return
			}
		default:
			return
		}
	}
}

func (s *Server) record(q Query) {
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()
}

func sendError(be *pgproto3.Backend, r Reply) {
	be.Send(&pgproto3.ErrorResponse{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Code:                r.Code,
		Message:             r.Message,
		Detail:              r.Detail,
		Hint:                r.Hint,
	})
}

func sendResult(be *pgproto3.Backend, r Reply) {
	be.Send(&pgproto3.ParseComplete{})
	be.Send(&pgproto3.BindComplete{})
	if len(r.Columns) == 0 {
		be.Send(&pgproto3.NoData{})
		be.Send(&pgproto3.CommandComplete{CommandTag: []byte("OK")})
		return
	}
	fields := make([]pgproto3.FieldDescription, len(r.Columns))
	for i, c := range r.Columns {
		oid := uint32(25)
		if i < len(r.OIDs) && r.OIDs[i] != 0 {
			oid = r.OIDs[i]
		}
		fields[i] = pgproto3.FieldDescription{Name: []byte(c), DataTypeOID: oid, DataTypeSize: -1, TypeModifier: -1}
	}
	be.Send(&pgproto3.RowDescription{Fields: fields})
	for _, row := range r.Rows {
		vals := make([][]byte, len(row))
		for i, v := range row {
			if v != nil {
				vals[i] = append([]byte{}, *v...) // non-nil, so '' stays distinct from NULL
			}
		}
		be.Send(&pgproto3.DataRow{Values: vals})
	}
	be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT " + strconv.Itoa(len(r.Rows)))})
}
