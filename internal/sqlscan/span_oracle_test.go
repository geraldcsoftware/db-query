//go:build cgo

package sqlscan

import (
	"testing"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// oracleCorpus is deliberately awkward. Each entry is a shape where deciding
// what is code and what is text is easy to get wrong by hand.
var oracleCorpus = []string{
	"SELECT 1",
	"select to_char(created, 'DD Mon, HH:MM:SS') from t",
	"SELECT 'it''s' AS s",
	`SELECT E'a\'b' AS s`,
	`SELECT e'tab\there' AS s`,
	"SELECT $$ a ; b $$ AS s",
	"SELECT $tag$ a ; b $tag$ AS s",
	"SELECT $tag$ $$ nested-looking $$ $tag$ AS s",
	`SELECT "quoted ident" FROM t`,
	`SELECT "quo""ted" FROM t`,
	"SELECT 1 -- trailing comment",
	"SELECT /* block */ 1",
	"SELECT /* outer /* inner */ still */ 1",
	"SELECT 1; SELECT 2",
	"SELECT arr[1:3] FROM t",
	"SELECT a::text FROM t",
	"SELECT * FROM t WHERE url = 'https://x/y?a=b#c'",
	"SELECT '' AS empty",
	"SELECT '--not a comment' AS s",
	"SELECT '/* not a comment */' AS s",
	"SELECT $$ -- not a comment $$ AS s",
	"SELECT U&'d\\0061t\\0061' AS s",
	"SELECT B'1010' AS b, X'1FF' AS x",
	"SELECT 'multi\nline' AS s",
	"SELECT * FROM t /* c1 */ /* c2 */ WHERE a = 'x'",
}

// TestSkipSpanAgreesWithPostgresLexer is a differential test against
// PostgreSQL's own scanner.
//
// skipSpan has to stay for the T-SQL dialect and for builds without cgo, so it
// cannot simply be replaced by the real lexer. What it can do is be checked
// against it: every stretch this package calls a literal or a quoted
// identifier must be exactly one token to PostgreSQL, and every stretch it
// calls a comment must be invisible to PostgreSQL. Divergence between a
// hand-rolled walk and the real grammar is what produced the placeholder bug,
// so it is worth failing a build over.
func TestSkipSpanAgreesWithPostgresLexer(t *testing.T) {
	for _, sql := range oracleCorpus {
		t.Run(sql, func(t *testing.T) {
			res, err := pg.Scan(sql)
			if err != nil {
				t.Skipf("postgres will not scan this, so there is nothing to compare: %v", err)
			}
			// Byte ranges PostgreSQL considers a single token.
			tokenAt := map[int32]int32{} // start -> end
			for _, tk := range res.Tokens {
				tokenAt[tk.Start] = tk.End
			}
			covered := func(b, e int) bool { return tokenAt[int32(b)] == int32(e) }

			for _, s := range spansOf(sql, DialectPostgres) {
				switch s.kind {
				case spanQuoted:
					if !covered(s.start, s.end) {
						t.Errorf("we call %q a literal or quoted identifier, PostgreSQL does not tokenise it as one",
							sql[s.start:s.end])
					}
				case spanComment:
					// PostgreSQL's scanner emits comments as tokens
					// (SQL_COMMENT, C_COMMENT) rather than skipping them, so a
					// comment is checked for exact coverage just like a
					// literal.
					if !covered(s.start, s.end) {
						t.Errorf("we call %q a comment, PostgreSQL does not tokenise it as one", sql[s.start:s.end])
					}
				}
			}
		})
	}
}

type byteSpan struct {
	start, end int
	kind       spanKind
}

// spansOf walks sql with skipSpan and reports the spans in byte offsets, which
// is what the lexer reports in.
func spansOf(sql string, d Dialect) []byteSpan {
	src := []rune(sql)
	// runeToByte[i] is the byte offset of rune i; the final entry is len(sql).
	runeToByte := make([]int, len(src)+1)
	b := 0
	for i, r := range src {
		runeToByte[i] = b
		b += len(string(r))
	}
	runeToByte[len(src)] = len(sql)

	var out []byteSpan
	for i := 0; i < len(src); {
		if end, kind := skipSpan(src, i, d); kind != spanNone {
			out = append(out, byteSpan{runeToByte[i], runeToByte[end], kind})
			i = end
			continue
		}
		i++
	}
	return out
}
