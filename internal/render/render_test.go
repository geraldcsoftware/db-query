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
	for _, f := range []string{"text", "json", "table"} {
		if _, err := For(f); err != nil {
			t.Fatalf("format %q: %v", f, err)
		}
	}
}

// TestForRejectsAuto pins that auto is not a renderer. Resolving it is the
// CLI's job; if For accepted it as a synonym for something, a caller could
// render "auto" without ever probing the terminal.
func TestForRejectsAuto(t *testing.T) {
	if _, err := For(AutoFormat); err == nil {
		t.Fatal("For must reject auto — it is resolved in the CLI, not here")
	}
}

func TestValid(t *testing.T) {
	for _, f := range []string{"text", "json", "table", AutoFormat} {
		if err := Valid(f); err != nil {
			t.Errorf("Valid(%q) = %v, want nil", f, err)
		}
	}
	if err := Valid("yaml"); err == nil {
		t.Error("Valid(yaml) must fail")
	}
}

// TestFormats pins the advertised value list. The zsh completion and the
// usage text are generated from Formats, so this is what stops them drifting
// apart from the renderer registry.
func TestFormats(t *testing.T) {
	got := strings.Join(Formats(), " ")
	want := "json table text auto"
	if got != want {
		t.Fatalf("Formats() = %q, want %q", got, want)
	}
}

// TestForErrorListsEveryFormat guards the operator-facing message: a mistyped
// --output should name all the real options, including auto.
func TestForErrorListsEveryFormat(t *testing.T) {
	_, err := For("yaml")
	if err == nil {
		t.Fatal("want error")
	}
	for _, f := range Formats() {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error %q does not mention %q", err, f)
		}
	}
}

