// Package cli wires resolvers, adapters, executor, and renderers into
// the user-facing command surface.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/render"
	"github.com/geraldcsoftware/db-query/internal/savedquery"
	"github.com/geraldcsoftware/db-query/internal/schema"
)

const usage = `db-query — run SQL against configured hosts via native clients

Usage:
  db-query query      --host <name> [flags] [SQL]   run ad-hoc SQL (positional, -f file, stdin) or a saved query
  db-query queries    [flags]                       list saved queries
  db-query introspect --host <name> [flags]         list tables and columns, rebuild the schema cache
  db-query hosts      [flags]                       list configured hosts

Flags:
  --host <name>       host entry from the config file
  --config <path>     config file (default: $DB_QUERY_CONFIG or ~/.config/db-query/config.toml)
  --output text|json  output format (default text)
  --param k=v         bind a query parameter (repeatable); psql: :'k', sqlcmd: $(k)
  -f <path>           read SQL from file ("-" for stdin)
  --save <name>       save the query under this name after it runs successfully (query)
  --source <name>     run a previously saved query by name (query)
  --category <cat>    saved-query category for --save/--source (default "default")
  --force             with --save: overwrite an existing query and bypass the duplicate check
  --timeout <dur>     per-invocation deadline (default 30s)
  --refresh-schema    rebuild the cached schema first (query, introspect)
  --no-headers        text output only: omit the header line, tab-separate rows (query)

Saved queries live under $DB_QUERY_QUERIES_DIR (else $XDG_CONFIG_HOME/db-query/queries,
else ~/.config/db-query/queries) as <category>/<name>.sql, storing SQL with placeholders
only — never resolved --param values. A saved query is bound to the provider it was saved
against, so --source refuses to run it against a host of a different provider.

Exit codes:
  0  success
  1  config, usage, or credential error
  2  client binary could not start
  3  schema error — the query references an unknown table or column; re-run with --refresh-schema
  4  other SQL error — the client ran and exited nonzero
`

// Exit codes: 0 success; 1 config/usage/credential error; 2 client binary
// failed to start; 3 schema error (unknown table/column) — the
// reintrospect-worthy signal; 4 other SQL error (client ran, exited
// nonzero, but not a schema error).
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	switch args[0] {
	case "query":
		return runQuery(args[1:], stdout, stderr)
	case "queries":
		return runQueries(args[1:], stdout, stderr)
	case "introspect":
		return runIntrospect(args[1:], stdout, stderr)
	case "hosts":
		return runHosts(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "db-query: unknown command %q\n\n%s", args[0], usage)
		return 1
	}
}

type commonFlags struct {
	host    string
	config  string
	output  string
	timeout time.Duration
}

func addCommon(fs *flag.FlagSet, c *commonFlags) {
	fs.StringVar(&c.host, "host", "", "host entry from config")
	fs.StringVar(&c.config, "config", "", "config file path")
	fs.StringVar(&c.output, "output", "text", "output format: text|json")
	fs.DurationVar(&c.timeout, "timeout", 30*time.Second, "per-invocation deadline")
}

// paramFlags collects repeatable --param k=v values.
type paramFlags map[string]string

func (p paramFlags) String() string { return "" }

func (p paramFlags) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok || k == "" {
		return fmt.Errorf("--param wants k=v, got %q", kv)
	}
	p[k] = v
	return nil
}

