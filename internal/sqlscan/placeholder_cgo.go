//go:build cgo

package sqlscan

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// normalisePostgres replaces bound psql placeholders using PostgreSQL's own
// scanner to decide what is and is not a placeholder.
//
// A colon means several things, and telling them apart by hand is what went
// wrong before: a cast, an array slice and a time format inside a literal were
// all rewritten as parameters, producing SQL that no longer parsed. The
// scanner makes those distinctions structural rather than a matter of rules.
// A cast is a single TYPECAST token, so it is never two colons. A literal is a
// single SCONST, so the colons inside it are not visible here at all. A slice
// bound is separated from its colon by the scanner's own offsets.
//
// What the scanner cannot do is recognise the placeholder itself: :'name' is a
// psql feature and this is the server's lexer, so it sees a bare colon
// followed by a string. That last step stays ours, and it is a small one.
func normalisePostgres(sql string, params map[string]string) string {
	res, err := pg.Scan(sql)
	if err != nil {
		// Anything the scanner rejects the parser will reject too, so the
		// submission classifies opaque and is refused. Returning it untouched
		// keeps that path honest rather than handing the parser something this
		// function invented.
		return sql
	}
	toks := res.Tokens
	var b strings.Builder
	last := 0
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Token != pg.Token_ASCII_58 {
			continue
		}
		next := toks[i+1]
		// psql interpolates only when the name abuts the colon, which is also
		// what separates :name from the colon in arr[1 : 3].
		if next.Start != toks[i].End {
			continue
		}
		name, repl, ok := placeholderAt(sql, next)
		if !ok {
			continue
		}
		if _, bound := params[name]; !bound {
			continue
		}
		b.WriteString(sql[last:toks[i].Start])
		b.WriteString(repl)
		last = int(next.End)
	}
	b.WriteString(sql[last:])
	return b.String()
}

// placeholderAt reads the name a colon-prefixed token carries, and what to
// substitute for the pair. The replacement only has to parse and not change
// the shape of the statement, since only the shape is being classified.
func placeholderAt(sql string, tok *pg.ScanToken) (name, repl string, ok bool) {
	text := sql[tok.Start:tok.End]
	switch tok.Token {
	case pg.Token_SCONST:
		// :'name' — psql quotes and escapes the value, so a string literal
		// stands in for it.
		return strings.Trim(text, "'"), "'0'", true
	case pg.Token_IDENT:
		if strings.HasPrefix(text, `"`) {
			// :"name" — psql substitutes a quoted identifier.
			return strings.Trim(text, `"`), `"c0"`, true
		}
		// :name — the unquoted form, which the adapter refuses before the
		// query runs. It is normalised here so the refusal reaching the user
		// is the specific one about quoting rather than "not parseable".
		return text, "'0'", true
	}
	return "", "", false
}
