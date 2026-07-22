package savedquery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolate points the store at a fresh temp dir and clears the other
// location env vars so Dir() is deterministic for a disk-touching test.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DB_QUERY_QUERIES_DIR", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	return dir
}

func TestDir(t *testing.T) {
	t.Run("explicit dir wins", func(t *testing.T) {
		t.Setenv("DB_QUERY_QUERIES_DIR", "/explicit")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		if got := Dir(); got != "/explicit" {
			t.Fatalf("Dir = %q, want /explicit", got)
		}
	})
	t.Run("xdg fallback", func(t *testing.T) {
		t.Setenv("DB_QUERY_QUERIES_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		want := filepath.Join("/xdg", "db-query", "queries")
		if got := Dir(); got != want {
			t.Fatalf("Dir = %q, want %q", got, want)
		}
	})
	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("DB_QUERY_QUERIES_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/me")
		want := filepath.Join("/home/me", ".config", "db-query", "queries")
		if got := Dir(); got != want {
			t.Fatalf("Dir = %q, want %q", got, want)
		}
	})
}

// TestSaveLoadRoundTrip pins the path and file-format round trip: a saved
// query loads back with its provider, hash and SQL intact, and the file
// carries the reserved header above the body.
func TestSaveLoadRoundTrip(t *testing.T) {
	isolate(t)
	const sql = "SELECT id, name FROM people WHERE id = :'id'"

	saved, err := Save("by-id", "people", "postgres", sql, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SQLHash != Hash(sql) {
		t.Fatalf("saved hash %q != Hash(sql) %q", saved.SQLHash, Hash(sql))
	}
	if _, err := time.Parse(time.RFC3339, saved.Saved); err != nil {
		t.Fatalf("saved timestamp %q is not RFC3339: %v", saved.Saved, err)
	}
	if saved.Path != filepath.Join(Dir(), "people", "by-id.sql") {
		t.Fatalf("unexpected path %q", saved.Path)
	}

	got, err := Load("by-id", "people")
	if err != nil {
		t.Fatal(err)
	}
	if got.SQL != sql {
		t.Fatalf("loaded SQL = %q, want %q", got.SQL, sql)
	}
	if got.Provider != "postgres" || got.SQLHash != saved.SQLHash || got.Saved != saved.Saved {
		t.Fatalf("metadata mismatch: %+v", got)
	}

	// The on-disk file must carry the reserved header above the body.
	raw, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"-- db-query:name=by-id\n",
		"-- db-query:category=people\n",
		"-- db-query:provider=postgres\n",
		"-- db-query:sqlhash=" + saved.SQLHash + "\n",
		"-- db-query:saved=" + saved.Saved + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("file missing header line %q; file:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, sql) {
		t.Fatalf("file body is not the raw SQL; file:\n%s", text)
	}
}

func TestDefaultCategory(t *testing.T) {
	isolate(t)
	saved, err := Save("q", "", "postgres", "SELECT 1", false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Category != DefaultCategory {
		t.Fatalf("category = %q, want %q", saved.Category, DefaultCategory)
	}
	if _, err := Load("q", ""); err != nil {
		t.Fatalf("load with defaulted category: %v", err)
	}
	if _, err := Load("q", DefaultCategory); err != nil {
		t.Fatalf("load with explicit default category: %v", err)
	}
}

// TestNormalizeEquivalence pins the dedup contract: whitespace- and
// comment-only differences hash alike, while a substantive change does not.
func TestNormalizeEquivalence(t *testing.T) {
	base := "SELECT * FROM orders"
	equal := []string{
		"SELECT * FROM orders",
		"SELECT   *\n\tFROM   orders",
		"SELECT * FROM orders;",
		"SELECT * FROM orders ; ;",
		"SELECT * FROM orders -- all of them\n",
		"SELECT * /* cols */ FROM orders",
		"  SELECT * FROM orders  ",
		"SELECT *\nFROM orders\n-- trailing note",
	}
	for _, s := range equal {
		if Hash(s) != Hash(base) {
			t.Fatalf("expected equal hash for %q\n got %q\nbase %q", s, Normalize(s), Normalize(base))
		}
	}
	different := []string{
		"SELECT * FROM products",
		"SELECT id FROM orders",
		"SELECT * FROM orders WHERE id = 1",
	}
	for _, s := range different {
		if Hash(s) == Hash(base) {
			t.Fatalf("expected different hash for %q", s)
		}
	}
}

// TestNormalizeRespectsLiterals guards against a "--" or "/*" inside a
// string literal or quoted identifier being mistaken for a comment.
func TestNormalizeRespectsLiterals(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"SELECT '-- not a comment'", "SELECT '-- not a comment'"},
		{"SELECT '/* still text */'", "SELECT '/* still text */'"},
		{"SELECT 'it''s fine'", "SELECT 'it''s fine'"},
		{`SELECT "weird-- col" FROM t`, `SELECT "weird-- col" FROM t`},
		{"SELECT 1 -- real comment\n, 2", "SELECT 1 , 2"},
		{"SELECT /* nested /* block */ */ 1", "SELECT 1"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Fatalf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// A literal with an embedded "--" must not collide with a genuinely
	// commented-out variant.
	if Hash("SELECT 'a -- b'") == Hash("SELECT 'a") {
		t.Fatal("literal content was stripped as a comment")
	}
}

// TestDedupeRefusalAndForce pins the global normalized-hash dedup and the
// force override.
func TestDedupeRefusalAndForce(t *testing.T) {
	isolate(t)
	if _, err := Save("orders", "reports", "postgres", "SELECT * FROM orders", false); err != nil {
		t.Fatal(err)
	}

	// A differently named query with equivalent SQL (only whitespace and a
	// comment differ) is refused and points at the original.
	_, err := Save("orders-copy", "default", "postgres", "SELECT *\nFROM orders -- dup\n", false)
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("want DuplicateError, got %v", err)
	}
	if dup.Category != "reports" || dup.Name != "orders" {
		t.Fatalf("DuplicateError points at %s/%s, want reports/orders", dup.Category, dup.Name)
	}
	if _, err := Load("orders-copy", "default"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused save must not create a file, got %v", err)
	}

	// force bypasses the dedup and writes.
	if _, err := Save("orders-copy", "default", "postgres", "SELECT *\nFROM orders -- dup\n", true); err != nil {
		t.Fatalf("force save should succeed: %v", err)
	}
	if _, err := Load("orders-copy", "default"); err != nil {
		t.Fatalf("forced query should load: %v", err)
	}
}

// TestExistsRefusalAndForce pins the target-file guard: a real SQL change
// under an existing name is refused without force and overwritten with it.
func TestExistsRefusalAndForce(t *testing.T) {
	isolate(t)
	if _, err := Save("q", "c", "postgres", "SELECT 1", false); err != nil {
		t.Fatal(err)
	}

	_, err := Save("q", "c", "postgres", "SELECT 2", false)
	var exists *ExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("want ExistsError, got %v", err)
	}
	if exists.Category != "c" || exists.Name != "q" {
		t.Fatalf("ExistsError names %s/%s, want c/q", exists.Category, exists.Name)
	}
	// The stored SQL is untouched by the refused save.
	if got, _ := Load("q", "c"); got.SQL != "SELECT 1" {
		t.Fatalf("refused save changed the stored SQL to %q", got.SQL)
	}

	if _, err := Save("q", "c", "postgres", "SELECT 2", true); err != nil {
		t.Fatalf("force overwrite should succeed: %v", err)
	}
	if got, _ := Load("q", "c"); got.SQL != "SELECT 2" {
		t.Fatalf("force did not overwrite; SQL = %q", got.SQL)
	}
}

