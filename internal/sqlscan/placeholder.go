package sqlscan

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

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

// NamedParams returns the distinct names a T-SQL batch references as @name, in
// the order they first appear and spelt as they first appear.
//
// SQL Server binds parameters server-side through sp_executesql, which has to
// be told the name of each one, so the adapter has to know which a batch uses.
// A reference is not necessarily a parameter: @name is also how T-SQL spells a
// local variable, so the caller binds the names it was given values for and
// leaves the rest to the batch's own DECLARE. System functions such as
// @@ROWCOUNT are not references. Literals, quoted identifiers and comments are
// skipped through the same helper the classifier uses, and an @ that continues
// an identifier is part of that identifier.
func NamedParams(sql string) []string {
	var out []string
	seen := map[string]bool{}
	src := []rune(sql)
	for i := 0; i < len(src); {
		if end, kind := skipSpan(src, i, DialectTSQL); kind != spanNone {
			i = end
			continue
		}
		if src[i] != '@' || (i > 0 && isTSQLNameRune(src[i-1])) {
			i++
			continue
		}
		if i+1 < len(src) && src[i+1] == '@' {
			// @@ROWCOUNT and friends: skip the whole name, so its tail is
			// not read as a reference of its own.
			j := i + 2
			for j < len(src) && isTSQLNameRune(src[j]) {
				j++
			}
			i = j
			continue
		}
		j := i + 1
		for j < len(src) && isTSQLNameRune(src[j]) {
			j++
		}
		if name := string(src[i+1 : j]); name != "" && !seen[strings.ToLower(name)] {
			// Variable names compare case-insensitively under the default
			// collations, so @Id and @id are one reference.
			seen[strings.ToLower(name)] = true
			out = append(out, name)
		}
		i = max(j, i+1)
	}
	return out
}

// isTSQLNameRune reports whether r can continue a T-SQL regular identifier,
// which admits @, # and $ after the first character as well as the usual set.
func isTSQLNameRune(r rune) bool {
	return isIdentRune(r) || r == '@' || r == '#' || r == '$'
}

// Batch is one GO-separated batch of a T-SQL submission, with the number of
// times the separator asked for it to run.
type Batch struct {
	SQL   string
	Count int
}

// Batches splits a T-SQL submission at its GO separators, the unit SQL Server
// compiles and runs at once. Unlike Scan it keeps everything else verbatim,
// semicolons and comments included: a batch's statements share its variables,
// so they must reach the server together and as written.
//
// GO takes an optional repeat count, as sqlcmd allowed. A count of zero is
// read as one, the least surprising way to treat a request to run nothing.
// Client directives are not recognised here; callers refuse them through Scan
// first.
func Batches(sql string) []Batch {
	var out []Batch
	var cur strings.Builder
	flush := func(count int) {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, Batch{SQL: s, Count: max(count, 1)})
		}
		cur.Reset()
	}
	atLineStart := true
	src := []rune(sql)
	for i := 0; i < len(src); {
		if end, kind := skipSpan(src, i, DialectTSQL); kind != spanNone {
			cur.WriteString(string(src[i:end]))
			// A line comment ends at its newline without consuming it, so
			// the next line still starts fresh.
			atLineStart = false
			i = end
			continue
		}
		c := src[i]
		if atLineStart && isGoLine(src, i) {
			j := i + 2
			for j < len(src) && src[j] != '\n' {
				j++
			}
			count, _ := strconv.Atoi(strings.TrimSpace(string(src[i+2 : j])))
			flush(count)
			i = j
			continue
		}
		cur.WriteRune(c)
		if c == '\n' {
			atLineStart = true
		} else if !unicode.IsSpace(c) {
			atLineStart = false
		}
		i++
	}
	flush(1)
	return out
}