// parseInterspersed parses fs while allowing flags to appear before or after
// the positional arguments. Go's flag package stops at the first non-flag
// token, so `query --host h "SQL" --param k=v` would otherwise swallow the
// SQL and everything after it as positionals and reject the trailing --param.
// Looping past each positional lets flags and SQL be given in any order; the
// collected positionals are returned. (A positional that itself begins with
// "-" is still ambiguous — pass such SQL via -f or stdin.)
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func runQuery(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	params := paramFlags{}
	var sqlFile, saveName, sourceName, category string
	var refreshSchema, noHeaders, force bool
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	fs.Var(params, "param", "bind a query parameter (repeatable)")
	fs.StringVar(&sqlFile, "f", "", `read SQL from file ("-" for stdin)`)
	fs.StringVar(&saveName, "save", "", "save the query under this name after it succeeds")
	fs.StringVar(&sourceName, "source", "", "run a saved query by name")
	fs.StringVar(&category, "category", savedquery.DefaultCategory, "saved-query category for --save/--source")
	fs.BoolVar(&refreshSchema, "refresh-schema", false, "rebuild the schema cache before running")
	fs.BoolVar(&noHeaders, "no-headers", false, "text output: omit the header line")
	fs.BoolVar(&force, "force", false, "with --save: overwrite an existing query and bypass the duplicate check")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return 1
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Flag-combination usage errors, checked before any secret is resolved.
	// A saved query is a complete unit: it is either sourced or authored, and
	// running one is not the moment to author another.
	if set["source"] && set["save"] {
		render.Error(stderr, c.output, "--source and --save are mutually exclusive; pick one")
		return 1
	}
	if set["source"] && (len(positional) > 0 || sqlFile != "") {
		render.Error(stderr, c.output, "--source runs a saved query; do not also pass SQL (a positional argument or -f)")
		return 1
	}
	if set["save"] && strings.TrimSpace(saveName) == "" {
		render.Error(stderr, c.output, "--save needs a non-empty query name")
		return 1
	}

	// Ad-hoc SQL is read up front; a --source query is loaded after setup so
	// the resolved host provider is known for the provider guard.
	var sql string
	if !set["source"] {
		var err error
		sql, err = readSQL(positional, sqlFile)
		if err != nil {
			render.Error(stderr, c.output, err.Error())
			return 1
		}
	}

	r, code := setup(c, stderr)
	if code != 0 {
		return code
	}

	if set["source"] {
		sq, err := savedquery.Load(sourceName, category)
		if err != nil {
			render.Error(stderr, c.output, sourceUnavailable(sourceName, category))
			return 1
		}
		// A saved query is provider-bound; running it against a host of a
		// different provider would send provider-specific SQL to the wrong
		// client. Refuse rather than let it fail obscurely mid-flight.
		if sq.Provider != r.host.Provider {
			render.Error(stderr, c.output, fmt.Sprintf(
				"saved query %s/%s is bound to provider %q, but host %q uses provider %q; refusing to run",
				sq.Category, sq.Name, sq.Provider, r.host.Name, r.host.Provider))
			return 1
		}
		sql = sq.SQL
	}

	// The schema cache is a silent side effect: build it the first time a
	// host+database is seen, or when --refresh-schema forces a rebuild,
	// before the user query runs. A build failure stops here — the user
	// query never runs against a schema we could not introspect.
	cachePath := schema.CachePath(r.host.Host, r.host.Database)
	if refreshSchema || !schema.Exists(cachePath) {
		if code := buildSchema(r, cachePath, c, stderr); code != 0 {
			return code
		}
	}

	rows, code := runOnce(r, adapter.Query{SQL: sql, Params: params}, c, true, stderr)
	if code != 0 {
		return code // a non-zero run saves nothing and returns its own code
	}
	if code := renderRows(rows, c.output, noHeaders, stdout, stderr); code != 0 {
		return code
	}

	// Save on success only: the query ran, exited 0, and its output has been
	// printed. The stored SQL carries placeholders, never the resolved --param
	// values. A refusal (a duplicate or an existing file, without --force) is a
	// usage error surfaced on stderr honouring --output — the run itself already
	// stands, so its output is not retracted.
	if set["save"] {
		sq, err := savedquery.Save(saveName, category, r.host.Provider, sql, force)
		if err != nil {
			render.Error(stderr, c.output, "saving query: "+err.Error())
			return 1
		}
		fmt.Fprintf(stderr, "db-query: saved as %s/%s\n", sq.Category, sq.Name)
	}
	return 0
}

// runQueries lists the saved queries in the store, optionally restricted to
// one --category. text prints a table (category, name, provider, short hash,
// SQL preview) through the shared render pivot; json emits the full records
// (category, name, provider, sqlhash, sql) so a caller can match against them.
func runQueries(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	var category string
	fs := flag.NewFlagSet("queries", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	fs.StringVar(&category, "category", "", "restrict to one saved-query category")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if _, err := render.For(c.output); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	list, err := savedquery.List(category)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if c.output == "json" {
		return renderQueriesJSON(list, c.output, stdout, stderr)
	}
	rows := adapter.Rows{Columns: []string{"category", "name", "provider", "hash", "sql"}}
	for _, q := range list {
		cat, name, prov := q.Category, q.Name, q.Provider
		hash := shortHash(q.SQLHash)
		preview := previewSQL(q.SQL)
		rows.Rows = append(rows.Rows, []*string{&cat, &name, &prov, &hash, &preview})
	}
	return renderRows(rows, c.output, false, stdout, stderr)
}

// queryListing is the JSON shape of one saved query in the queries command:
// exactly the fields a caller needs to match a request against the store.
type queryListing struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	SQLHash  string `json:"sqlhash"`
	SQL      string `json:"sql"`
}