func TestTraversalRejection(t *testing.T) {
	isolate(t)
	bad := []struct{ name, category string }{
		{"", "default"},
		{"   ", "default"},
		{"a/b", "default"},
		{"..", "default"},
		{"../evil", "default"},
		{"a..b", "default"},
		{".", "default"},
		{"ok", "a/b"},
		{"ok", ".."},
		{"ok", "../evil"},
	}
	for _, c := range bad {
		if _, err := Path(c.name, c.category); err == nil {
			t.Errorf("Path(%q,%q) should be rejected", c.name, c.category)
		}
		if _, err := Save(c.name, c.category, "postgres", "SELECT 1", false); err == nil {
			t.Errorf("Save(%q,%q) should be rejected", c.name, c.category)
		}
		if _, err := Load(c.name, c.category); err == nil {
			t.Errorf("Load(%q,%q) should be rejected", c.name, c.category)
		}
	}
}

// TestBodyPreservesLeadingComment guards that a user's own leading comment
// survives the round trip and does not become reserved metadata.
func TestBodyPreservesLeadingComment(t *testing.T) {
	isolate(t)
	sql := "-- monthly revenue report\n-- owner: finance\nSELECT sum(total) FROM orders"

	saved, err := Save("revenue", "reports", "postgres", sql, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load("revenue", "reports")
	if err != nil {
		t.Fatal(err)
	}
	if got.SQL != sql {
		t.Fatalf("body not preserved:\n got %q\nwant %q", got.SQL, sql)
	}
	// The leading comment is ignored for hashing.
	if saved.SQLHash != Hash("SELECT sum(total) FROM orders") {
		t.Fatal("leading comment should not affect the hash")
	}
}

func TestListSortingAndFilter(t *testing.T) {
	isolate(t)
	seed := []struct{ name, category, sql string }{
		{"beta", "reports", "SELECT 2"},
		{"alpha", "reports", "SELECT 1"},
		{"gamma", "people", "SELECT 3"},
	}
	for _, s := range seed {
		if _, err := Save(s.name, s.category, "postgres", s.sql, false); err != nil {
			t.Fatal(err)
		}
	}

	all, err := List("")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(all))
	for i, q := range all {
		got[i] = q.Category + "/" + q.Name
	}
	want := []string{"people/gamma", "reports/alpha", "reports/beta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List order = %v, want %v", got, want)
	}
	if all[0].SQL != "SELECT 3" || all[0].Provider != "postgres" {
		t.Fatalf("List did not carry SQL/provider: %+v", all[0])
	}

	only, err := List("reports")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 2 || only[0].Name != "alpha" || only[1].Name != "beta" {
		t.Fatalf("filtered list = %+v", only)
	}
}

func TestListMissingStore(t *testing.T) {
	t.Setenv("DB_QUERY_QUERIES_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := List("")
	if err != nil {
		t.Fatalf("missing store must not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing store must be empty, got %v", got)
	}
}

func TestLoadMissing(t *testing.T) {
	isolate(t)
	_, err := Load("nope", "default")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing query must wrap os.ErrNotExist, got %v", err)
	}
}
