// Package savedquery persists named SQL queries to a local store so the
// CLI can re-run them by name. A saved query is a .sql file at
// Dir()/<category>/<name>.sql carrying a small reserved header of
// "-- db-query:key=value" lines above the raw SQL body. The store lives
// under $DB_QUERY_QUERIES_DIR, else $XDG_CONFIG_HOME/db-query/queries,
// else ~/.config/db-query/queries; it holds SQL with placeholders only,
// never resolved parameter values or credentials.
package savedquery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultCategory is used when a category is not supplied.
const DefaultCategory = "default"

// metaPrefix is the reserved header prefix; lines beginning with it carry
// stored metadata rather than SQL.
const metaPrefix = "-- db-query:"

// metaLine matches a reserved header line, capturing the key and value.
// A line that does not match starts the SQL body, so a user's own leading
// comments are preserved.
var metaLine = regexp.MustCompile(`^-- db-query:(\w+)=(.*)$`)

// SavedQuery is a stored query: its location, provider binding, the hash
// of its normalized SQL, when it was saved, and the raw SQL body.
type SavedQuery struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Provider string `json:"provider"`
	SQLHash  string `json:"sqlhash"`
	Saved    string `json:"saved,omitempty"`
	SQL      string `json:"sql"`
	Path     string `json:"-"`
}

// DuplicateError reports that another stored query already holds SQL with
// the same normalized hash. Save returns it (unless force) so a caller can
// point at the existing query.
type DuplicateError struct {
	Category string
	Name     string
	Hash     string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("a saved query with identical SQL already exists: %s/%s (hash %s); use --force to save anyway",
		e.Category, e.Name, short(e.Hash))
}

// ExistsError reports that the target file is already present. Save returns
// it (unless force) rather than overwrite silently.
type ExistsError struct {
	Category string
	Name     string
	Path     string
}

func (e *ExistsError) Error() string {
	return fmt.Sprintf("saved query %s/%s already exists; use --force to overwrite", e.Category, e.Name)
}

// Dir returns the saved-query store directory: $DB_QUERY_QUERIES_DIR, else
// $XDG_CONFIG_HOME/db-query/queries, else ~/.config/db-query/queries.
func Dir() string {
	if d := os.Getenv("DB_QUERY_QUERIES_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("db-query", "queries")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "db-query", "queries")
}

// Path returns the on-disk path for a name+category, applying the default
// category and rejecting empty names and path-traversal segments.
func Path(name, category string) (string, error) {
	_, _, path, err := resolve(name, category)
	return path, err
}

// resolve validates and sanitises a name+category into safe path segments
// and the full file path. It rejects an empty name and any segment
// containing "/" or "..", so a stored query can never escape the store.
func resolve(name, category string) (safeName, safeCategory, path string, err error) {
	if strings.TrimSpace(name) == "" {
		return "", "", "", fmt.Errorf("saved query name must not be empty")
	}
	if category == "" {
		category = DefaultCategory
	}
	safeName, err = segment("name", name)
	if err != nil {
		return "", "", "", err
	}
	safeCategory, err = segment("category", category)
	if err != nil {
		return "", "", "", err
	}
	path = filepath.Join(Dir(), safeCategory, safeName+".sql")
	return safeName, safeCategory, path, nil
}

// segment rejects traversal, then reduces a value to a filesystem-safe path
// segment. "/" and ".." (and a lone ".") are hard rejections rather than
// sanitisation, so nothing can climb out of the store; every other unsafe
// character folds to "-".
func segment(kind, value string) (string, error) {
	if strings.Contains(value, "/") || strings.Contains(value, "..") || value == "." {
		return "", fmt.Errorf("invalid %s %q: must not contain '/' or '..'", kind, value)
	}
	return sanitize(value), nil
}

// sanitize keeps alphanumerics and the name-friendly punctuation "._-" and
// folds everything else to "-".
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

// Normalize reduces SQL to a canonical form for hashing: it strips line
// (--) and block (/* */) comments, collapses all whitespace to single
// spaces, trims, and drops trailing semicolons. String literals and quoted
// identifiers are tracked so a "--" or "/*" inside them is not mistaken for
// a comment. Case is preserved, since quoted identifiers can be
// case-sensitive.
func Normalize(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '\'':
			i = copyQuoted(&b, sql, i, '\'')
		case c == '"':
			i = copyQuoted(&b, sql, i, '"')
		case c == '-' && i+1 < n && sql[i+1] == '-':
			b.WriteByte(' ')
			i += 2
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			b.WriteByte(' ')
			i = skipBlockComment(sql, i+2)
		default:
			b.WriteByte(c)
			i++
		}
	}
	collapsed := strings.Join(strings.Fields(b.String()), " ")
	return strings.TrimRight(collapsed, "; ")
}

// copyQuoted copies a quoted run (opened by quote at start) verbatim,
// treating a doubled quote as an embedded quote, and returns the index just
// past the closing quote (or end of input if unterminated).
func copyQuoted(b *strings.Builder, sql string, start int, quote byte) int {
	n := len(sql)
	b.WriteByte(quote)
	i := start + 1
	for i < n {
		if sql[i] == quote {
			if i+1 < n && sql[i+1] == quote {
				b.WriteByte(quote)
				b.WriteByte(quote)
				i += 2
				continue
			}
			b.WriteByte(quote)
			return i + 1
		}
		b.WriteByte(sql[i])
		i++
	}
	return i
}

