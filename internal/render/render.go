// Package render turns the neutral Rows into output bytes. The format
// branch lives at this one pivot point, not per adapter — no
// providers × formats matrix.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// Options carries cross-format rendering choices. They are applied at the
// single pivot point (Render), so no adapter needs to know about them.
type Options struct {
	// NoHeaders omits the header line in text output and prints every row
	// tab-separated for any shape, so a 1×1 result is just the bare value.
	// JSON ignores it — its objects are already self-describing.
	NoHeaders bool
}

// Renderer renders a neutral rowset to a writer.
type Renderer interface {
	Render(w io.Writer, rows adapter.Rows, opts Options) error
}

var renderers = map[string]Renderer{
	"text": textRenderer{},
	"json": jsonRenderer{},
}

// For returns the renderer for a format name.
func For(format string) (Renderer, error) {
	r, ok := renderers[format]
	if !ok {
		return nil, fmt.Errorf("unknown output format %q (supported: text, json)", format)
	}
	return r, nil
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
// column order. nil *string renders as JSON null, &"" as "".
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
				val, err := json.Marshal(*row[ci])
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
