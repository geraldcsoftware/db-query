package tui

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/pgfake"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// fakePG starts a fake postgres server and points the test hosts at it,
// mirroring internal/cli/cli_test.go's helper of the same name. Hosts built
// by testResolved and bootstrapConfig read the port when they are made, so
// the fake is started first.
func fakePG(t *testing.T, h pgfake.Handler) *pgfake.Server {
	t.Helper()
	srv := pgfake.Start(t, h)
	t.Setenv("DBQ_TEST_PGPORT", strconv.Itoa(srv.Port))
	return srv
}

// unreachablePG points the test hosts at a port nothing listens on.
func unreachablePG(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBQ_TEST_PGPORT", strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
	l.Close()
}

// testPort is the port fakePG or unreachablePG chose, or postgres's own
// default for a test that never connects.
func testPort() int {
	if n, err := strconv.Atoi(os.Getenv("DBQ_TEST_PGPORT")); err == nil {
		return n
	}
	return 5432
}

func testResolved(t *testing.T) session.Resolved {
	t.Helper()
	a, err := adapter.For("postgres")
	if err != nil {
		t.Fatal(err)
	}
	return session.Resolved{
		Adapter: a,
		Host:    config.HostConfig{Name: "t", Host: "localhost", Port: testPort(), Database: "testdb", Extra: map[string]string{"sslmode": "disable"}},
	}
}

func TestExecuteHappyPath(t *testing.T) {
	fakePG(t, func(pgfake.Query) pgfake.Reply { return pgfake.Rows([]string{"id"}, []string{"1"}) })
	sets, schemaErr, err := execute(context.Background(), testResolved(t), "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr {
		t.Fatal("must not report a schema error on success")
	}
	if len(sets) != 1 || len(sets[0].Columns) != 1 || sets[0].Columns[0] != "id" {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestExecuteSeveralResultSets(t *testing.T) {
	fakePG(t, func(q pgfake.Query) pgfake.Reply { return pgfake.Rows([]string{q.SQL}) })
	sets, _, err := execute(context.Background(), testResolved(t), "select 1; select 2")
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 || sets[0].Columns[0] != "select 1" || sets[1].Columns[0] != "select 2" {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestExecuteSchemaError(t *testing.T) {
	fakePG(t, func(pgfake.Query) pgfake.Reply { return pgfake.Error("42703", `column "nope" does not exist`) })
	_, schemaErr, err := execute(context.Background(), testResolved(t), "select nope from t")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !schemaErr {
		t.Fatal("expected a schema error")
	}
}

func TestExecuteCancellation(t *testing.T) {
	fakePG(t, func(pgfake.Query) pgfake.Reply { return pgfake.Reply{Delay: 5 * time.Second} })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := execute(ctx, testResolved(t), "select 1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestExecuteExplicitCancel(t *testing.T) {
	fakePG(t, func(pgfake.Query) pgfake.Reply { return pgfake.Reply{Delay: 5 * time.Second} })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _, err := execute(ctx, testResolved(t), "select 1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
