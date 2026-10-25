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

func TestPositionalParams(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []int
	}{
		{"none", "SELECT 1", []int{}},
		{"in order", "SELECT $1, $2", []int{1, 2}},
		{"repeated and out of order", "SELECT $3 WHERE a = $1 OR b = $3", []int{1, 3}},
		{"multi-digit", "SELECT $12", []int{12}},
		{"beside a cast", "SELECT $1::int", []int{1}},
		{"inside a literal is data", "SELECT '$1'", []int{}},
		{"inside a comment is not code", "-- $1\nSELECT /* $2 */ 1", []int{}},
		{"inside a dollar-quoted body", "SELECT $q$ $1 $q$, $2", []int{2}},
		{"inside an anonymous dollar-quoted body", "SELECT $$ $1 $$", []int{}},
		{"part of an identifier", "SELECT a$1 FROM t", []int{}},
		{"zero is left to the server", "SELECT $0", []int{}},
		{"too large is left to the server", "SELECT $99999999999999999999", []int{}},
		{"a bare dollar", "SELECT $", []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PositionalParams(tt.sql)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
