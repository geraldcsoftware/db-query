package sqlscan

import "testing"

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