// skipBlockComment consumes a (possibly nested) block comment starting just
// after its opening "/*" and returns the index just past the matching "*/"
// (or end of input if unterminated).
func skipBlockComment(sql string, start int) int {
	n := len(sql)
	depth := 1
	i := start
	for i < n && depth > 0 {
		switch {
		case sql[i] == '/' && i+1 < n && sql[i+1] == '*':
			depth++
			i += 2
		case sql[i] == '*' && i+1 < n && sql[i+1] == '/':
			depth--
			i += 2
		default:
			i++
		}
	}
	return i
}

// Hash returns the hex SHA-256 of the normalized SQL. Queries differing
// only in whitespace or comments hash alike; a substantive change does not.
func Hash(sql string) string {
	sum := sha256.Sum256([]byte(Normalize(sql)))
	return hex.EncodeToString(sum[:])
}

// Save persists SQL under name+category, bound to provider. Unless force,
// it refuses when any stored query already holds SQL with the same
// normalized hash (a DuplicateError naming it) or when the target file
// already exists (an ExistsError). force overwrites regardless. The saved
// timestamp is recorded in RFC3339.
func Save(name, category, provider, sql string, force bool) (SavedQuery, error) {
	safeName, safeCategory, path, err := resolve(name, category)
	if err != nil {
		return SavedQuery{}, err
	}
	hash := Hash(sql)
	if !force {
		existing, err := List("")
		if err != nil {
			return SavedQuery{}, err
		}
		for _, e := range existing {
			if Hash(e.SQL) == hash {
				return SavedQuery{}, &DuplicateError{Category: e.Category, Name: e.Name, Hash: hash}
			}
		}
		if _, err := os.Stat(path); err == nil {
			return SavedQuery{}, &ExistsError{Category: safeCategory, Name: safeName, Path: path}
		}
	}
	sq := SavedQuery{
		Name:     safeName,
		Category: safeCategory,
		Provider: provider,
		SQLHash:  hash,
		Saved:    time.Now().UTC().Format(time.RFC3339),
		SQL:      sql,
		Path:     path,
	}
	if err := writeFile(path, sq); err != nil {
		return SavedQuery{}, err
	}
	return sq, nil
}

// Load reads the stored query at name+category into its metadata and SQL
// body. A missing query yields an error that wraps os.ErrNotExist.
func Load(name, category string) (SavedQuery, error) {
	safeName, safeCategory, path, err := resolve(name, category)
	if err != nil {
		return SavedQuery{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SavedQuery{}, fmt.Errorf("saved query %q (category %q) not found: %w", safeName, safeCategory, err)
	}
	meta, body := parse(string(data))
	return SavedQuery{
		Name:     safeName,
		Category: safeCategory,
		Provider: meta["provider"],
		SQLHash:  meta["sqlhash"],
		Saved:    meta["saved"],
		SQL:      body,
		Path:     path,
	}, nil
}

// List returns every stored query, sorted by category then name. A
// non-empty categoryFilter restricts the result to that one category. A
// missing store yields an empty result, not an error.
func List(categoryFilter string) ([]SavedQuery, error) {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading saved-query store %s: %w", dir, err)
	}
	filter := categoryFilter
	if filter != "" {
		filter = sanitize(filter)
	}
	var out []SavedQuery
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		category := e.Name()
		if filter != "" && category != filter {
			continue
		}
		catDir := filepath.Join(dir, category)
		files, err := os.ReadDir(catDir)
		if err != nil {
			return nil, fmt.Errorf("reading category %s: %w", catDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
				continue
			}
			path := filepath.Join(catDir, f.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading saved query %s: %w", path, err)
			}
			meta, body := parse(string(data))
			out = append(out, SavedQuery{
				Name:     strings.TrimSuffix(f.Name(), ".sql"),
				Category: category,
				Provider: meta["provider"],
				SQLHash:  meta["sqlhash"],
				Saved:    meta["saved"],
				SQL:      body,
				Path:     path,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// parse separates the reserved header from the SQL body. Contiguous leading
// lines matching the reserved prefix are metadata; the body is everything
// from the first non-matching line to EOF, verbatim.
func parse(content string) (map[string]string, string) {
	lines := strings.Split(content, "\n")
	meta := map[string]string{}
	i := 0
	for ; i < len(lines); i++ {
		m := metaLine.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		meta[m[1]] = m[2]
	}
	return meta, strings.Join(lines[i:], "\n")
}

// writeFile writes the reserved header followed by the raw SQL body,
// creating the category directory as needed.
func writeFile(path string, sq SavedQuery) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating saved-query dir: %w", err)
	}
	var b strings.Builder
	writeMeta(&b, "name", sq.Name)
	writeMeta(&b, "category", sq.Category)
	writeMeta(&b, "provider", sq.Provider)
	writeMeta(&b, "sqlhash", sq.SQLHash)
	writeMeta(&b, "saved", sq.Saved)
	b.WriteString(sq.SQL)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing saved query %s: %w", path, err)
	}
	return nil
}

func writeMeta(b *strings.Builder, key, value string) {
	b.WriteString(metaPrefix)
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(value)
	b.WriteByte('\n')
}

// short returns the first 8 characters of a hash for compact messages.
func short(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}
