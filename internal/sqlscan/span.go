package sqlscan

import "unicode"

// spanKind classifies a stretch of input that is not code.
type spanKind int

const (
	spanNone    spanKind = iota
	spanQuoted           // a literal or quoted identifier: its text is not code
	spanComment          // a comment: it contributes nothing at all
)

// skipSpan reports the extent of a literal, quoted identifier or comment
// beginning at i, returning the index just past it. It returns (i, spanNone)
// when ordinary code starts there.
//
// Every walk over SQL in this package and in the adapters needs the same
// answer to this one question, and the walk that answered it differently
// produced a real bug: a normaliser that did not skip literals rewrote the
// colons in a time format such as HH:MM:SS as parameter placeholders,
// unbalancing the quotes so the statement no longer parsed and was refused as
// unclassifiable. Answering it in one place is what stops the walks drifting
// apart again, and span_oracle_test.go checks the answers against PostgreSQL's
// own scanner.
func skipSpan(src []rune, i int, d Dialect) (int, spanKind) {
	if i >= len(src) {
		return i, spanNone
	}
	switch {
	case src[i] == '-' && i+1 < len(src) && src[i+1] == '-':
		j := i
		for j < len(src) && src[j] != '\n' {
			j++
		}
		return j, spanComment

	case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
		// Both dialects nest block comments, so tracking depth is what stops
		// an inner close ending the comment early and spilling its text into
		// the code around it.
		depth, j := 1, i+2
		for j < len(src) && depth > 0 {
			switch {
			case src[j] == '/' && j+1 < len(src) && src[j+1] == '*':
				depth++
				j += 2
			case src[j] == '*' && j+1 < len(src) && src[j+1] == '/':
				depth--
				j += 2
			default:
				j++
			}
		}
		return j, spanComment

	case src[i] == '\'':
		return skipQuoted(src, i, '\'', false), spanQuoted

	case stringPrefixAt(src, i, d):
		// A prefixed literal is one token to PostgreSQL, prefix included: an
		// E-prefixed string is a single SCONST starting at the E, not at the
		// quote. Starting the span at the quote would leave the prefix looking
		// like code, which is harmless today and exactly the sort of near-miss
		// that stops being harmless later.
		quote := i + 1
		if src[i+1] == '&' {
			quote = i + 2
		}
		escapes := src[i] == 'E' || src[i] == 'e'
		return skipQuoted(src, quote, '\'', escapes), spanQuoted

	case src[i] == '"':
		return skipQuoted(src, i, '"', false), spanQuoted

	case src[i] == '[' && d == DialectTSQL:
		return skipQuoted(src, i, ']', false), spanQuoted

	case src[i] == '$' && d == DialectPostgres:
		if end, ok := skipDollarQuoted(src, i); ok {
			return end, spanQuoted
		}
	}
	return i, spanNone
}

// skipQuoted returns the index just past a run whose opening delimiter sits at
// open. A doubled delimiter is an escaped one and does not end the run, which
// is how both dialects spell a quote inside a quoted thing. escapes
// additionally honours backslash escapes, which postgres applies only to an
// E-prefixed string.
func skipQuoted(src []rune, open int, close rune, escapes bool) int {
	for j := open + 1; j < len(src); j++ {
		if escapes && src[j] == '\\' {
			j++ // the escaped character cannot close the run
			continue
		}
		if src[j] == close {
			if j+1 < len(src) && src[j+1] == close {
				j++ // a doubled delimiter is one escaped character
				continue
			}
			return j + 1
		}
	}
	return len(src) // unterminated: consume the remainder rather than re-enter code
}

// stringPrefixAt reports whether a prefixed string literal starts at i. The
// prefixes differ by dialect: postgres has E (backslash escapes), B and X (bit
// and hex) and U& (unicode); T-SQL has N (national character).
//
// A prefix must not be the tail of an identifier, or a name ending in e
// followed by a literal would be read as one prefixed literal where PostgreSQL
// sees an identifier and then an ordinary string.
func stringPrefixAt(src []rune, i int, d Dialect) bool {
	if i > 0 && isIdentRune(src[i-1]) {
		return false
	}
	switch d {
	case DialectPostgres:
		switch src[i] {
		case 'E', 'e', 'B', 'b', 'X', 'x':
			return i+1 < len(src) && src[i+1] == '\''
		case 'U', 'u':
			return i+2 < len(src) && src[i+1] == '&' && src[i+2] == '\''
		}
	case DialectTSQL:
		if src[i] == 'N' || src[i] == 'n' {
			return i+1 < len(src) && src[i+1] == '\''
		}
	}
	return false
}

// skipDollarQuoted returns the index just past a postgres dollar-quoted body.
func skipDollarQuoted(src []rune, i int) (int, bool) {
	j := i + 1
	for j < len(src) && isIdentRune(src[j]) {
		j++
	}
	if j >= len(src) || src[j] != '$' {
		return 0, false
	}
	delim := src[i : j+1]
	for k := j + 1; k+len(delim) <= len(src); k++ {
		if matchAt(src, k, delim) {
			return k + len(delim), true
		}
	}
	return len(src), true // unterminated
}

func matchAt(src []rune, at int, want []rune) bool {
	for n, r := range want {
		if src[at+n] != r {
			return false
		}
	}
	return true
}

func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// SkipSpan is skipSpan for callers outside this package: it reports the index
// just past a literal, quoted identifier or comment starting at i, and whether
// one started there at all. Adapters use it so that their own scans skip
// exactly what the classifier skips.
func SkipSpan(src []rune, i int, d Dialect) (int, bool) {
	end, kind := skipSpan(src, i, d)
	return end, kind != spanNone
}
