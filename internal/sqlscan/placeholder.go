package sqlscan

import (
	"sort"
	"strconv"
	"strings"
)

// NormalisePlaceholders replaces client-side parameter placeholders with inert
// literals so a submission can be classified.
//
// Only SQL Server has any. sqlcmd expands $(name) before it sends anything, so
// the planner cannot parse one, and without this every parameterised query
// would classify opaque. Postgres parameters are the server's own $1, $2,
// which its grammar parses as they stand, so a postgres submission is returned
// untouched.
//
// Only names that are actually bound are replaced, because that is exactly
// what sqlcmd does. Literals, comments and quoted identifiers are skipped.
//
// Only the shape is being classified, so what a placeholder is replaced with
// does not matter as long as it parses and cannot itself change the shape. The
// bound values are covered by the digest and validated in the adapter; this
// text is used for classification and nothing else, and never for the digest.
func NormalisePlaceholders(sql string, params map[string]string, d Dialect) string {
	if len(params) == 0 || d != DialectTSQL {
		return sql
	}
	return normaliseTSQL(sql, params)
}

// PositionalParams returns the distinct parameter numbers a postgres statement
// references as $1, $2 and so on, in ascending order.
//
// The server needs to be told the type of every parameter up to the highest one
// a statement names, and can infer it only for those the statement uses, so the
// adapter has to know which those are. Literals, quoted identifiers,
// dollar-quoted bodies and comments are skipped through the same helper the
// classifier uses. A dollar that continues an identifier, as in a$1, is part of
// the name and not a parameter. $0 and numbers too large to be a parameter are
// left for the server to refuse with its own message.
func PositionalParams(sql string) []int {
	seen := map[int]bool{}
	src := []rune(sql)
	for i := 0; i < len(src); {
		if end, kind := skipSpan(src, i, DialectPostgres); kind != spanNone {
			i = end
			continue
		}
		if src[i] != '$' || (i > 0 && (isIdentRune(src[i-1]) || src[i-1] == '$')) {
			i++
			continue
		}
		j := i + 1
		for j < len(src) && src[j] >= '0' && src[j] <= '9' {
			j++
		}
		if n, err := strconv.Atoi(string(src[i+1 : j])); err == nil && n > 0 && n <= maxParams {
			seen[n] = true
		}
		i = max(j, i+1)
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// maxParams is the most parameters one postgres statement can carry: the wire
// protocol counts them in a 16-bit field.
const maxParams = 65535

func normaliseTSQL(sql string, params map[string]string) string {
	var b strings.Builder
	src := []rune(sql)
	for i := 0; i < len(src); {
		if end, kind := skipSpan(src, i, DialectTSQL); kind != spanNone {
			b.WriteString(string(src[i:end]))
			i = end
			continue
		}
		if src[i] == '$' && i+1 < len(src) && src[i+1] == '(' {
			j := i + 2
			for j < len(src) && src[j] != ')' {
				j++
			}
			if j < len(src) {
				if _, bound := params[string(src[i+2:j])]; bound {
					b.WriteString("'0'")
					i = j + 1
					continue
				}
			}
		}
		b.WriteRune(src[i])
		i++
	}
	return b.String()
}
