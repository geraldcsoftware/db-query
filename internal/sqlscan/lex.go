package sqlscan

import (
	"strings"
	"unicode"
)

// Dialect selects the quoting and batching rules a scan applies. It is not a
// grammar: the scanner understands literals, comments and separators, and
// nothing else. Understanding statements is §13.13's mechanism, not this.
type Dialect int

const (
	DialectPostgres Dialect = iota
	DialectTSQL
)

// Scan splits a submission into statements and reports any client directive it
// meets. It is the pre-pass §13.13 runs first for both providers, before any
// parser or planner: a directive must be refusable on a host whose database is
// unreachable, and the planner mechanism needs one statement at a time.
//
// Comments and separators are dropped; the statement text is otherwise intact,
// because the digest binds the resolved SQL and a scan that rewrote it would
// bind something the engine never sees.
func Scan(sql string, d Dialect) (statements []string, directives []string) {
	var cur strings.Builder
	// atLineStart tracks whether only whitespace has been seen since the last
	// newline. A directive is a line-initial marker, so this is what
	// distinguishes `\i foo` from a backslash inside an expression.
	atLineStart := true

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			statements = append(statements, s)
		}
		cur.Reset()
	}

	src := []rune(sql)
	for i := 0; i < len(src); {
		// Literals, quoted identifiers and comments first, through the same
		// helper every other walk uses, so none of them can disagree about
		// where code stops and text begins.
		if end, kind := skipSpan(src, i, d); kind != spanNone {
			if kind == spanQuoted {
				cur.WriteString(string(src[i:end]))
				atLineStart = false
			} else {
				// A comment contributes nothing but must not join the tokens
				// on either side of it.
				cur.WriteRune(' ')
				if end < len(src) && src[end] == '\n' {
					atLineStart = true
				}
			}
			i = end
			continue
		}

		c := src[i]

		// A directive claims the rest of its line, and never reaches a
		// statement: it is refused, not executed.
		if atLineStart && isDirectiveMarker(c, d) {
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			directives = append(directives, strings.TrimSpace(string(src[i:j])))
			i = j
			atLineStart = true
			continue
		}

		switch {
		case c == ';':
			flush()
			atLineStart = false
			i++
			continue
		case c == '\n':
			cur.WriteRune('\n')
			atLineStart = true
			i++
			continue
		}

		// A T-SQL batch separator ends a statement the way a semicolon does.
		if d == DialectTSQL && atLineStart && isGoLine(src, i) {
			flush()
			for i < len(src) && src[i] != '\n' {
				i++
			}
			atLineStart = true
			continue
		}

		cur.WriteRune(c)
		if !unicode.IsSpace(c) {
			atLineStart = false
		}
		i++
	}
	flush()
	return statements, directives
}

// isDirectiveMarker reports whether c begins a client directive for d. These
// are executed by the client and never reach the server, so neither a parser
// nor a planner can see them: they have to be caught here or not at all.
func isDirectiveMarker(c rune, d Dialect) bool {
	switch d {
	case DialectPostgres:
		return c == '\\'
	case DialectTSQL:
		// sqlcmd spells its shell-out `:!!` in current versions and `!!`
		// in older ones, so both markers are refused.
		return c == ':' || c == '!'
	}
	return false
}

// isGoLine reports whether a T-SQL batch separator starts at i. sqlcmd accepts
// `GO` alone on a line, optionally with a repeat count.
func isGoLine(src []rune, i int) bool {
	j := i
	if j+1 >= len(src) {
		return false
	}
	if !(src[j] == 'G' || src[j] == 'g') || !(src[j+1] == 'O' || src[j+1] == 'o') {
		return false
	}
	for k := j + 2; k < len(src) && src[k] != '\n'; k++ {
		if !unicode.IsSpace(src[k]) && !unicode.IsDigit(src[k]) {
			return false
		}
	}
	return true
}

// String names a dialect, for the mechanism field of a verdict reached by the
// pre-pass rather than by a parser or a planner.
func (d Dialect) String() string {
	if d == DialectTSQL {
		return "tsql-prepass"
	}
	return "postgres-prepass"
}
