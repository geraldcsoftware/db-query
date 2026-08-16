package tui

import (
	"strings"
	"unicode"
)

// tableRef is one table named in a FROM or JOIN clause, with whatever alias it
// was given. It is what turns `c` in `c.amount` back into a table whose columns
// can be offered.
type tableRef struct{ name, alias string }

// scopeOf finds the tables a query names, by scanning the whole buffer rather
// than the line under the cursor: the clause that introduces an alias routinely
// sits several lines above the place it is used.
//
// The scan is deliberately shallow. It reads FROM and JOIN clauses and nothing
// else, so a subquery's tables are in scope alongside the outer query's rather
// than being hidden from it. Offering a column that turns out to be out of
// scope costs a user one rejected statement; hiding one they meant to type
// costs them the feature.
func scopeOf(lines []string) []tableRef {
	toks := sqlTokens(lines)
	var out []tableRef
	for i := 0; i < len(toks); i++ {
		switch strings.ToUpper(toks[i]) {
		case "FROM", "JOIN":
			var refs []tableRef
			refs, i = tableList(toks, i+1)
			out = append(out, refs...)
		}
	}
	return out
}

// tableList reads the comma-separated table references starting at i and
// returns them with the index of the last token consumed.
func tableList(toks []string, i int) ([]tableRef, int) {
	var out []tableRef
	for i < len(toks) {
		if isReserved(toks[i]) || toks[i] == "," {
			break
		}
		ref := tableRef{name: toks[i]}
		i++

		// An alias follows either behind AS or on its own, and in both cases it
		// is a plain word — a clause keyword there means the list has ended.
		if i < len(toks) && strings.EqualFold(toks[i], "AS") && i+1 < len(toks) && !isReserved(toks[i+1]) {
			ref.alias = toks[i+1]
			i += 2
		} else if i < len(toks) && toks[i] != "," && !isReserved(toks[i]) {
			ref.alias = toks[i]
			i++
		}
		out = append(out, ref)

		if i < len(toks) && toks[i] == "," {
			i++
			continue
		}
		break
	}
	return out, i - 1
}

// sqlTokens splits a buffer into words and commas. Quotes and brackets are kept
// inside a word so `"public"."orders"` and `[dbo].[orders]` survive whole, to be
// unquoted when a name is compared; a dot is kept for the same reason. Anything
// after -- is dropped, so a commented-out clause does not put a table in scope.
// Block comments are not tracked, which at worst leaves a stale table offered.
func sqlTokens(lines []string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, line := range lines {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		for _, r := range line {
			switch {
			case r == '_' || r == '.' || r == '"' || r == '[' || r == ']' ||
				unicode.IsLetter(r) || unicode.IsDigit(r):
				cur.WriteRune(r)
			case r == ',':
				flush()
				out = append(out, ",")
			default:
				flush()
			}
		}
		flush()
	}
	return out
}

// reservedWords are the words that cannot be a table name or an alias, so
// meeting one ends a table list. Only words that can legitimately follow a
// table reference need to be here.
var reservedWords = map[string]bool{
	"AS": true, "ON": true, "USING": true, "WHERE": true, "GROUP": true,
	"ORDER": true, "HAVING": true, "LIMIT": true, "OFFSET": true, "FETCH": true,
	"UNION": true, "INTERSECT": true, "EXCEPT": true, "SELECT": true,
	"INSERT": true, "UPDATE": true, "DELETE": true, "SET": true, "VALUES": true,
	"RETURNING": true, "WITH": true, "AND": true, "OR": true, "NOT": true,
	"JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true,
	"CROSS": true, "OUTER": true, "NATURAL": true, "LATERAL": true, "FROM": true,
	"TOP": true, "INTO": true, "BY": true,
}

func isReserved(word string) bool { return reservedWords[strings.ToUpper(word)] }

// unquote strips the quoting a name may have been written with, so a reference
// matches the catalogue's plain name however it was spelled.
func unquote(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '[' || r == ']' || r == '`' {
			return -1
		}
		return r
	}, name)
}
