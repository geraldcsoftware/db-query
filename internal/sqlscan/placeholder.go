package sqlscan

import (
	"strings"
	"unicode"
)

// NormalisePlaceholders replaces client-side parameter placeholders with inert
// literals so a submission can be classified.
//
// Placeholders are a client feature, not SQL: psql expands :'name' and sqlcmd
// expands $(name) before either sends anything, so neither PostgreSQL's
// grammar nor SQL Server's planner can parse one. Without this every
// parameterised query would fail to parse and classify opaque, which is to say
// --param would stop working entirely.
//
// Only the shape is being classified, so what a placeholder is replaced with
// does not matter as long as it parses and cannot itself change the shape. The
// bound values are covered by the digest and validated in the adapter; this
// text is used for classification and nothing else, and never for the digest.
func NormalisePlaceholders(sql string, d Dialect) string {
	if d == DialectTSQL {
		return normaliseTSQL(sql)
	}
	return normalisePostgres(sql)
}

func normalisePostgres(sql string) string {
	var b strings.Builder
	src := []rune(sql)
	for i := 0; i < len(src); i++ {
		if src[i] != ':' {
			b.WriteRune(src[i])
			continue
		}
		// A cast, not a placeholder.
		if i+1 < len(src) && src[i+1] == ':' {
			b.WriteString("::")
			i++
			continue
		}
		// :'name' and :"name" are the quoted forms; the bare :name form is
		// refused by the adapter before it runs, but is normalised here too so
		// that the refusal it gets is the specific one about quoting rather
		// than a bare "unparseable".
		if i+1 < len(src) && (src[i+1] == '\'' || src[i+1] == '"') {
			quote := src[i+1]
			j := i + 2
			for j < len(src) && src[j] != quote {
				j++
			}
			if quote == '\'' {
				b.WriteString("'0'")
			} else {
				b.WriteString(`"c0"`)
			}
			i = j
			continue
		}
		if j := identEnd(src, i+1); j > i+1 {
			b.WriteString("'0'")
			i = j - 1
			continue
		}
		b.WriteRune(':')
	}
	return b.String()
}

func normaliseTSQL(sql string) string {
	var b strings.Builder
	src := []rune(sql)
	for i := 0; i < len(src); i++ {
		if src[i] == '$' && i+1 < len(src) && src[i+1] == '(' {
			j := i + 2
			for j < len(src) && src[j] != ')' {
				j++
			}
			b.WriteString("'0'")
			i = j
			continue
		}
		b.WriteRune(src[i])
	}
	return b.String()
}

func identEnd(src []rune, i int) int {
	for i < len(src) && (src[i] == '_' || unicode.IsLetter(src[i]) || unicode.IsDigit(src[i])) {
		i++
	}
	return i
}
