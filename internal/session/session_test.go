package session

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/dblist"
	"github.com/geraldcsoftware/db-query/internal/pgfake"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T) string {
	t.Helper()
	port := os.Getenv("DBQ_TEST_PGPORT")
	if port == "" {
		port = "5432"
	}
	return writeFile(t, t.TempDir(), "config.toml", `
[hosts.testpg]
provider   = "postgres"
host       = "127.0.0.1"
port       = `+port+`
database   = "testdb"
username   = "app"
credential = "env:DBQ_SESSION_TEST_PW"
`)
}

func TestSetupHappyPath(t *testing.T) {
	t.Setenv("DBQ_SESSION_TEST_PW", "pw")
	cfg := testConfig(t)
	var errb strings.Builder
	r, code := Setup(CommonFlags{Host: "testpg", Config: cfg, Output: "text", Timeout: time.Second}, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if r.Adapter.Name() != "postgres" {
		t.Fatalf("adapter = %q, want postgres", r.Adapter.Name())
	}
	if r.Host.Database != "testdb" {
		t.Fatalf("database = %q, want testdb", r.Host.Database)
	}
	if r.Cred.Password != "pw" {
		t.Fatalf("password = %q, want pw", r.Cred.Password)
	}
}

func TestSetupMissingHost(t *testing.T) {
	var errb strings.Builder
	_, code := Setup(CommonFlags{Output: "text", Timeout: time.Second}, &errb)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errb.String(), "--host is required") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// fakePG starts a fake postgres server and points testConfig at it, mirroring
// internal/cli's test helper of the same shape.
func fakePG(t *testing.T, h pgfake.Handler) *pgfake.Server {
	t.Helper()
	srv := pgfake.Start(t, h)
	t.Setenv("DBQ_TEST_PGPORT", strconv.Itoa(srv.Port))
	return srv
}

// TestListDatabasesWritesCache pins the pairing that makes the listing worth
// sharing: the rows come back untouched — renaming the provider's column is
// the CLI's presentation concern — and the names land in the cache keyed on
// the config label, which is what keeps --database completion current.
func TestListDatabasesWritesCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DBQ_SESSION_TEST_PW", "pw")
	fakePG(t, func(pgfake.Query) pgfake.Reply {
		return pgfake.Rows([]string{"datname"}, []string{"postgres"}, []string{"testdb"})
	})
	c := CommonFlags{Host: "testpg", Config: testConfig(t), Output: "text", Timeout: 10 * time.Second}

	r, code := Setup(c, io.Discard)
	if code != 0 {
		t.Fatalf("setup code=%d", code)
	}
	var errb strings.Builder
	rows, code := ListDatabases(r, c, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if !reflect.DeepEqual(rows.Columns, []string{"datname"}) {
		t.Fatalf("columns = %v, want the provider's own", rows.Columns)
	}
	names, err := dblist.Read(dblist.CachePath("testpg"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"postgres", "testdb"}) {
		t.Fatalf("cached names = %v", names)
	}
}

// TestDatabaseNamesSkipsEmpty: an empty candidate would complete to nothing,
// so it is dropped rather than cached.
func TestDatabaseNamesSkipsEmpty(t *testing.T) {
	name, empty := "keep", ""
	rows := adapter.Rows{
		Columns: []string{"datname"},
		Rows:    [][]*string{{&name}, {&empty}, {nil}, {}},
	}
	if got := DatabaseNames(rows); !reflect.DeepEqual(got, []string{"keep"}) {
		t.Fatalf("names = %v, want [keep]", got)
	}
}

func TestSetupUnknownHost(t *testing.T) {
	cfg := testConfig(t)
	var errb strings.Builder
	_, code := Setup(CommonFlags{Host: "nope", Config: cfg, Output: "text", Timeout: time.Second}, &errb)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errb.String(), `unknown host "nope"`) {
		t.Fatalf("stderr = %q", errb.String())
	}
}