// renderQueriesJSON emits the listing as a JSON array (empty as []), honouring
// --output on the error path.
func renderQueriesJSON(list []savedquery.SavedQuery, output string, stdout, stderr io.Writer) int {
	out := make([]queryListing, len(list))
	for i, q := range list {
		out[i] = queryListing{Category: q.Category, Name: q.Name, Provider: q.Provider, SQLHash: q.SQLHash, SQL: q.SQL}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		render.Error(stderr, output, err.Error())
		return 1
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

// sourceUnavailable builds the error shown when --source names a query the
// store does not hold, listing what is available so the caller can pick one.
func sourceUnavailable(name, category string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "saved query %q not found in category %q", name, category)
	all, err := savedquery.List("")
	if err != nil || len(all) == 0 {
		b.WriteString("; no saved queries available")
		return b.String()
	}
	refs := make([]string, len(all))
	for i, q := range all {
		refs[i] = q.Category + "/" + q.Name
	}
	fmt.Fprintf(&b, "; available: %s", strings.Join(refs, ", "))
	return b.String()
}

// shortHash trims a hash to its first 8 characters for compact listings.
func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// previewSQL collapses SQL to a single spaced line and truncates it for the
// text listing, so a multi-line query stays one table cell.
func previewSQL(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	const max = 60
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func runIntrospect(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	// --refresh-schema is accepted for symmetry with query; introspect
	// always rebuilds the cache regardless, so the flag is a no-op here.
	var refreshSchema bool
	fs := flag.NewFlagSet("introspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	fs.BoolVar(&refreshSchema, "refresh-schema", false, "rebuild the schema cache (introspect always rebuilds)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	r, code := setup(c, stderr)
	if code != 0 {
		return code
	}

	// introspect always rebuilds: run the provider catalog query, persist
	// it to the cache, then print it.
	rows, code := runOnce(r, adapter.Query{SQL: r.adapter.IntrospectSQL()}, c, false, stderr)
	if code != 0 {
		return code
	}
	if err := schema.Write(schema.CachePath(r.host.Host, r.host.Database), rows); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	return renderRows(rows, c.output, false, stdout, stderr)
}

func runHosts(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := flag.NewFlagSet("hosts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg, err := loadConfig(c.config)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	rows := adapter.Rows{Columns: []string{"host", "provider", "database"}}
	for _, name := range cfg.HostNames() {
		h := cfg.Hosts[name]
		n, p, d := name, h.Provider, h.Database
		rows.Rows = append(rows.Rows, []*string{&n, &p, &d})
	}
	return renderRows(rows, c.output, false, stdout, stderr)
}

func readSQL(positional []string, file string) (string, error) {
	switch {
	case len(positional) > 0 && file != "":
		return "", fmt.Errorf("give SQL either as an argument or via -f, not both")
	case len(positional) > 1:
		return "", fmt.Errorf("expected one SQL argument, got %d (quote the query)", len(positional))
	case len(positional) == 1:
		if strings.TrimSpace(positional[0]) == "" {
			return "", fmt.Errorf("SQL argument is empty")
		}
		return positional[0], nil
	case file == "-" || file == "":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading SQL from stdin: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return "", fmt.Errorf("no SQL given (argument, -f file, or stdin)")
		}
		return string(b), nil
	default:
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading SQL file: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return "", fmt.Errorf("SQL file %s is empty", file)
		}
		return string(b), nil
	}
}

func loadConfig(path string) (config.Config, error) {
	if path == "" {
		path = config.DefaultPath()
	}
	if path == "" {
		return config.Config{}, fmt.Errorf("cannot determine config path; set --config or DB_QUERY_CONFIG")
	}
	return config.Load(path)
}

// resolved bundles a host's adapter, its resolved credential, and the
// merged host config — the setup shared by the query and introspect paths.
type resolved struct {
	adapter adapter.Adapter
	cred    credential.Credential
	host    config.HostConfig // after MergeCredential; Host/Database are final
}

// setup validates the output format and host flag, loads config, selects
// the adapter, resolves the credential lazily (only this host's secret is
// touched), and merges it into the host config. On any failure it renders
// the error and returns exit code 1.
func setup(c commonFlags, stderr io.Writer) (resolved, int) {
	if _, err := render.For(c.output); err != nil {
		render.Error(stderr, c.output, err.Error())
		return resolved{}, 1
	}
	if c.host == "" {
		render.Error(stderr, c.output, "--host is required")
		return resolved{}, 1
	}
	cfg, err := loadConfig(c.config)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return resolved{}, 1
	}
	host, err := cfg.Host(c.host)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return resolved{}, 1
	}
	a, err := adapter.For(host.Provider)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return resolved{}, 1
	}
	cred, err := resolveCredential(host)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return resolved{}, 1
	}
	host = config.MergeCredential(host, cred)
	return resolved{adapter: a, cred: cred, host: host}, 0
}

