// Package cache locates the on-disk directories db-query caches into and makes
// arbitrary values safe to embed in a filename. It holds no cache format of its
// own: each caller owns its subdirectory, its key and its file contents, and
// shares only the placement and sanitisation rules so the two caches cannot
// drift apart on where they live or what characters they permit.
package cache

import (
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the cache directory for one named subsystem:
// $XDG_CACHE_HOME/db-query/<sub>, else ~/.cache/db-query/<sub>.
func Dir(sub string) string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("db-query", sub)
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "db-query", sub)
}

// Sanitize reduces a value to filesystem-safe characters. Callers pair it with
// a hash of the raw input, so this only needs to be legible, not invertible or
// collision-free.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}
