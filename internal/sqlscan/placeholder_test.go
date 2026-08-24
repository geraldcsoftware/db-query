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
		{"casts", "select somecol::date as date, sometext::jsonb as jsoncontent from t"},
		{"array slice with literals", "SELECT arr[1:3] FROM t"},
		{"array slice with an identifier", "SELECT arr[1:upper] FROM t"},
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

func TestNormaliseTSQL(t *testing.T) {
	params := map[string]string{"who": "Ada"}
	tests := []struct{ name, sql, want string }{
		{"bound placeholder", "SELECT $(who)", "SELECT '0'"},
		{"unbound is left alone", "SELECT $(other)", "SELECT $(other)"},
		{"inside a literal is data", "SELECT 'cost $(who)' AS s", "SELECT 'cost $(who)' AS s"},
		{"bracket identifier survives", "SELECT [a:b] FROM t", "SELECT [a:b] FROM t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalisePlaceholders(tt.sql, params, DialectTSQL); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
