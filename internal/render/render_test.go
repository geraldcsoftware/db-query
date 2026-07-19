package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

func ptr(s string) *string { return &s }

func sampleRows() adapter.Rows {
	return adapter.Rows{
		Columns: []string{"id", "name", "nickname"},
		Rows: [][]*string{
			{ptr("1"), ptr("Ada"), nil},
			{ptr("2"), ptr("Grace"), ptr("")},
		},
	}
}

func TestForUnknownFormat(t *testing.T) {
	if _, err := For("yaml"); err == nil {
		t.Fatal("want error for unknown format")
	}
	for _, f := range []string{"text", "json"} {
		if _, err := For(f); err != nil {
			t.Fatalf("format %q: %v", f, err)
		}
	}
}

func TestTextRenderer(t *testing.T) {
	r, _ := For("text")
	var b strings.Builder
	if err := r.Render(&b, sampleRows()); err != nil {
		t.Fatal(err)
	}
	want := "id\tname\tnickname\n1\tAda\t\n2\tGrace\t\n"
	if b.String() != want {
		t.Fatalf("text = %q, want %q", b.String(), want)
	}
}

func TestTextRendererEmpty(t *testing.T) {
	r, _ := For("text")
	var b strings.Builder
	if err := r.Render(&b, adapter.Rows{}); err != nil {
		t.Fatal(err)
	}
	if b.String() != "" {
		t.Fatalf("empty rows must render nothing, got %q", b.String())
	}
}

func TestJSONRenderer(t *testing.T) {
	r, _ := For("json")
	var b strings.Builder
	if err := r.Render(&b, sampleRows()); err != nil {
		t.Fatal(err)
	}
	var got []map[string]*string
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d", len(got))
	}
	if got[0]["nickname"] != nil {
		t.Fatal("NULL must render as JSON null")
	}
	if got[1]["nickname"] == nil || *got[1]["nickname"] != "" {
		t.Fatal("empty string must render as \"\", not null")
	}
	// Keys must appear in column order, not map order.
	if !strings.Contains(b.String(), `"id": "1", "name": "Ada", "nickname": null`) {
		t.Fatalf("keys out of column order:\n%s", b.String())
	}
}

func TestJSONRendererEmpty(t *testing.T) {
	r, _ := For("json")
	var b strings.Builder
	if err := r.Render(&b, adapter.Rows{}); err != nil {
		t.Fatal(err)
	}
	var got []any
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil || len(got) != 0 {
		t.Fatalf("empty rows must render as [], got %q (%v)", b.String(), err)
	}
}

func TestJSONRendererEscaping(t *testing.T) {
	r, _ := For("json")
	var b strings.Builder
	rows := adapter.Rows{
		Columns: []string{`we"ird`},
		Rows:    [][]*string{{ptr("line\nbreak\t\"quote\"")}},
	}
	if err := r.Render(&b, rows); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b.String())
	}
	if got[0][`we"ird`] != "line\nbreak\t\"quote\"" {
		t.Fatalf("round trip failed: %+v", got)
	}
}

func TestErrorHonorsFormat(t *testing.T) {
	t.Run("json mode emits structured error", func(t *testing.T) {
		var b strings.Builder
		Error(&b, "json", "kaboom")
		var doc map[string]string
		if err := json.Unmarshal([]byte(b.String()), &doc); err != nil {
			t.Fatalf("error output is not JSON: %q", b.String())
		}
		if doc["error"] != "kaboom" {
			t.Fatalf("doc = %v", doc)
		}
	})
	t.Run("text mode emits plain line", func(t *testing.T) {
		var b strings.Builder
		Error(&b, "text", "kaboom")
		if b.String() != "db-query: kaboom\n" {
			t.Fatalf("text error = %q", b.String())
		}
	})
}
