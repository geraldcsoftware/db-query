// Package render turns the neutral Rows into output bytes. The format
// branch lives at this one pivot point, not per adapter — no
// providers × formats matrix.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// Options carries cross-format rendering choices. They are applied at the
// single pivot point (Render), so no adapter needs to know about them.
type Options struct {
	// NoHeaders omits the header line in text output and prints every row
	// tab-separated for any shape, so a 1×1 result is just the bare value.
	// In table output it drops the header row and the row-count footer.
	// JSON ignores it — its objects are already self-describing.
	NoHeaders bool

	// MaxColWidth caps a table cell at this many display cells, truncating
	// with an ellipsis beyond it; 0 means unlimited. Table output only — text
	// and json carry values whole.
	MaxColWidth int

	// Border selects the table frame: ascii, light, markdown, or none. Empty
	// means DefaultBorder, so a zero Options renders. Table output only.
	Border string
}

// AutoFormat resolves to table when stdout is a terminal and text otherwise.
// It is not a renderer: the CLI resolves it to a concrete format before the
// render pivot, which keeps every renderer a pure function of (Rows, Options).
const AutoFormat = "auto"

// Renderer renders a neutral rowset to a writer.
type Renderer interface {
	Render(w io.Writer, rows adapter.Rows, opts Options) error
}

var renderers = map[string]Renderer{
	"text":  textRenderer{},
	"json":  jsonRenderer{},
	"table": tableRenderer{},
}

// Formats returns every accepted --output value: the concrete renderers
// sorted, then AutoFormat. The shell completion and the usage text are
// generated from it so they cannot drift from the registry.
func Formats() []string {
	names := make([]string, 0, len(renderers)+1)
	for name := range renderers {
		names = append(names, name)
	}
	sort.Strings(names)
	return append(names, AutoFormat)
}

// For returns the renderer for a format name. AutoFormat is deliberately not
// a key — passing it here is a bug, not a fallback.
func For(format string) (Renderer, error) {
	r, ok := renderers[format]
	if !ok {
		return nil, fmt.Errorf("unknown output format %q (supported: %s)", format, strings.Join(Formats(), ", "))
	}
	return r, nil
}

// Valid reports whether format is something the CLI accepts — a concrete
// renderer, or AutoFormat, which it resolves to one before rendering. Use it
// for flag validation; use For at the point of rendering.
func Valid(format string) error {
	if format == AutoFormat {
		return nil
	}
	_, err := For(format)
	return err
}

// Render writes rows to w in the named format. This is the single pivot
// where the output format is selected and cross-format options are applied,
// so the choice is not duplicated per adapter.
func Render(w io.Writer, format string, rows adapter.Rows, opts Options) error {
	r, err := For(format)
	if err != nil {
		return err
	}
	return r.Render(w, rows, opts)
}

// RenderAll writes every result set of a run. One set renders exactly as
// Render would, and so does none, as an empty result. Several render one after
// another: text and table output separate them with a blank line, and json
// wraps them in an outer array holding one array per set, so a consumer can
// still tell where one result ends and the next begins.
func RenderAll(w io.Writer, format string, sets []adapter.Rows, opts Options) error {
	if len(sets) <= 1 {
		return Render(w, format, adapter.First(sets), opts)
	}
	r, err := For(format)
	if err != nil {
		return err
	}
	if format == "json" {
		return renderJSONSets(w, r, sets, opts)
	}
	for i, rows := range sets {
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if err := r.Render(w, rows, opts); err != nil {
			return err
		}
	}
	return nil
}

// renderJSONSets nests each set's own json array inside an outer one, indented
// a level so the document reads the way the single-set form does.
func renderJSONSets(w io.Writer, r Renderer, sets []adapter.Rows, opts Options) error {
	var b strings.Builder
	b.WriteString("[\n")
	for i, rows := range sets {
		var one strings.Builder
		if err := r.Render(&one, rows, opts); err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSuffix(one.String(), "\n"), "\n")
		for j, line := range lines {
			b.WriteString("  " + line)
			if j < len(lines)-1 {
				b.WriteString("\n")
			}
		}
		if i < len(sets)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// textRenderer prints a tab-separated header + rows. NULL renders as an
// empty cell (text mode is for human eyes; JSON carries fidelity).
type textRenderer struct{}

func (textRenderer) Render(w io.Writer, rows adapter.Rows, opts Options) error {
	if len(rows.Columns) == 0 {
		return nil
	}
	if !opts.NoHeaders {
		if _, err := fmt.Fprintln(w, strings.Join(rows.Columns, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rows.Rows {
		cells := make([]string, len(row))
		for i, c := range row {
			if c != nil {
				cells[i] = *c
			}
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// jsonRenderer prints an array of objects keyed by column name, keys in
// column order. nil *string renders as JSON null, &"" as "". A number column
// prints as a JSON number and a boolean column as true or false; a value its
// column's kind cannot represent, such as a NaN, stays a string.
type jsonRenderer struct{}

func (jsonRenderer) Render(w io.Writer, rows adapter.Rows, _ Options) error {
	var b strings.Builder
	b.WriteString("[")
	for ri, row := range rows.Rows {
		if ri > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  {")
		for ci, col := range rows.Columns {
			if ci > 0 {
				b.WriteString(", ")
			}
			key, err := json.Marshal(col)
			if err != nil {
				return err
			}
			b.Write(key)
			b.WriteString(": ")
			if ci < len(row) && row[ci] != nil {
				val, err := jsonValue(*row[ci], rows.Kind(ci))
				if err != nil {
					return err
				}
				b.Write(val)
			} else {
				b.WriteString("null")
			}
		}
		b.WriteString("}")
	}
	if len(rows.Rows) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// jsonValue encodes one non-NULL value by its column's kind.
//
// A number is written as the server's own text, not reparsed into a float, so
// a numeric(38,10) or a bigint reaches the document digit for digit. JSON puts
// no limit on a number's size or precision; a consumer that reads numbers as
// float64 rounds what is beyond 2^53, which is that consumer's choice to make.
func jsonValue(v string, k adapter.Kind) ([]byte, error) {
	switch k {
	case adapter.KindNumber:
		if isJSONNumber(v) {
			return []byte(v), nil
		}
	case adapter.KindBool:
		switch v {
		case "t", "true", "1":
			return []byte("true"), nil
		case "f", "false", "0":
			return []byte("false"), nil
		}
	}
	return json.Marshal(v)
}

// isJSONNumber reports whether v is, character for character, a JSON number.
// NaN, Infinity, a leading + or surrounding space are not.
func isJSONNumber(v string) bool {
	if v == "" || !(v[0] == '-' || (v[0] >= '0' && v[0] <= '9')) {
		return false
	}
	if strings.TrimSpace(v) != v {
		return false
	}
	var n json.Number
	return json.Unmarshal([]byte(v), &n) == nil
}

// Error emits a failure honoring the output format: in json mode a
// structured {"error": ...} document, so `--output json | jq` never
// breaks on the error path.
func Error(w io.Writer, format string, msg string) {
	if format == "json" {
		doc, _ := json.Marshal(map[string]string{"error": msg})
		fmt.Fprintln(w, string(doc))
		return
	}
	fmt.Fprintln(w, "db-query: "+msg)
}