// runOnce builds, runs, and parses a single invocation against a resolved
// host, applying the exit-code contract: 1 (build/parse failure), 2 (client
// could not start), 3 (schema error), 4 (other SQL error), 0 (rows valid).
// It renders any error itself. When hintRefresh is set a schema error also
// hints at --refresh-schema; the internal schema build passes false, since
// that hint only makes sense for the user's query.
func runOnce(r resolved, q adapter.Query, c commonFlags, hintRefresh bool, stderr io.Writer) (adapter.Rows, int) {
	inv, err := r.adapter.Build(r.host, q)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return adapter.Rows{}, 1
	}
	inv.Env = r.adapter.Env(r.cred, r.host)

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	res, err := executor.Run(ctx, inv)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return adapter.Rows{}, 2
	}
	if res.ExitCode != 0 {
		// go-sqlcmd prints login/connection errors to stdout even with
		// -r 1, so fall back to stdout when stderr is empty.
		detail := strings.TrimSpace(string(res.Stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(res.Stdout))
		}
		msg := fmt.Sprintf("%s exited %d: %s", inv.Argv[0], res.ExitCode, detail)
		if r.adapter.IsSchemaError(res) {
			if hintRefresh {
				msg += "\nhint: the schema may have changed — re-run with --refresh-schema to rebuild the schema cache"
			}
			render.Error(stderr, c.output, msg)
			return adapter.Rows{}, 3
		}
		render.Error(stderr, c.output, msg)
		return adapter.Rows{}, 4
	}
	rows, err := r.adapter.Parse(res)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return adapter.Rows{}, 1
	}
	return rows, 0
}

// buildSchema runs the provider introspection and persists the result to
// the schema cache. It is a silent side effect of the query path: the rows
// are cached, never rendered. It returns 0 on success or the runOnce exit
// code on failure.
func buildSchema(r resolved, cachePath string, c commonFlags, stderr io.Writer) int {
	rows, code := runOnce(r, adapter.Query{SQL: r.adapter.IntrospectSQL()}, c, false, stderr)
	if code != 0 {
		return code
	}
	if err := schema.Write(cachePath, rows); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	return 0
}

// renderRows writes rows through the single render pivot, honouring the
// output format and cross-format options. It returns 0 or exit code 1.
func renderRows(rows adapter.Rows, output string, noHeaders bool, stdout, stderr io.Writer) int {
	if err := render.Render(stdout, output, rows, render.Options{NoHeaders: noHeaders}); err != nil {
		render.Error(stderr, output, err.Error())
		return 1
	}
	return 0
}

// resolveCredential produces the final neutral record for a host:
// password from the credential URI; username from explicit host config
// (literal or URI) with the resolver's own username filling the gap.
func resolveCredential(host config.HostConfig) (credential.Credential, error) {
	var cred credential.Credential
	if host.Credential != "" {
		var err error
		cred, err = credential.Resolve(host.Credential)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("resolving credential for host %s: %w", host.Name, err)
		}
	}
	switch {
	case host.Username == "":
		// keep cred.Username (resolver-supplied, e.g. bw: or keychain:)
	case credential.IsURI(host.Username):
		u, err := credential.ResolveScalar(host.Username)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("resolving username for host %s: %w", host.Name, err)
		}
		cred.Username = u
	default:
		cred.Username = host.Username
	}
	return cred, nil
}
