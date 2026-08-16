package tui

import (
	"strconv"
	"strings"
	"sync"

	"github.com/geraldcsoftware/db-query/internal/schema"
)

// schemaSource is the embedded editor's completion source: the catalogue the
// session is browsing, plus SQL's own vocabulary.
//
// It is read on the goroutine Neovim's requests arrive on and replaced on the
// event loop's, when a database switch rebuilds the Schema pane, so both go
// through the mutex. Every answer is served from memory: Neovim's core is
// single threaded, so a source that went to disk or to the database would
// freeze the whole pane rather than merely the popup, and the two-call protocol
// would pay for it twice per keystroke.
type schemaSource struct {
	mu sync.RWMutex

	tables []schema.Table

	// ambiguous holds the table names that occur in more than one schema. Those
	// are offered qualified, since inserting the bare name would name two
	// tables; every other table is offered plain, which is what a user with a
	// default search path would type.
	ambiguous map[string]bool
}

func newSchemaSource() *schemaSource { return &schemaSource{} }

// setTables replaces the catalogue. A cold cache is an empty catalogue rather
// than an error: the pane still completes keywords, which is exactly as much as
// it can honestly offer before anything has been introspected.
func (s *schemaSource) setTables(tables []schema.Table) {
	counts := make(map[string]int, len(tables))
	for _, t := range tables {
		counts[strings.ToLower(t.Name)]++
	}
	ambiguous := map[string]bool{}
	for name, n := range counts {
		if n > 1 {
			ambiguous[name] = true
		}
	}

	s.mu.Lock()
	s.tables, s.ambiguous = tables, ambiguous
	s.mu.Unlock()
}

// Candidates answers one completion context.
//
// Behind a qualifier it offers that table's columns and nothing else, the
// qualifier having already said what is wanted. Without one it offers the
// tables, the columns of the tables this query names, and the keywords —
// deliberately not every column of every table, since a column belonging to a
// table the query never mentions is not one the statement could select.
//
// Nothing is offered for an empty unqualified prefix. A dot is a question and
// deserves an answer; a space is not.
func (s *schemaSource) Candidates(qualifier, prefix string, buflines []string) []map[string]any {
	s.mu.RLock()
	tables, ambiguous := s.tables, s.ambiguous
	s.mu.RUnlock()

	if qualifier != "" {
		t, ok := resolveQualifier(qualifier, buflines, tables)
		if !ok {
			return nil
		}
		return columnItems(t, prefix)
	}
	if prefix == "" {
		return nil
	}

	// Ordered by how specific each group is to the statement being written.
	// Neovim scores the candidates itself and sorts on that score, so this
	// decides only what happens between equally good matches.
	out := make([]map[string]any, 0, 32)
	for _, t := range inScopeTables(buflines, tables) {
		out = append(out, columnItems(t, prefix)...)
	}
	for _, t := range tables {
		if !fuzzyMatch(t.Name, prefix) {
			continue
		}
		out = append(out, item(tableWord(t, ambiguous), "t", plural(len(t.Columns)), qualifiedName(t)))
	}
	for _, k := range sqlKeywordList {
		if fuzzyMatch(k, prefix) {
			out = append(out, item(keywordInTypedCase(prefix, k), "v", "keyword", ""))
		}
	}
	return out
}

// columnItems is one table's matching columns, each carrying its type where the
// popup shows it: menu, because kind is restricted to a single letter from a
// fixed set and cannot hold one.
func columnItems(t schema.Table, prefix string) []map[string]any {
	out := make([]map[string]any, 0, len(t.Columns))
	for _, c := range t.Columns {
		if !fuzzyMatch(c.Name, prefix) {
			continue
		}
		info := qualifiedName(t) + "." + c.Name + " " + c.DataType
		if !c.Nullable {
			info += " not null"
		}
		out = append(out, item(c.Name, "m", c.DataType, info))
	}
	return out
}

func item(word, kind, menu, info string) map[string]any {
	m := map[string]any{"word": word, "kind": kind, "menu": menu}
	if info != "" {
		m["info"] = info
	}
	return m
}

