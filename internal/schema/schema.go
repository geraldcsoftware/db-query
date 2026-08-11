// Package schema persists a host's introspected schema to a local cache
// file so the CLI can skip re-introspection when the schema is already
// known. The cache lives under $XDG_CACHE_HOME/db-query/schema/ and is
// keyed on host+database; it carries table/column metadata only, never
// credentials.
package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/cache"
)

// CacheDir returns the schema cache directory: $XDG_CACHE_HOME/db-query/schema,
// else ~/.cache/db-query/schema.
func CacheDir() string { return cache.Dir("schema") }

// CachePath returns the cache file path for a host+database. The filename
// is filesystem-safe and collision-safe: a sanitized host and database for
// human legibility, plus the first 8 hex characters of the SHA-256 of
// host+NUL+database. The hash is the uniqueness guarantee — distinct
// host/database pairs never share a file even when sanitisation or a
// case-folding filesystem would collapse their readable parts, and the NUL
// separator stops a boundary shift ("ab"/"c" vs "a"/"bc") from hashing
// alike.
func CachePath(host, database string) string {
	sum := sha256.Sum256([]byte(host + "\x00" + database))
	hash := hex.EncodeToString(sum[:])[:8]
	name := fmt.Sprintf("%s_%s-%s.json", cache.Sanitize(host), cache.Sanitize(database), hash)
	return filepath.Join(CacheDir(), name)
}

// Exists reports whether a schema cache file is present at path.
func Exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Write persists rows as JSON at path, creating the cache directory as
// needed. NULL (a nil *string) and the empty string round-trip faithfully
// as JSON null versus "".
func Write(path string, rows adapter.Rows) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating schema cache dir: %w", err)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding schema: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing schema cache %s: %w", path, err)
	}
	return nil
}

// Read loads the cached rows at path.
func Read(path string) (adapter.Rows, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return adapter.Rows{}, fmt.Errorf("reading schema cache %s: %w", path, err)
	}
	var rows adapter.Rows
	if err := json.Unmarshal(data, &rows); err != nil {
		return adapter.Rows{}, fmt.Errorf("decoding schema cache %s: %w", path, err)
	}
	return rows, nil
}

// Column is one table column from a cached catalogue.
type Column struct {
	Name     string
	DataType string
	Nullable bool
}

// Table is one table's columns, derived from a cached catalogue, in cache
// (ordinal-position) order.
type Table struct {
	Schema  string
	Name    string
	Columns []Column
}

// catalogueColumns are the five columns both providers' IntrospectSQL
// produces. Matched case-insensitively: postgres's information_schema is
// lowercase, sqlserver's INFORMATION_SCHEMA is uppercase.
type catalogueColumns struct {
	schema, table, column, dataType, nullable int
}

func findCatalogueColumns(cols []string) (catalogueColumns, error) {
	idx := catalogueColumns{-1, -1, -1, -1, -1}
	for i, c := range cols {
		switch {
		case strings.EqualFold(c, "table_schema"):
			idx.schema = i
		case strings.EqualFold(c, "table_name"):
			idx.table = i
		case strings.EqualFold(c, "column_name"):
			idx.column = i
		case strings.EqualFold(c, "data_type"):
			idx.dataType = i
		case strings.EqualFold(c, "is_nullable"):
			idx.nullable = i
		}
	}
	if idx.schema < 0 || idx.table < 0 || idx.column < 0 || idx.dataType < 0 || idx.nullable < 0 {
		return catalogueColumns{}, fmt.Errorf("not a schema catalogue: missing one of table_schema/table_name/column_name/data_type/is_nullable")
	}
	return idx, nil
}

func cell(row []*string, i int) string {
	if i >= len(row) || row[i] == nil {
		return ""
	}
	return *row[i]
}

// Tables groups a cached catalogue's flat rows into one entry per table,
// columns in the cache's own order (the introspection query orders by
// ordinal position, so no re-sorting is needed here).
func Tables(rows adapter.Rows) ([]Table, error) {
	idx, err := findCatalogueColumns(rows.Columns)
	if err != nil {
		return nil, err
	}
	var out []Table
	var cur *Table
	for _, row := range rows.Rows {
		schemaName, tableName := cell(row, idx.schema), cell(row, idx.table)
		if cur == nil || cur.Schema != schemaName || cur.Name != tableName {
			out = append(out, Table{Schema: schemaName, Name: tableName})
			cur = &out[len(out)-1]
		}
		cur.Columns = append(cur.Columns, Column{
			Name:     cell(row, idx.column),
			DataType: cell(row, idx.dataType),
			Nullable: strings.EqualFold(cell(row, idx.nullable), "YES"),
		})
	}
	return out, nil
}
