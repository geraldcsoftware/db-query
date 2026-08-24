//go:build cgo

package sqlscan

import "testing"

// A colon means several things in SQL, and only one of them is a placeholder.
// Rewriting the others produces SQL that no longer parses, which classifies
// opaque and is refused, so each of these is a query the tool would wrongly
// have blocked.
func TestNormaliseLeavesNonPlaceholderColonsAlone(t *testing.T) {
	params := map[string]string{"who": "Ada", "id": "1"}
	unchanged := []struct{ name, sql string }{
		{"time format in a literal", "select to_char(created, 'DD Mon, HH:MM:SS') from t"},
		{"minute format in a literal", "select to_char(created, 'DD/MM HH:MI:SS') from t"},
		{"24-hour time format", "select to_char(now(), 'HH24:MI:SS')"},
		{"an interval literal", "select interval '1:30'"},
		{"casts", "select somecol::date as date, sometext::jsonb as jsoncontent from t"},
		{"a cast feeding a json operator", "select col::jsonb->>'someProperty' from t"},
		{"a schema-qualified cast", "select col::public.mytype from t"},
		{"nested casts", "select (col::text)::int from t"},
		{"array slice with literals", "SELECT arr[1:3] FROM t"},
		{"array slice from zero", "SELECT arr[0:3] FROM t"},
		{"array slice with the lower bound omitted", "SELECT arr[:3] FROM t"},
		{"array slice with the upper bound omitted", "SELECT arr[2:] FROM t"},
		{"array slice with both bounds omitted", "SELECT arr[:] FROM t"},
		{"chained array slices", "SELECT arr[1:3][2:4] FROM t"},
		{"array slice with an identifier", "SELECT arr[1:upper] FROM t"},
		{"a colon in a literal", "SELECT 'some: thing'"},
		{"a cast written inside a literal", "SELECT 'a::b'"},
		// := is named-argument notation, and `id` is bound: the pair is a
		// single token to the scanner, so the binding cannot reach it.
		{"named-argument notation", "SELECT f(id := 1)"},
		{"named-argument notation without spaces", "SELECT f(id:=1)"},
		{"a url in a literal", "SELECT * FROM t WHERE url = 'https://x/y'"},
		{"a colon in a line comment", "SELECT 1 -- note: something\n"},
		{"a colon in a block comment", "/* note: something */ SELECT 1"},
		{"a colon in a dollar-quoted body", "SELECT $tag$ a:b $tag$"},
		{"a colon in an escape string", `SELECT E'a\'b:c' AS s`},
		{"a doubled quote inside a literal", "SELECT 'it''s 10:30' AS s"},
		{"an unbound name is not a placeholder", "SELECT * FROM t WHERE x = :notbound"},
		{"json path operators", `SELECT data #> '{a,b}' FROM t`},
	}
	for _, tt := range unchanged {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalisePlaceholders(tt.sql, params, DialectPostgres); got != tt.sql {
				t.Errorf("rewritten:\n  in:  %s\n  out: %s", tt.sql, got)
			}
		})
	}
}

func TestNormaliseReplacesBoundPlaceholders(t *testing.T) {
	params := map[string]string{"who": "Ada"}
	tests := []struct{ name, sql, want string }{
		{"quoted literal form", "SELECT :'who'", "SELECT '0'"},
		{"quoted identifier form", `SELECT :"who"`, `SELECT "c0"`},
		{"bare form", "SELECT :who", "SELECT '0'"},
		{"among other colons", "SELECT a::text, arr[1:3], :'who'", "SELECT a::text, arr[1:3], '0'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalisePlaceholders(tt.sql, params, DialectPostgres); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormaliseIsANoOpWithoutParams(t *testing.T) {
	sql := "SELECT :'who', arr[1:3], to_char(t, 'HH:MM')"
	if got := NormalisePlaceholders(sql, nil, DialectPostgres); got != sql {
		t.Errorf("got %q, want it untouched", got)
	}
}

// Adjacency is psql's own rule, and it is what the scanner's offsets express.
// The hand-rolled walk could not see it: it had only the characters, not the
// token boundaries.
func TestNormaliseRequiresTheNameToAbutTheColon(t *testing.T) {
	params := map[string]string{"who": "Ada", "3": "x"}
	for _, sql := range []string{
		"SELECT arr[1 : 3] FROM t", // a slice, spaced out
		"SELECT : 'who' FROM t",    // a colon and a separate literal
		"SELECT :\n'who' FROM t",   // separated by a newline
	} {
		if got := NormalisePlaceholders(sql, params, DialectPostgres); got != sql {
			t.Errorf("rewritten:\n  in:  %s\n  out: %s", sql, got)
		}
	}
}

// Anything the scanner rejects the parser rejects too, so the submission
// classifies opaque and is refused. Returning it untouched keeps that path
// honest rather than handing the parser something this code invented.
func TestNormaliseLeavesUnscannableSQLAlone(t *testing.T) {
	params := map[string]string{"who": "Ada"}
	sql := "SELECT 'unterminated :'who'"
	if got := NormalisePlaceholders(sql, params, DialectPostgres); got != sql {
		t.Errorf("got %q, want it untouched", got)
	}
}

// Whatever the normaliser produces has to be parseable, or classification
// turns a valid query into a refusal. That is the failure the previous
// implementation actually shipped.
func TestNormalisedSQLStillParses(t *testing.T) {
	params := map[string]string{"who": "Ada", "id": "1"}
	for _, sql := range []string{
		"SELECT to_char(created, 'DD Mon, HH:MM:SS') FROM t",
		"SELECT to_char(created, 'DD/MM HH:MI:SS') FROM t WHERE n = :'who'",
		"SELECT a::date, b::jsonb FROM t",
		"SELECT a::jsonb->>'someProperty' FROM t WHERE n = :'who'",
		"SELECT arr[1:3] FROM t",
		"SELECT arr[:3], arr[2:], arr[0:3] FROM t WHERE n = :'who'",
		"SELECT f(id := 1) FROM t WHERE n = :'who'",
		"SELECT :'who'::date FROM t",
		"SELECT * FROM t WHERE name = :'who' AND id = :id",
		`SELECT * FROM :"who"`,
		"SELECT E'a\\'b:c', $tag$ x:y $tag$ FROM t WHERE n = :'who'",
	} {
		out := NormalisePlaceholders(sql, params, DialectPostgres)
		if v := ClassifyPostgres(out); v.Class == ClassOpaque {
			t.Errorf("normalised form does not parse:\n  in:  %s\n  out: %s\n  %+v", sql, out, v.Statements)
		}
	}
}
