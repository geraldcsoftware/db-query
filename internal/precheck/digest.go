// Package precheck builds the dry-run document a hook reads, binds it to the
// invocation it describes, and gates execution on the result.
// See docs/design.md §13.12 and §13.14.
package precheck

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Tuple is what a token is bound to. It is deliberately more than the SQL.
//
// Binding SQL alone admits two replays. A caller could pre-check against a
// development host and execute against production with the same digest, which
// Provider, Host and Database close. And it could pre-check with a harmless
// parameter and execute with a payload in the value, which ParamValues closes:
// the SQL text is identical in both runs, so nothing else would notice.
type Tuple struct {
	Provider    string
	Host        string
	Database    string
	SQL         string
	ParamValues map[string]string
}

// canonical serialises a tuple unambiguously. Every part is length-prefixed so
// that no rearrangement of contents can produce the bytes of a different
// tuple: without it, a host of "ab" with database "c" and a host of "a" with
// database "bc" would hash alike.
func (t Tuple) canonical() []byte {
	var b []byte
	add := func(s string) {
		b = append(b, []byte(strconv.Itoa(len(s)))...)
		b = append(b, ':')
		b = append(b, []byte(s)...)
	}
	add(t.Provider)
	add(t.Host)
	add(t.Database)
	add(t.SQL)
	names := make([]string, 0, len(t.ParamValues))
	for k := range t.ParamValues {
		names = append(names, k)
	}
	sort.Strings(names) // map order must not change the digest
	add(strconv.Itoa(len(names)))
	for _, n := range names {
		add(n)
		add(t.ParamValues[n])
	}
	return b
}

// Digest binds a tuple with the installation key.
//
// The key stops a caller fabricating the "this was pre-checked" claim. It is
// not what makes the scheme safe: the caller runs as the same user and can
// read whatever this process can, and in any case §13.12 mints a token only
// for SQL that classified clean, so obtaining one for a destructive submission
// is not possible whether or not the key is known. The key is a deterrent
// against the accidental path, and is recorded as one.
func Digest(t Tuple, key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(t.canonical())
	return hex.EncodeToString(m.Sum(nil))
}

// Matches compares a presented digest against the tuple in constant time.
func Matches(t Tuple, key []byte, presented string) bool {
	want := Digest(t, key)
	return hmac.Equal([]byte(want), []byte(presented))
}

// KeyPath is where the installation key lives, beside the config file.
func KeyPath() string {
	dir := filepath.Dir(defaultConfigPath())
	return filepath.Join(dir, "precheck.key")
}

// defaultConfigPath mirrors config.DefaultPath without importing it, keeping
// this package free of a dependency it would otherwise need only for a
// directory name.
func defaultConfigPath() string {
	if p := os.Getenv("DB_QUERY_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "config.toml"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "db-query", "config.toml")
}

// LoadKey reads the installation key, creating one on first use.
//
// Per-installation rather than compiled in: a constant baked into a public
// release becomes common knowledge the first time anyone runs strings on the
// binary, and could never be rotated without shipping a new one. Deleting the
// file rotates the key, which invalidates outstanding tokens and nothing else.
func LoadKey() ([]byte, error) {
	path := KeyPath()
	b, err := os.ReadFile(path)
	if err == nil && len(b) >= 32 {
		return b, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading the precheck key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating a precheck key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the config directory: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("writing the precheck key: %w", err)
	}
	return key, nil
}
