package cli

import (
	"fmt"
	"unicode"
)

// commandNames lists the full command names a mistyped word is compared
// against. Aliases are left out: a one or two letter alias is within distance 2
// of too many unrelated words to make a useful suggestion.
var commandNames = []string{
	"query", "list", "schema", "introspect", "databases", "hosts", "version", "completion", "help",
}

// maxSuggestionDistance is the largest edit distance at which a word is still
// offered a "did you mean" suggestion.
const maxSuggestionDistance = 2

// looksLikeSQL reports whether arg contains whitespace. A command name never
// does, so a quoted argument with a space is SQL, while a single bare word stays
// a command name and a mistyped one fails instead of reaching the server.
func looksLikeSQL(arg string) bool {
	for _, r := range arg {
		if unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// suggestCommand returns the full command name closest to word, provided it is
// within maxSuggestionDistance edits. The earlier command wins a tie.
func suggestCommand(word string) (string, bool) {
	best, bestDist := "", maxSuggestionDistance+1
	for _, name := range commandNames {
		if d := editDistance(word, name); d < bestDist {
			best, bestDist = name, d
		}
	}
	return best, bestDist <= maxSuggestionDistance
}

// editDistance is the Levenshtein distance between a and b, counted in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// unknownCommandMessage explains an unrecognised first word. A close command
// name is suggested in both modes; otherwise the hint depends on whether SQL
// can be run without the query command, which only a terminal allows.
func unknownCommandMessage(word string, interactive bool) string {
	if s, ok := suggestCommand(word); ok {
		return fmt.Sprintf("db-query: unknown command %q (did you mean %q?)", word, s)
	}
	if interactive {
		return fmt.Sprintf("db-query: unknown command %q; to run SQL without the query command, pass it as one quoted argument", word)
	}
	return fmt.Sprintf("db-query: unknown command %q; outside a terminal, run SQL through the query command: db-query query \"<SQL>\"", word)
}
