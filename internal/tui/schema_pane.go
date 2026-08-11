package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/schema"
)

// schemaPane browses a host's cached schema catalogue. It reads the cache
// file only and never triggers a live introspect: a missing or unreadable
// cache surfaces as hint text rather than an I/O call. cursor indexes into
// tables; expanded tracks which cursor positions currently show their
// column list.
type schemaPane struct {
	tables   []schema.Table
	cursor   int
	expanded map[int]bool
	hint     string
}

func newSchemaPane(host config.HostConfig) schemaPane {
	path := schema.CachePath(host.Host, host.Database)
	if !schema.Exists(path) {
		return schemaPane{
			expanded: map[int]bool{},
			hint:     "no cached schema — run 'db-query introspect --host " + host.Name + "' first",
		}
	}
	rows, err := schema.Read(path)
	if err != nil {
		return schemaPane{expanded: map[int]bool{}, hint: "schema cache unreadable: " + err.Error()}
	}
	tables, err := schema.Tables(rows)
	if err != nil {
		return schemaPane{expanded: map[int]bool{}, hint: "schema cache is not a catalogue: " + err.Error()}
	}
	return schemaPane{tables: tables, expanded: map[int]bool{}}
}

func (p schemaPane) update(msg tea.Msg) (schemaPane, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok || len(p.tables) == 0 {
		return p, nil
	}
	switch keyMsg.String() {
	case "up":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down":
		if p.cursor < len(p.tables)-1 {
			p.cursor++
		}
	case "enter":
		p.expanded = cloneExpanded(p.expanded)
		p.expanded[p.cursor] = !p.expanded[p.cursor]
	}
	return p, nil
}

func cloneExpanded(m map[int]bool) map[int]bool {
	out := make(map[int]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// selectedTable returns the table currently under the cursor, if any.
func (p schemaPane) selectedTable() (schema.Table, bool) {
	if p.cursor < 0 || p.cursor >= len(p.tables) {
		return schema.Table{}, false
	}
	return p.tables[p.cursor], true
}

func (p schemaPane) view() string {
	if p.hint != "" {
		return p.hint
	}
	var out string
	for i, t := range p.tables {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		mark := "▸"
		if p.expanded[i] {
			mark = "▾"
		}
		out += cursor + mark + " " + t.Name + "\n"
		if p.expanded[i] {
			for _, c := range t.Columns {
				out += "      " + c.Name + " " + c.DataType + "\n"
			}
		}
	}
	return out
}
