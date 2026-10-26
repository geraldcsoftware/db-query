package sqlscan

import "testing"

func TestNamedParams(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"none", "SELECT 1", nil},
		{"in order of appearance", "SELECT @b, @a", []string{"b", "a"}},
		{"repeated once, first spelling kept", "SELECT @Id WHERE x = @id OR y = @ID", []string{"Id"}},
		{"underscores and digits", "SELECT @min_id2", []string{"min_id2"}},
		{"system function is not a reference", "SELECT @@ROWCOUNT, @@VERSION", nil},
		{"system function beside a reference", "SELECT @@ROWCOUNT + @n", []string{"n"}},
		{"inside a literal is data", "SELECT 'mail@example.com', N'@x'", nil},
		{"inside a comment is not code", "-- @a\nSELECT /* @b */ 1", nil},
		{"inside a bracketed identifier", "SELECT [@a] FROM t", nil},
		{"inside a quoted identifier", `SELECT "@a" FROM t`, nil},
		{"part of an identifier", "SELECT a@b FROM t", nil},
		{"a local variable is still a reference", "DECLARE @x int = 1; SELECT @x", []string{"x"}},
		{"a bare at sign", "SELECT @", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NamedParams(tt.sql)
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

func TestBatches(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []Batch
	}{
		{"one batch, statements kept together", "DECLARE @x int = 1;\nSELECT @x;",
			[]Batch{{"DECLARE @x int = 1;\nSELECT @x;", 1}}},
		{"split at GO", "SELECT 1\nGO\nSELECT 2\n",
			[]Batch{{"SELECT 1", 1}, {"SELECT 2", 1}}},
		{"lower case and indented", "SELECT 1\n  go  \nSELECT 2",
			[]Batch{{"SELECT 1", 1}, {"SELECT 2", 1}}},
		{"repeat count", "INSERT INTO t DEFAULT VALUES\nGO 3\n",
			[]Batch{{"INSERT INTO t DEFAULT VALUES", 3}}},
		{"zero count runs once", "SELECT 1\nGO 0", []Batch{{"SELECT 1", 1}}},
		{"empty batches dropped", "GO\nGO\nSELECT 1\nGO\n\nGO", []Batch{{"SELECT 1", 1}}},
		{"GO inside a literal is data", "SELECT '\nGO\n' AS s", []Batch{{"SELECT '\nGO\n' AS s", 1}}},
		{"GO inside a block comment is not a separator", "SELECT 1 /*\nGO\n*/ AS a",
			[]Batch{{"SELECT 1 /*\nGO\n*/ AS a", 1}}},
		{"GO after a line comment still separates", "SELECT 1 -- note\nGO\nSELECT 2",
			[]Batch{{"SELECT 1 -- note", 1}, {"SELECT 2", 1}}},
		{"GOTO is not a separator", "GOTO done\ndone:\nSELECT 1",
			[]Batch{{"GOTO done\ndone:\nSELECT 1", 1}}},
		{"GO mid-line is not a separator", "SELECT 1 GO", []Batch{{"SELECT 1 GO", 1}}},
		{"GO5 is an identifier, not a separator", "SELECT a,\nGO5\nFROM t", []Batch{{"SELECT a,\nGO5\nFROM t", 1}}},
		{"a count needs no trailing space", "SELECT 1\nGO 2", []Batch{{"SELECT 1", 2}}},
		{"two counts are not a separator", "SELECT 1\nGO 2 3", []Batch{{"SELECT 1\nGO 2 3", 1}}},
		{"a count with CRLF", "SELECT 1\r\nGO 2\r\nSELECT 2", []Batch{{"SELECT 1", 2}, {"SELECT 2", 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Batches(tt.sql)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %+v, want %+v", got, tt.want)
				}
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
