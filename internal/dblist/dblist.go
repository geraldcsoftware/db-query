// Package dblist persists the list of database names available on a host so
// shell completion can offer them for --database without opening a connection.
// The cache lives under $XDG_CACHE_HOME/db-query/databases/ and holds names
// only, never credentials.
//
// It is a sibling of internal/schema rather than part of it: the schema cache
// is keyed on the resolved server address, which is the better identity but is
// unavailable to the completion helper, since a host may take its address from
// a credential and the helper may never resolve one. This cache is therefore
// keyed on the config label, which the helper can always compute from config
// alone. See docs/design.md §13.9.
package dblist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/geraldcsoftware/db-query/internal/cache"
)

// CacheDir returns the database-list cache directory.
func CacheDir() string { return cache.Dir("databases") }

// CachePath returns the cache file path for a host, named for the host's config
// label. The filename is a sanitized label for legibility plus the first 8 hex
// characters of the SHA-256 of the raw label; the hash is what guarantees that
// two labels never share a file even where sanitisation or a case-folding
// filesystem would collapse their readable parts.
func CachePath(host string) string {
	sum := sha256.Sum256([]byte(host))
	hash := hex.EncodeToString(sum[:])[:8]
	return filepath.Join(CacheDir(), fmt.Sprintf("%s-%s.json", cache.Sanitize(host), hash))
}

// Exists reports whether a database-list cache file is present at path.
func Exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Write persists names at path as a bare JSON array, creating the cache
// directory as needed.
func Write(path string, names []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating database-list cache dir: %w", err)
	}
	if names == nil {
		names = []string{}
	}
	data, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding database list: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing database-list cache %s: %w", path, err)
	}
	return nil
}

// Read loads the cached names at path.
func Read(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading database-list cache %s: %w", path, err)
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("decoding database-list cache %s: %w", path, err)
	}
	return names, nil
}
