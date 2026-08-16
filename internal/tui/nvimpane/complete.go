package nvimpane

// CandidateSource supplies the words offered for one completion context.
//
// qualifier is whatever preceded a dot, empty when there was none; prefix is
// the partial word being completed; buflines is the whole buffer, which is
// what lets a qualifier declared on one line be resolved from another.
//
// An implementation must be a pure in-memory lookup. Neovim's core is single
// threaded, so a slow reply freezes the whole pane rather than merely the
// popup, and the two-call protocol pays whatever it costs twice per keystroke.
type CandidateSource interface {
	Candidates(qualifier, prefix string, buflines []string) []map[string]any
}

// complete serves both calls of Neovim's complete-functions protocol. The first
// asks where the text being completed begins; the second asks what to offer.
//
// refresh = "always" makes Neovim call back on every keystroke rather than
// filtering its first answer itself, which is what lets the qualifier before
// the cursor change the candidate set mid-word.
func complete(src CandidateSource, findstart int, line string, col int, buflines []string) any {
	start, qualifier, prefix := parseContext(line, col)
	if findstart == 1 {
		return start
	}

	words := []map[string]any{}
	if src != nil {
		words = src.Candidates(qualifier, prefix, buflines)
	}
	return map[string]any{"words": words, "refresh": "always"}
}

// parseContext splits the text before the cursor into the qualifier before a
// dot (an alias or a table name, empty when there is none), the partial word
// after it, and the zero-based byte offset where the completed text begins.
func parseContext(line string, col int) (start int, qualifier, prefix string) {
	cut := col - 1 // col is 1-based; cut is the byte offset of the cursor
	if cut > len(line) {
		cut = len(line)
	}
	if cut < 0 {
		cut = 0
	}
	head := line[:cut]

	i := len(head)
	for i > 0 && isIdent(head[i-1]) {
		i--
	}
	prefix = head[i:]
	start = i

	// A dot immediately before the word turns whatever precedes it into a
	// qualifier, and the completion replaces only the part after the dot.
	if i > 0 && head[i-1] == '.' {
		j := i - 1
		for j > 0 && isIdent(head[j-1]) {
			j--
		}
		qualifier = head[j : i-1]
	}
	return start, qualifier, prefix
}

func isIdent(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
