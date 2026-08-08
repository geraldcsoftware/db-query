package dblist

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCacheDir(t *testing.T) {
	t.Run("xdg wins", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "/xdg")
		want := filepath.Join("/xdg", "db-query", "databases")
		if got := CacheDir(); got != want {
			t.Fatalf("CacheDir = %q, want %q", got, want)
		}
	})
	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("HOME", "/home/me")
		want := filepath.Join("/home/me", ".cache", "db-query", "databases")
		if got := CacheDir(); got != want {
			t.Fatalf("CacheDir = %q, want %q", got, want)
		}
	})
}

// TestCacheDirIsNotTheSchemaDir guards the separation: the two caches key
// differently and must never share a directory.
func TestCacheDirIsNotTheSchemaDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	if strings.HasSuffix(CacheDir(), string(filepath.Separator)+"schema") {
		t.Fatalf("database-list cache must not live in the schema dir: %q", CacheDir())
	}
}

func TestCachePathDeterministic(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if CachePath("lionel") != CachePath("lionel") {
		t.Fatal("CachePath must be deterministic for the same host")
	}
}

// TestCachePathUniqueness pins the guarantee that distinct config labels never
// map to the same file, including where sanitisation or case folding would
// otherwise collapse them.
func TestCachePathUniqueness(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	names := []string{
		"lionel",
		"lionel-uat",
		"a/b", // unsafe char sanitises to a-b
		"a-b", // same sanitised form as a/b, different raw
		"a:b", // likewise
		"Lionel",
		"",
	}
	seen := map[string]string{}
	for _, n := range names {
		path := CachePath(n)
		if prev, ok := seen[path]; ok {
			t.Fatalf("collision: %q and %q both map to %s", prev, n, path)
		}
		seen[path] = n
	}
	if len(seen) != len(names) {
		t.Fatalf("expected %d distinct paths, got %d", len(names), len(seen))
	}
}

func TestCachePathFilesystemSafe(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	path := CachePath("ho/st:name with spaces")
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

// TestCachePathFormat documents the filename shape: <sanitized-name>-<8hex>.json.
func TestCachePathFormat(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	base := filepath.Base(CachePath("core.internal"))
	prefix := "core.internal-"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".json") {
		t.Fatalf("filename %q does not match <name>-<hash>.json", base)
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

func TestWriteReadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := CachePath("lionel")
	want := []string{"postgres", "testdb", "reporting"}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", got, want)
	}
}

// TestWriteProducesBareJSONArray pins the locked file shape: a bare array of
// names, not an object wrapping one. The completion helper and a human reading
// the file both depend on it.
func TestWriteProducesBareJSONArray(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := CachePath("lionel")
	if err := Write(path, []string{"postgres", "testdb"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		t.Fatalf("cache file must be a bare JSON array, got:\n%s", trimmed)
	}
	if !strings.Contains(trimmed, `"postgres"`) || !strings.Contains(trimmed, `"testdb"`) {
		t.Fatalf("cache file is missing names:\n%s", trimmed)
	}
}

func TestWriteReadEmptyList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := CachePath("empty")
	if err := Write(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want no names, got %#v", got)
	}
}

func TestExists(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := CachePath("lionel")
	if Exists(path) {
		t.Fatal("must not report existence before write")
	}
	if err := Write(path, []string{"postgres"}); err != nil {
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
		if _, err := Read(CachePath("nope")); err == nil {
			t.Fatal("want error for a missing cache file")
		}
	})
	t.Run("corrupt json", func(t *testing.T) {
		path := CachePath("bad")
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
