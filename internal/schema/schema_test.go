package schema

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

func ptr(s string) *string { return &s }

func TestCacheDir(t *testing.T) {
	t.Run("xdg wins", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "/xdg")
		want := filepath.Join("/xdg", "db-query", "schema")
		if got := CacheDir(); got != want {
			t.Fatalf("CacheDir = %q, want %q", got, want)
		}
	})
	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("HOME", "/home/me")
		want := filepath.Join("/home/me", ".cache", "db-query", "schema")
		if got := CacheDir(); got != want {
			t.Fatalf("CacheDir = %q, want %q", got, want)
		}
	})
}

func TestCachePathDeterministic(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if CachePath("h", "d") != CachePath("h", "d") {
		t.Fatal("CachePath must be deterministic for the same host+database")
	}
}

// TestCachePathUniqueness pins the locked guarantee: distinct host+database
// pairs must never map to the same file, including the awkward cases where
// sanitisation or a boundary shift would otherwise collide.
func TestCachePathUniqueness(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	pairs := [][2]string{
		{"core.internal", "core"},
		{"core.internal", "reports"}, // same host, different db
		{"sql01.internal", "core"},   // different host, same db
		{"a/b", "c"},                 // unsafe char sanitises to a-b
		{"a-b", "c"},                 // same sanitised prefix as a/b, different raw
		{"ab", "c"},                  // boundary shift vs the next pair
		{"a", "bc"},                  // NUL separator must distinguish from ab+c
		{"", "core"},                 // empty host
		{"core", ""},                 // empty db
		{"Core", "core"},             // case differs from the next pair
		{"core", "core"},
	}
	seen := map[string][2]string{}
	for _, p := range pairs {
		path := CachePath(p[0], p[1])
		if prev, ok := seen[path]; ok {
			t.Fatalf("collision: %v and %v both map to %s", prev, p, path)
		}
		seen[path] = p
	}
	if len(seen) != len(pairs) {
		t.Fatalf("expected %d distinct paths, got %d", len(pairs), len(seen))
	}
}

// TestCachePathFilesystemSafe checks that no input can escape the cache dir
// or leave an unsafe character in the filename.
func TestCachePathFilesystemSafe(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	path := CachePath("ho/st:name", "d/b name")
	if dir := filepath.Dir(path); dir != CacheDir() {
		t.Fatalf("unsafe chars leaked into path: dir = %q, want %q", dir, CacheDir())
	}
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".json") {
		t.Fatalf("filename must end in .json: %q", base)
	}
	for _, r := range base {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if !ok {
			t.Fatalf("filename has unsafe char %q in %q", r, base)
		}
	}
}

// TestWriteReadRoundTrip guards the load-bearing property: NULL versus the
// empty string survives the JSON round trip.
func TestWriteReadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := CachePath("core.internal", "core")
	want := adapter.Rows{
		Columns: []string{"table", "column", "default"},
		Rows: [][]*string{
			{ptr("users"), ptr("id"), ptr("")}, // empty-string default
			{ptr("users"), ptr("email"), nil},  // NULL default
			{nil, nil, nil},
		},
	}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
	// Explicit NULL-vs-empty assertions, in case DeepEqual is ever relaxed.
	if got.Rows[0][2] == nil || *got.Rows[0][2] != "" {
		t.Fatal("empty string must survive as a non-nil pointer to \"\"")
	}
	if got.Rows[1][2] != nil {
		t.Fatal("NULL must survive as a nil pointer")
	}
}

func TestExists(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := CachePath("h", "d")
	if Exists(path) {
		t.Fatal("must not report existence before write")
	}
	if err := Write(path, adapter.Rows{Columns: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	if !Exists(path) {
		t.Fatal("must report existence after write")
	}
	if Exists(CacheDir()) {
		t.Fatal("a directory must not count as an existing cache file")
	}
}

func TestReadErrors(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Run("missing file", func(t *testing.T) {
		if _, err := Read(CachePath("nope", "nope")); err == nil {
			t.Fatal("want error for a missing cache file")
		}
	})
	t.Run("corrupt json", func(t *testing.T) {
		path := CachePath("bad", "bad")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil {
			t.Fatal("want error for a corrupt cache file")
		}
	})
}

// TestCachePathFormat documents the locked filename shape.
func TestCachePathFormat(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	base := filepath.Base(CachePath("core.internal", "core"))
	prefix := "core.internal_core-"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".json") {
		t.Fatalf("filename %q does not match <host>_<db>-<hash>.json", base)
	}
	hash := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".json")
	if len(hash) != 8 {
		t.Fatalf("hash segment = %q, want 8 hex chars", hash)
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("hash segment %q is not lowercase hex", hash)
		}
	}
}

func TestTables(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"table_schema", "table_name", "column_name", "data_type", "is_nullable"},
	}
	add := func(schemaName, table, col, dtype, nullable string) {
		s, tb, c, d, n := schemaName, table, col, dtype, nullable
		rows.Rows = append(rows.Rows, []*string{&s, &tb, &c, &d, &n})
	}
	add("public", "orders", "id", "int8", "NO")
	add("public", "orders", "status", "text", "YES")
	add("public", "payments", "id", "int8", "NO")

	tables, err := Tables(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(tables))
	}
	if tables[0].Name != "orders" || len(tables[0].Columns) != 2 {
		t.Fatalf("tables[0] = %+v", tables[0])
	}
	if tables[0].Columns[0].Name != "id" || tables[0].Columns[0].Nullable {
		t.Fatalf("orders.id = %+v, want non-nullable", tables[0].Columns[0])
	}
	if !tables[0].Columns[1].Nullable {
		t.Fatalf("orders.status should be nullable")
	}
	if tables[1].Name != "payments" || len(tables[1].Columns) != 1 {
		t.Fatalf("tables[1] = %+v", tables[1])
	}
}

func TestTablesCaseInsensitiveColumns(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "DATA_TYPE", "IS_NULLABLE"},
	}
	s, tb, c, d, n := "dbo", "orders", "id", "int", "NO"
	rows.Rows = append(rows.Rows, []*string{&s, &tb, &c, &d, &n})

	tables, err := Tables(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Schema != "dbo" {
		t.Fatalf("tables = %+v", tables)
	}
}

func TestTablesMissingColumns(t *testing.T) {
	_, err := Tables(adapter.Rows{Columns: []string{"not", "a", "catalogue"}})
	if err == nil {
		t.Fatal("expected an error for a rowset that is not a catalogue")
	}
}