func TestTextRenderer(t *testing.T) {
	r, _ := For("text")
	var b strings.Builder
	if err := r.Render(&b, sampleRows(), Options{}); err != nil {
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
	if err := r.Render(&b, adapter.Rows{}, Options{}); err != nil {
		t.Fatal(err)
	}
	if b.String() != "" {
		t.Fatalf("empty rows must render nothing, got %q", b.String())
	}
}

func TestTextRendererNoHeaders(t *testing.T) {
	r, _ := For("text")
	var b strings.Builder
	if err := r.Render(&b, sampleRows(), Options{NoHeaders: true}); err != nil {
		t.Fatal(err)
	}
	// Header line dropped; rows stay tab-separated.
	want := "1\tAda\t\n2\tGrace\t\n"
	if b.String() != want {
		t.Fatalf("no-headers text = %q, want %q", b.String(), want)
	}
}

// TestTextRendererNoHeaders1x1 pins the locked shape: a single-cell result
// under --no-headers is just the bare value plus a newline.
func TestTextRendererNoHeaders1x1(t *testing.T) {
	r, _ := For("text")
	var b strings.Builder
	rows := adapter.Rows{Columns: []string{"count"}, Rows: [][]*string{{ptr("42")}}}
	if err := r.Render(&b, rows, Options{NoHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if b.String() != "42\n" {
		t.Fatalf("1×1 no-headers = %q, want %q", b.String(), "42\n")
	}
}

func TestJSONRenderer(t *testing.T) {
	r, _ := For("json")
	var b strings.Builder
	if err := r.Render(&b, sampleRows(), Options{}); err != nil {
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

func TestJSONRendererTypedColumns(t *testing.T) {
	rows := adapter.Rows{
		Columns: []string{"n", "big", "price", "f", "ok", "pg_ok", "name", "nan", "flag"},
		Kinds: []adapter.Kind{
			adapter.KindNumber, adapter.KindNumber, adapter.KindNumber, adapter.KindNumber,
			adapter.KindBool, adapter.KindBool, adapter.KindText, adapter.KindNumber,
		}, // one short: the last column has no kind and is text
		Rows: [][]*string{
			{ptr("42"), ptr("123456789012345678901234567890"), ptr("1.50"), ptr("1e+21"),
				ptr("1"), ptr("f"), ptr("007"), ptr("NaN"), ptr("1")},
			{nil, ptr("-0"), ptr("0.0000"), ptr("-2.5"), ptr("0"), ptr("t"), ptr("x"), ptr("Infinity"), nil},
		},
	}
	var b strings.Builder
	if err := (jsonRenderer{}).Render(&b, rows, Options{}); err != nil {
		t.Fatal(err)
	}
	want := `[
  {"n": 42, "big": 123456789012345678901234567890, "price": 1.50, "f": 1e+21, "ok": true, "pg_ok": false, "name": "007", "nan": "NaN", "flag": "1"},
  {"n": null, "big": -0, "price": 0.0000, "f": -2.5, "ok": false, "pg_ok": true, "name": "x", "nan": "Infinity", "flag": null}
]
`
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
	if !json.Valid([]byte(b.String())) {
		t.Fatal("output is not valid JSON")
	}
}

func TestIsJSONNumber(t *testing.T) {
	for v, want := range map[string]bool{
		"0": true, "-1": true, "3.14": true, "1E5": true, "1e-5": true, "-0.0": true,
		"": false, "+1": false, "01": false, ".5": false, "1.": false, " 1": false,
		"1 ": false, "NaN": false, "Infinity": false, "-Infinity": false, "0x1F": false, "1_000": false,
	} {
		if got := isJSONNumber(v); got != want {
			t.Errorf("isJSONNumber(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestJSONRendererEmpty(t *testing.T) {
	r, _ := For("json")
	var b strings.Builder
	if err := r.Render(&b, adapter.Rows{}, Options{}); err != nil {
		t.Fatal(err)
	}
	var got []any
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil || len(got) != 0 {
		t.Fatalf("empty rows must render as [], got %q (%v)", b.String(), err)
	}
}

// TestJSONRendererNoHeadersNoOp pins that --no-headers does not affect JSON:
// the output is still an array of self-describing objects.
func TestJSONRendererNoHeadersNoOp(t *testing.T) {
	r, _ := For("json")
	var plain, noHdr strings.Builder
	if err := r.Render(&plain, sampleRows(), Options{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Render(&noHdr, sampleRows(), Options{NoHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if plain.String() != noHdr.String() {
		t.Fatalf("NoHeaders must be a no-op for JSON:\n plain = %q\n noHdr = %q", plain.String(), noHdr.String())
	}
}

func TestJSONRendererEscaping(t *testing.T) {
	r, _ := For("json")
	var b strings.Builder
	rows := adapter.Rows{
		Columns: []string{`we"ird`},
		Rows:    [][]*string{{ptr("line\nbreak\t\"quote\"")}},
	}
	if err := r.Render(&b, rows, Options{}); err != nil {
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

func TestRenderPivot(t *testing.T) {
	t.Run("dispatches to text with options", func(t *testing.T) {
		var b strings.Builder
		if err := Render(&b, "text", sampleRows(), Options{NoHeaders: true}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "id\tname") {
			t.Fatalf("NoHeaders not honoured through pivot: %q", b.String())
		}
	})
	t.Run("unknown format errors", func(t *testing.T) {
		var b strings.Builder
		if err := Render(&b, "yaml", sampleRows(), Options{}); err == nil {
			t.Fatal("want error for unknown format")
		}
	})
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

func TestRenderAllSeveralSets(t *testing.T) {
	one := "1"
	two := "2"
	sets := []adapter.Rows{
		{Columns: []string{"a"}, Rows: [][]*string{{&one}}},
		{Columns: []string{"b"}, Rows: [][]*string{{&two}, {nil}}},
	}
	tests := []struct{ format, want string }{
		{"text", "a\n1\n\nb\n2\n\n"},
		{"json", "[\n  [\n    {\"a\": \"1\"}\n  ],\n  [\n    {\"b\": \"2\"},\n    {\"b\": null}\n  ]\n]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			var b strings.Builder
			if err := RenderAll(&b, tt.format, sets, Options{}); err != nil {
				t.Fatal(err)
			}
			if b.String() != tt.want {
				t.Errorf("got %q, want %q", b.String(), tt.want)
			}
		})
	}
}

func TestRenderAllOneSetMatchesRender(t *testing.T) {
	v := "x"
	rows := adapter.Rows{Columns: []string{"c"}, Rows: [][]*string{{&v}}}
	for _, format := range []string{"text", "json", "table"} {
		var all, single strings.Builder
		if err := RenderAll(&all, format, []adapter.Rows{rows}, Options{}); err != nil {
			t.Fatal(err)
		}
		if err := Render(&single, format, rows, Options{}); err != nil {
			t.Fatal(err)
		}
		if all.String() != single.String() {
			t.Errorf("%s: RenderAll %q, Render %q", format, all.String(), single.String())
		}
	}
	var none, empty strings.Builder
	_ = RenderAll(&none, "json", nil, Options{})
	_ = Render(&empty, "json", adapter.Rows{}, Options{})
	if none.String() != empty.String() {
		t.Errorf("no sets: got %q, want %q", none.String(), empty.String())
	}
}
