package sqlscan

import "strings"

// NormalisePlaceholders replaces client-side parameter placeholders with inert
// literals so a submission can be classified.
//
// Placeholders are a client feature, not SQL: psql expands :'name' and sqlcmd
// expands $(name) before either sends anything, so neither PostgreSQL's
// grammar nor SQL Server's planner can parse one. Without this every
// parameterised query would fail to parse and classify opaque, which is to say
// --param would stop working entirely.
//
// Only names that are actually bound are replaced, because that is exactly
// what the clients do, and because a colon means several other things in SQL.
// Rewriting every `:word` breaks a cast's neighbour, an array slice such as
// `arr[1:3]`, and a time format such as `to_char(t, 'HH:MM:SS')`, each of which
// then fails to parse and is refused as unclassifiable. Literals, comments and
// quoted identifiers are skipped for the same reason.
//
// Only the shape is being classified, so what a placeholder is replaced with
// does not matter as long as it parses and cannot itself change the shape. The
// bound values are covered by the digest and validated in the adapter; this
// text is used for classification and nothing else, and never for the digest.
func NormalisePlaceholders(sql string, params map[string]string, d Dialect) string {
	if len(params) == 0 {
		return sql
	}
	if d == DialectTSQL {
		return normaliseTSQL(sql, params)
	}
	return normalisePostgres(sql, params)
}

func normalisePostgres(sql string, params map[string]string) string {
	var b strings.Builder
	src := []rune(sql)
	for i := 0; i < len(src); {
		if end, kind := skipSpan(src, i, DialectPostgres); kind != spanNone {
			b.WriteString(string(src[i:end]))
			i = end
			continue
		}
		if src[i] != ':' {
			b.WriteRune(src[i])
			i++
			continue
		}
		// A cast, which is two colons and no placeholder.
		if i+1 < len(src) && src[i+1] == ':' {
			b.WriteString("::")
			i += 2
			continue
		}
		if repl, end, ok := postgresPlaceholder(src, i, params); ok {
			b.WriteString(repl)
			i = end
			continue
		}
		b.WriteRune(':')
		i++
	}
	return b.String()
}

// postgresPlaceholder matches :name, :'name' and :"name" at i, but only for a
// name the caller actually bound. An unbound one is left alone: psql would not
// interpolate it either, and it is far more likely to be an array slice or a
// stray colon than a parameter.
func postgresPlaceholder(src []rune, i int, params map[string]string) (string, int, bool) {
	if i+1 >= len(src) {
		return "", 0, false
	}
	switch src[i+1] {
	case '\'', '"':
		quote := src[i+1]
		j := i + 2
		for j < len(src) && src[j] != quote {
			j++
		}
		if j >= len(src) {
			return "", 0, false // unterminated: not a placeholder
		}
		if _, bound := params[string(src[i+2:j])]; !bound {
			return "", 0, false
		}
		if quote == '\'' {
			return "'0'", j + 1, true
		}
		return `"c0"`, j + 1, true
	default:
		j := identEnd(src, i+1)
		if j == i+1 {
			return "", 0, false
		}
		if _, bound := params[string(src[i+1:j])]; !bound {
			return "", 0, false
		}
		// The bare form is refused by the adapter before it runs. It is
		// normalised here so that the refusal reaching the user is the
		// specific one about quoting rather than "not parseable".
		return "'0'", j, true
	}
}

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

func identEnd(src []rune, i int) int {
	for i < len(src) && isIdentRune(src[i]) {
		i++
	}
	return i
}
