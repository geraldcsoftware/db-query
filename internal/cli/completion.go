package cli

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/dblist"
	"github.com/geraldcsoftware/db-query/internal/savedquery"
)

//go:embed completion.zsh
var zshCompletionScript string

// runCompletion prints the shell completion script. Only zsh is supported; an
// unsupported or missing shell is a usage error (exit 1), which also leaves
// room to add other shells later without changing today's contract.
func runCompletion(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "db-query: completion needs a shell argument (only 'zsh' is supported)")
		return 1
	}
	switch args[0] {
	case "zsh":
		fmt.Fprint(stdout, zshCompletionScript)
		return 0
	default:
		fmt.Fprintf(stderr, "db-query: unsupported shell %q (only 'zsh' is supported)\n", args[0])
		return 1
	}
}

// runComplete is the hidden dynamic-completion helper the zsh script shells
// into on every TAB. It prints tab-delimited "name<TAB>description" candidate
// lines for the requested target — except the database target, whose
// candidates are bare names carrying no description and so no tab — reading
// only local files. It never resolves a credential or opens a database, and on
// any error it prints nothing and returns 0 — a completion callback must never
// emit noise into the prompt or a non-zero status. stderr is accepted for
// signature symmetry and never used.
func runComplete(args []string, stdout, stderr io.Writer) int {
	var target, cfgPath, category, host string
	// Order-independent scan: the zsh script passes flags around the target,
	// but keep parsing forgiving so the helper is also pleasant to invoke by
	// hand when debugging.
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 < len(args) {
				i++
				cfgPath = args[i]
			}
		case "--category":
			if i+1 < len(args) {
				i++
				category = args[i]
			}
		case "--host", "-H":
			if i+1 < len(args) {
				i++
				host = args[i]
			}
		default:
			if target == "" && !strings.HasPrefix(args[i], "-") {
				target = args[i]
			}
		}
	}
	// An exported host stands in for one not yet typed on the line; the helper
	// is a subprocess, so it inherits it for free. The flag wins, matching the
	// CLI's own flag-then-environment precedence.
	if host == "" {
		host = os.Getenv("DB_QUERY_HOST")
	}
	switch target {
	case "host":
		completeHosts(cfgPath, stdout)
	case "source":
		completeSources(category, stdout)
	case "category":
		completeCategories(stdout)
	case "database":
		completeDatabases(host, stdout)
	}
	return 0
}

// completeDatabases prints one cached database name per line for the given
// host, with no description — so the candidate line carries no tab. The names
// come from the cache `db-query <host> databases` wrote; nothing here connects,
// resolves a credential, or even reads the config file. A host that has never
// been listed, or a cache that will not parse, yields no output at all: zsh
// then completes nothing rather than falling back to filenames.
func completeDatabases(host string, stdout io.Writer) {
	if host == "" {
		return
	}
	names, err := dblist.Read(dblist.CachePath(host))
	if err != nil {
		return
	}
	for _, n := range names {
		fmt.Fprintln(stdout, n)
	}
}

// completeHosts prints each configured host as "name<TAB>provider · database"
// (the database is omitted when unset). Config comes from --config, else
// $DB_QUERY_CONFIG / the default path. Any load error yields no output.
func completeHosts(cfgPath string, stdout io.Writer) {
	path := cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	if path == "" {
		return
	}
	cfg, err := config.Load(path)
	if err != nil {
		return
	}
	for _, name := range cfg.HostNames() {
		h := cfg.Hosts[name]
		desc := h.Provider
		if h.Database != "" {
			desc = h.Provider + " · " + h.Database
		}
		fmt.Fprintf(stdout, "%s\t%s\n", name, desc)
	}
}

// completeSources prints each saved query as "name<TAB>category · preview".
// A non-empty category restricts the listing. Any store error yields no output.
func completeSources(category string, stdout io.Writer) {
	list, err := savedquery.List(category)
	if err != nil {
		return
	}
	for _, q := range list {
		fmt.Fprintf(stdout, "%s\t%s · %s\n", q.Name, q.Category, previewSQL(q.SQL))
	}
}

// completeCategories prints each distinct saved-query category as
// "category<TAB>N quer(y|ies)", in the store's sorted category order.
func completeCategories(stdout io.Writer) {
	list, err := savedquery.List("")
	if err != nil {
		return
	}
	counts := map[string]int{}
	var order []string
	for _, q := range list {
		if _, seen := counts[q.Category]; !seen {
			order = append(order, q.Category)
		}
		counts[q.Category]++
	}
	for _, cat := range order {
		unit := "queries"
		if counts[cat] == 1 {
			unit = "query"
		}
		fmt.Fprintf(stdout, "%s\t%d %s\n", cat, counts[cat], unit)
	}
}
