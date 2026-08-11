package tui

import (
	"strconv"
	"strings"

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

	// scroll keeps the cursor on screen once the catalogue — table rows plus
	// the columns of every expanded table — is taller than the pane.
	scroll listScroll
}

// setSize records how many rows the layout gives the pane's content and keeps
// the cursor visible in them, so a resize cannot leave it off screen.
func (p *schemaPane) setSize(h int) {
	p.scroll.setHeight(h)
	p.scroll.follow(p.cursorLine(), p.totalLines())
}

// cursorLine is the rendered line the cursor's table sits on, which is its
// index plus the columns of every expanded table above it.
func (p schemaPane) cursorLine() int {
	line := 0
	for i := 0; i < p.cursor && i < len(p.tables); i++ {
		line++
		if p.expanded[i] {
			line += len(p.tables[i].Columns)
		}
	}
	return line
}

// totalLines is how many rows view renders in full.
func (p schemaPane) totalLines() int {
	n := len(p.tables)
	for i, t := range p.tables {
		if p.expanded[i] {
			n += len(t.Columns)
		}
	}
	return n
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
	p.scroll.follow(p.cursorLine(), p.totalLines())
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

// A table row leads with a disclosure marker and a column row with the blank
// that stands in for one, so an expanded table's column names line up under
// the table name rather than under its marker.
const (
	markerCollapsed = "▶ "
	markerExpanded  = "▼ "
	markerColumn    = "  "
)

// view renders the catalogue into a pane w cells wide: one row per table with
// its column count against the pane's right edge, and an expanded table's
// columns listed underneath with their data types. The count is len(Columns),
// which the cache already holds — a per-table row count would mean a query
// per table on every frame. Only the rows around the cursor are returned, so a
// catalogue longer than the pane scrolls rather than being cut off at the row
// the pane happens to end on.
func (p schemaPane) view(w int) string {
	if p.hint != "" {
		return indentLines(hintStyle.Render(p.hint))
	}
	rows := make([]string, 0, len(p.tables))
	for i, t := range p.tables {
		marker := markerCollapsed
		if p.expanded[i] {
			marker = markerExpanded
		}
		rows = append(rows, listRow(w, i == p.cursor, marker, t.Name, strconv.Itoa(len(t.Columns))))
		if !p.expanded[i] {
			continue
		}
		for _, c := range t.Columns {
			rows = append(rows, listRow(w, false, markerColumn, c.Name, c.DataType))
		}
	}
	return strings.Join(p.scroll.window(rows), "\n")
}