// tableWord is the text inserted for a table: its bare name, which is what a
// user with a default search path writes, unless that name occurs in more than
// one schema and only the qualified form identifies it.
func tableWord(t schema.Table, ambiguous map[string]bool) string {
	if t.Schema != "" && ambiguous[strings.ToLower(t.Name)] {
		return t.Schema + "." + t.Name
	}
	return t.Name
}

func qualifiedName(t schema.Table) string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

func plural(n int) string {
	if n == 1 {
		return "1 column"
	}
	return strconv.Itoa(n) + " columns"
}

// resolveQualifier turns whatever preceded a dot into a table. A qualifier that
// names a table needs no lookup at all; anything else has to be an alias, which
// only the query's own FROM and JOIN clauses can explain.
func resolveQualifier(qualifier string, buflines []string, tables []schema.Table) (schema.Table, bool) {
	if t, ok := findTable(qualifier, tables); ok {
		return t, true
	}
	for _, ref := range scopeOf(buflines) {
		if ref.alias != "" && strings.EqualFold(unquote(ref.alias), qualifier) {
			return findTable(ref.name, tables)
		}
	}
	return schema.Table{}, false
}

// inScopeTables is the tables this query names, in the order the query names
// them, without repeats.
func inScopeTables(buflines []string, tables []schema.Table) []schema.Table {
	var out []schema.Table
	seen := map[string]bool{}
	for _, ref := range scopeOf(buflines) {
		t, ok := findTable(ref.name, tables)
		if !ok {
			continue
		}
		key := qualifiedName(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// findTable matches a written name against the catalogue, by bare name or by
// schema-qualified name, ignoring case and whatever quoting it was written
// with.
func findTable(name string, tables []schema.Table) (schema.Table, bool) {
	name = unquote(name)
	for _, t := range tables {
		if strings.EqualFold(t.Name, name) || strings.EqualFold(qualifiedName(t), name) {
			return t, true
		}
	}
	return schema.Table{}, false
}

// fuzzyMatch is the membership test Neovim's own fuzzy matching uses: the typed
// characters appear in order, not necessarily together, ignoring case. Filtering
// on anything stricter here would drop candidates Neovim would have kept, and
// filtering on nothing would put the whole catalogue on the wire once per
// keystroke.
func fuzzyMatch(candidate, prefix string) bool {
	if prefix == "" {
		return true
	}
	c, p := strings.ToLower(candidate), strings.ToLower(prefix)
	i := 0
	for _, r := range c {
		if i == len(p) {
			break
		}
		if rune(p[i]) == r {
			i++
		}
	}
	return i == len(p)
}

// sqlKeywordList is SQL's own vocabulary, the part of completion that needs
// nothing from the connection and is therefore offered before anything has been
// introspected.
var sqlKeywordList = []string{
	"AND", "AS", "ASC", "BETWEEN", "BY", "CASE", "COUNT", "CREATE", "DELETE",
	"DESC", "DISTINCT", "ELSE", "END", "EXISTS", "FROM", "FULL", "GROUP",
	"HAVING", "ILIKE", "IN", "INNER", "INSERT", "INTO", "IS", "JOIN", "LEFT",
	"LIKE", "LIMIT", "NOT", "NULL", "OFFSET", "ON", "OR", "ORDER", "OUTER",
	"RETURNING", "RIGHT", "SELECT", "SET", "SUM", "THEN", "UNION", "UPDATE",
	"VALUES", "WHEN", "WHERE", "WITH",
}

// keywordInTypedCase offers a keyword in the case the user is writing in: the
// characters already typed are kept exactly as typed and only the rest is
// supplied, in upper case, which is the case db-query writes its own generated
// SQL in. A table or a column gets no such treatment — its case belongs to the
// database, and offering a name spelled differently would be offering one that
// does not exist.
func keywordInTypedCase(prefix, keyword string) string {
	if len(prefix) > len(keyword) || !strings.EqualFold(keyword[:len(prefix)], prefix) {
		// A fuzzy match rather than a prefix match, so there is no typed head to
		// preserve and the keyword stands as it is written here.
		return keyword
	}
	rest := keyword[len(prefix):]
	if prefix != strings.ToUpper(prefix) {
		rest = strings.ToLower(rest)
	}
	return prefix + rest
}
