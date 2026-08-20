package sqlscan

import (
	"reflect"
	"testing"
)

func TestScanStatements(t *testing.T) {
	tests := []struct {
		name    string
		dialect Dialect
		sql     string
		want    []string
	}{
		{"single", DialectPostgres, "SELECT 1", []string{"SELECT 1"}},
		{"trailing semicolon", DialectPostgres, "SELECT 1;", []string{"SELECT 1"}},
		{"two statements", DialectPostgres, "SELECT 1; DROP TABLE t;", []string{"SELECT 1", "DROP TABLE t"}},
		{
			// The semicolon is inside a literal, so it must not split.
			"semicolon in a literal", DialectPostgres,
			"SELECT 'a;b' AS s", []string{"SELECT 'a;b' AS s"},
		},
		{
			// '' is an escaped quote, not the end of the literal, so the
			// following semicolon is still inside it.
			"escaped quote", DialectPostgres,
			"SELECT 'it''s; fine' AS s", []string{"SELECT 'it''s; fine' AS s"},
		},
		{
			// A dollar-quoted body carries semicolons that belong to it.
			"dollar quoting", DialectPostgres,
			"DO $$ BEGIN EXECUTE 'DROP TABLE t'; END $$", []string{"DO $$ BEGIN EXECUTE 'DROP TABLE t'; END $$"},
		},
		{
			"tagged dollar quoting", DialectPostgres,
			"SELECT $tag$a;b$tag$", []string{"SELECT $tag$a;b$tag$"},
		},
		{
			"line comment hides a semicolon", DialectPostgres,
			"SELECT 1 -- ; DROP TABLE t\n", []string{"SELECT 1"},
		},
		{
			"nested block comment", DialectPostgres,
			"/* outer /* inner */ still comment */ SELECT 1", []string{"SELECT 1"},
		},
		{
			"quoted identifier", DialectPostgres,
			`SELECT * FROM "weird;name"`, []string{`SELECT * FROM "weird;name"`},
		},
		{
			"bracket identifier is tsql only", DialectTSQL,
			"SELECT * FROM [weird;name]", []string{"SELECT * FROM [weird;name]"},
		},
		{
			"go separates tsql batches", DialectTSQL,
			"SELECT 1\nGO\nSELECT 2\n", []string{"SELECT 1", "SELECT 2"},
		},
		{
			"go with a repeat count", DialectTSQL,
			"SELECT 1\nGO 5\nSELECT 2", []string{"SELECT 1", "SELECT 2"},
		},
		{
			// "GO" leading an identifier is not a batch separator.
			"go is not a prefix match", DialectTSQL,
			"SELECT 1\nGOOSE\n", []string{"SELECT 1\nGOOSE"},
		},
		{"empty", DialectPostgres, "   \n  ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := Scan(tt.sql, tt.dialect)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statements %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if normalise(got[i]) != normalise(tt.want[i]) {
					t.Errorf("statement %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestScanDirectives(t *testing.T) {
	tests := []struct {
		name    string
		dialect Dialect
		sql     string
		want    []string
	}{
		{"psql shell-out", DialectPostgres, "SELECT 1;\n\\! rm -rf /", []string{`\! rm -rf /`}},
		{"psql include", DialectPostgres, "\\i /tmp/payload.sql", []string{`\i /tmp/payload.sql`}},
		{"psql gexec", DialectPostgres, "SELECT 'DROP TABLE t'\n\\gexec", []string{`\gexec`}},
		{"psql copy to program", DialectPostgres, "\\copy (SELECT 1) TO PROGRAM 'sh'", []string{`\copy (SELECT 1) TO PROGRAM 'sh'`}},
		{"indented still counts", DialectPostgres, "SELECT 1;\n   \\! whoami", []string{`\! whoami`}},
		{"sqlcmd shell-out", DialectTSQL, "SELECT 1\nGO\n:!! whoami", []string{":!! whoami"}},
		{"sqlcmd include", DialectTSQL, ":r payload.sql", []string{":r payload.sql"}},
		{"none", DialectPostgres, "SELECT 1", nil},
		{
			// A backslash inside a literal is data, not a directive. Reporting
			// it would refuse legitimate SQL.
			"backslash inside a literal", DialectPostgres,
			`SELECT 'a\b' AS s`, nil,
		},
		{
			// Nor is one mid-line: directives are line-initial.
			"backslash mid-line", DialectPostgres,
			`SELECT 1 \ 2`, nil,
		},
		{
			// A colon leads postgres parameter syntax and must not be read as
			// a directive there.
			"postgres param syntax", DialectPostgres,
			":'who'", nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := Scan(tt.sql, tt.dialect)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// normalise collapses whitespace so a test compares statement content rather
// than the incidental spacing a comment or newline leaves behind.
func normalise(s string) string {
	var out []rune
	space := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			space = true
			continue
		}
		if space && len(out) > 0 {
			out = append(out, ' ')
		}
		space = false
		out = append(out, r)
	}
	return string(out)
}

func TestScanRefusesLegacySqlcmdShellOut(t *testing.T) {
	// sqlcmd spells shell-out `:!!` now and `!!` in older versions. Catching
	// only the colon form would leave the older spelling live.
	for _, sql := range []string{":!! whoami", "!! whoami", "SELECT 1\nGO\n!!dir"} {
		if _, got := Scan(sql, DialectTSQL); len(got) == 0 {
			t.Errorf("%q: no directive reported", sql)
		}
	}
}
