package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// fakeBin puts a stub executable on PATH that prints the given script's
// output, mirroring internal/cli/cli_test.go's fakePsql pattern.
func fakeBin(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testResolved(t *testing.T) session.Resolved {
	t.Helper()
	a, err := adapter.For("postgres")
	if err != nil {
		t.Fatal(err)
	}
	return session.Resolved{
		Adapter: a,
		Host:    config.HostConfig{Name: "t", Host: "localhost", Database: "testdb"},
	}
}

func TestExecuteHappyPath(t *testing.T) {
	fakeBin(t, "psql", `printf 'id\n1\n'`)
	rows, schemaErr, err := execute(context.Background(), testResolved(t), "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr {
		t.Fatal("must not report a schema error on success")
	}
	if len(rows.Columns) != 1 || rows.Columns[0] != "id" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestExecuteSchemaError(t *testing.T) {
	fakeBin(t, "psql", `echo 'ERROR:  column "nope" does not exist' >&2; exit 1`)
	_, schemaErr, err := execute(context.Background(), testResolved(t), "select nope from t")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !schemaErr {
		t.Fatal("expected IsSchemaError to be true")
	}
}

func TestExecuteCancellation(t *testing.T) {
	fakeBin(t, "psql", `sleep 5; printf 'id\n1\n'`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := execute(ctx, testResolved(t), "select 1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestExecuteExplicitCancel(t *testing.T) {
	fakeBin(t, "psql", `sleep 5; printf 'id\n1\n'`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _, err := execute(ctx, testResolved(t), "select 1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
