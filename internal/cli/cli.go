// Package cli wires resolvers, adapters, executor, and renderers into
// the user-facing command surface.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/render"
	"github.com/geraldcsoftware/db-query/internal/savedquery"
	"github.com/geraldcsoftware/db-query/internal/schema"
	"github.com/geraldcsoftware/db-query/internal/session"
	"github.com/geraldcsoftware/db-query/internal/tui"
)

const usage = `db-query — run SQL against configured hosts via native clients

Usage:
  db-query [shared flags] <command> [flags] [args]
  db-query [shared flags]                          on a terminal, opens the interactive TUI

Commands:
  query      (q)     --host <name> [flags] [SQL]   run ad-hoc SQL (positional, -f file, stdin) or a saved query
  list       (ls, l) [flags]                       list saved queries
  schema     (s)     --host <name> [flags] [table] show the cached schema; a table name restricts to that table
  introspect (i)     --host <name> [flags]         list tables and columns, rebuild the schema cache
  databases          --host <name> [flags]         list databases on the host, caching the names for
                                                   --database completion
  hosts              [flags] [name]                list configured hosts; a name shows that host's
                                                   effective config and where each key came from
  version                                          print version information
  completion zsh                                   print the zsh completion script (see README to install)

Flags:
  --host (-H) <name>      : host entry from the config file
  --database (-d) <db>    : override the host's configured database (query, schema, introspect, databases)
  --config (-c) <path>    : config file (default: $DB_QUERY_CONFIG or ~/.config/db-query/config.toml)
  --output (-o) <format>  : json|table|text|auto (default auto)
  --param (-p) k=v        : bind a query parameter (repeatable); psql: :'k', sqlcmd: $(k)
  --file (-f) <path>      : read SQL from file ("-" for stdin)
  --source (-s) <name>    : run a previously saved query by name (query)
  --save <name>           : save the query under this name after it runs successfully (query)
  --category (-C) <cat>   : saved-query category for --save/--source (default "default")
  --force                 : with --save: overwrite an existing query and bypass the duplicate check
  --timeout (-t) <dur>    : per-invocation deadline (default 30s)
  --refresh-schema        : rebuild the cached schema first (query, schema, introspect)
  --no-headers            : omit the header line; text tab-separates rows, table drops the
                            row-count footer (query, schema)
  --max-col-width <n>     : table output: truncate cells wider than n cells (default 50,
                            0 = unlimited) (query, schema)
  --border <style>        : table output: ascii|light|markdown|none (default ascii)
                            (query, schema)
  --tables (-T)           : schema: print one schema-qualified table name per line instead of columns
  --help (-h)             : show this help (works on any command)
  --version (-v)          : print version information

Output formats. auto — the default — renders a bordered table when stdout is a
terminal and tab-separated text otherwise, so results are readable by eye while
a pipe, a redirect, or a program calling db-query still gets the stable
machine-readable shape. Pass --output explicitly to force either one; export
DB_QUERY_OUTPUT to pin it for a whole shell. Every command that prints rows —
query, list, schema, introspect, hosts — honours the same setting.

--border picks the table frame: ascii (portable, the default), light (Unicode
box-drawing), markdown (paste into an issue or notes file), or none (aligned
columns, no frame). markdown omits the row-count footer so the output pastes
verbatim; combined with --no-headers it emits data rows without the --- rule,
which appends to an existing table rather than standing alone.

The shared flags (--host, --database, --config, --output, --timeout) may also be
given before the command, which keeps the part that changes between runs at the
end of the line — so the previous command can be edited instead of retyped:

  db-query --host test --database testdb schema
  db-query --host test --database testdb query "select * from todos;"

The same flag given after the command wins over one given before it. Only these
five are accepted before the command; a command's own flags belong after it.

Environment:
  DB_QUERY_HOST          default for --host
  DB_QUERY_DATABASE      default for --database
  DB_QUERY_OUTPUT        default for --output
  DB_QUERY_CONFIG        default config file path
  DB_QUERY_QUERIES_DIR   saved-query store directory
  DB_QUERY_TUI_PAGE_SIZE rows per page in the interactive mode's Results pane (default 100)

A flag beats the environment, and the environment beats the config file, so an
exported DB_QUERY_HOST/DB_QUERY_DATABASE pair makes every command in that shell
short while a one-off --host still overrides it.

Saved queries live under $DB_QUERY_QUERIES_DIR (else $XDG_CONFIG_HOME/db-query/queries,
else ~/.config/db-query/queries) as <category>/<name>.sql, storing SQL with placeholders
only — never resolved --param values. A saved query is bound to the provider it was saved
against, so --source refuses to run it against a host of a different provider.

Shared host configuration. A [profiles.<name>] section holds keys that several
hosts have in common; a host picks them up with inherit = "<name>". Profiles may
inherit profiles, so a base profile can carry the provider while narrower ones
carry per-group credentials. Precedence runs explicit host key, then the nearest
profile in the chain, then anything the resolved credential supplies. A profile
is not connectable — it only feeds hosts. Run 'db-query hosts <name>' to see a
host's merged result and which section each key came from.

  [profiles.pg]
  provider   = "postgres"
  database   = "postgres"

  [profiles.eus]
  inherit    = "pg"
  username   = "gchifanzwa"
  credential = "bws:<secret-id>"

  [hosts.lionel]
  inherit    = "eus"
  host       = "lionel.internal"

Exit codes:
  0  success
  1  config, usage, or credential error
  2  client binary could not start
  3  schema error — the query references an unknown table or column; re-run with --refresh-schema
  4  other SQL error — the client ran and exited nonzero
`

// BuildInfo carries release metadata reported by the `version` command and
// the `--version` flag. It is populated from main via SetBuildInfo with values
// injected at build time through -ldflags.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

var buildInfo = BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}

// SetBuildInfo records the build metadata reported by the version command.
func SetBuildInfo(bi BuildInfo) { buildInfo = bi }

func versionString() string {
	return fmt.Sprintf("db-query %s (commit %s, built %s)", buildInfo.Version, buildInfo.Commit, buildInfo.Date)
}

// commandAliases maps each shorthand to the command it stands for. The set is
// an explicit map rather than a prefix match: a prefix would make `l` ambiguous
// the moment a second l-command lands, and a new command could silently steal
// an established shorthand.
var commandAliases = map[string]string{
	"q":  "query",
	"s":  "schema",
	"i":  "introspect",
	"ls": "list",
	"l":  "list",
}

// canonicalCommand resolves a command alias to the full command name, leaving
// anything else untouched so an unknown command is still reported as typed.
func canonicalCommand(name string) string {
	if full, ok := commandAliases[name]; ok {
		return full
	}
	return name
}

// Exit codes: 0 success; 1 config/usage/credential error; 2 client binary
// failed to start; 3 schema error (unknown table/column) — the
// reintrospect-worthy signal; 4 other SQL error (client ran, exited
// nonzero, but not a schema error).
func Run(args []string, stdout, stderr io.Writer) int {
	// No arguments at all is not a special case: it parses to no shared flags
	// and no command, which the len(rest) == 0 branch below already handles —
	// opening the TUI on a terminal and printing usage anywhere else. Returning
	// early here instead would make the bare `db-query` the one invocation that
	// could never reach the TUI.
	//
	// The shared flags are parsed off the front of the command line before the
	// command is chosen. Go's flag package stops at the first non-flag token,
	// which is exactly the command name, so `--host h schema --tables` leaves
	// ["schema", "--tables"] for the subcommand. Only the shared flags are
	// registered here: a subcommand's own flag before the command is still an
	// error, rather than quietly becoming global for every command.
	var globals commonFlags
	var showVersion bool
	fs := newFlagSet("db-query", stderr)
	addCommon(fs, &globals, envDefaults())
	fs.BoolVar(&showVersion, "version", false, "print version information")
	fs.BoolVar(&showVersion, "v", false, "shorthand for --version")
	if err := fs.Parse(args); err != nil {
		return exitParse(err, stdout)
	}
	if showVersion {
		fmt.Fprintln(stdout, versionString())
		return 0
	}
	rest := fs.Args()
	if len(rest) == 0 {
		if isTerminal(stdout) {
			return launchTUI(toSessionFlags(globals), buildInfo.Version, stdout, stderr)
		}
		// Shared flags but no command, and not a terminal: nothing to run.
		fmt.Fprint(stderr, usage)
		return 1
	}
	cmdArgs := rest[1:]
	switch canonicalCommand(rest[0]) {
	case "query":
		return runQuery(cmdArgs, globals, stdout, stderr)
	case "list":
		return runList(cmdArgs, globals, stdout, stderr)
	case "schema":
		return runSchema(cmdArgs, globals, stdout, stderr)
	case "introspect":
		return runIntrospect(cmdArgs, globals, stdout, stderr)
	case "databases":
		return runDatabases(cmdArgs, globals, stdout, stderr)
	case "hosts":
		return runHosts(cmdArgs, globals, stdout, stderr)
	case "__complete":
		return runComplete(cmdArgs, stdout, stderr)
	case "completion":
		return runCompletion(cmdArgs, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, versionString())
		return 0
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "db-query: unknown command %q\n\n%s", rest[0], usage)
		return 1
	}
}

type commonFlags struct {
	host     string
	config   string
	output   string
	timeout  time.Duration
	database string
}

// toSessionFlags converts the CLI's commonFlags into session.CommonFlags at
// the boundary into the session package — the two types are kept separate so
// commonFlags's field names (used pervasively across every command's flag
// parsing) never need renaming.
func toSessionFlags(c commonFlags) session.CommonFlags {
	return session.CommonFlags{
		Host:     c.host,
		Config:   c.config,
		Output:   c.output,
		Timeout:  c.timeout,
		Database: c.database,
	}
}

const (
	defaultOutput  = render.AutoFormat
	defaultTimeout = 30 * time.Second
	// defaultMaxColWidth caps a table cell's display width. Wide enough for a
	// timestamp or a short description; a blob column is truncated rather than
	// allowed to push every other column off the screen.
	defaultMaxColWidth = 50
)

// envDefaults seeds the shared flags from the environment: DB_QUERY_HOST and
// DB_QUERY_DATABASE stand in for --host/--database, so one export makes every
// later command in that shell short. DB_QUERY_OUTPUT pins the format, which is
// how a caller that must not receive tables — an agent, a CI job — opts out of
// the auto default once instead of passing --output on every invocation.
// --config is left empty because DB_QUERY_CONFIG is already read further down,
// by config.DefaultPath.
func envDefaults() commonFlags {
	output := os.Getenv("DB_QUERY_OUTPUT")
	if output == "" {
		output = defaultOutput
	}
	return commonFlags{
		host:     os.Getenv("DB_QUERY_HOST"),
		database: os.Getenv("DB_QUERY_DATABASE"),
		output:   output,
		timeout:  defaultTimeout,
	}
}

// addCommon registers the flags shared by every subcommand. Each long flag
// and its single-letter shorthand bind the same variable, so either spelling
// sets the value. def supplies every default, which is how a value given
// before the command — or in the environment — is inherited: the subcommand's
// own spelling overrides it only when actually given.
func addCommon(fs *flag.FlagSet, c *commonFlags, def commonFlags) {
	fs.StringVar(&c.host, "host", def.host, "host entry from config")
	fs.StringVar(&c.host, "H", def.host, "shorthand for --host")
	fs.StringVar(&c.config, "config", def.config, "config file path")
	fs.StringVar(&c.config, "c", def.config, "shorthand for --config")
	fs.StringVar(&c.output, "output", def.output, "output format: "+strings.Join(render.Formats(), "|"))
	fs.StringVar(&c.output, "o", def.output, "shorthand for --output")
	fs.DurationVar(&c.timeout, "timeout", def.timeout, "per-invocation deadline")
	fs.DurationVar(&c.timeout, "t", def.timeout, "shorthand for --timeout")
	fs.StringVar(&c.database, "database", def.database, "override the host's configured database")
	fs.StringVar(&c.database, "d", def.database, "shorthand for --database")
}

// newFlagSet builds a subcommand FlagSet whose parse errors go to stderr.
// Automatic usage printing is suppressed: a parse error already names the
// offending flag, and -h/--help is mapped to the full usage text by exitParse.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	return fs
}

// exitParse maps a flag-parse failure to its exit code: -h/--help prints the
// usage text on stdout and exits 0; anything else was already reported on
// stderr by the FlagSet (exit 1).
func exitParse(err error, stdout io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	return 1
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

func runQuery(args []string, globals commonFlags, stdout, stderr io.Writer) int {
	var c commonFlags
	params := paramFlags{}
	var sqlFile, saveName, sourceName, category string
	var refreshSchema, noHeaders, force bool
	var maxColWidth int
	var border string
	fs := newFlagSet("query", stderr)
	addCommon(fs, &c, globals)
	fs.Var(params, "param", "bind a query parameter (repeatable)")
	fs.Var(params, "p", "shorthand for --param")
	fs.StringVar(&sqlFile, "file", "", `read SQL from file ("-" for stdin)`)
	fs.StringVar(&sqlFile, "f", "", "shorthand for --file")
	fs.StringVar(&saveName, "save", "", "save the query under this name after it succeeds")
	fs.StringVar(&sourceName, "source", "", "run a saved query by name")
	fs.StringVar(&sourceName, "s", "", "shorthand for --source")
	fs.StringVar(&category, "category", savedquery.DefaultCategory, "saved-query category for --save/--source")
	fs.StringVar(&category, "C", savedquery.DefaultCategory, "shorthand for --category")
	fs.BoolVar(&refreshSchema, "refresh-schema", false, "rebuild the schema cache before running")
	fs.BoolVar(&noHeaders, "no-headers", false, "text/table output: omit the header line")
	fs.IntVar(&maxColWidth, "max-col-width", defaultMaxColWidth, "table output: truncate cells wider than this (0 = unlimited)")
	fs.StringVar(&border, "border", render.DefaultBorder, "table output: frame style ("+strings.Join(render.Borders(), "|")+")")
	fs.BoolVar(&force, "force", false, "with --save: overwrite an existing query and bypass the duplicate check")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return exitParse(err, stdout)
	}
	if err := render.ValidBorder(border); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	sourceSet := set["source"] || set["s"]

	// Flag-combination usage errors, checked before any secret is resolved.
	// A saved query is a complete unit: it is either sourced or authored, and
	// running one is not the moment to author another.
	if sourceSet && set["save"] {
		render.Error(stderr, c.output, "--source and --save are mutually exclusive; pick one")
		return 1
	}
	if sourceSet && (len(positional) > 0 || sqlFile != "") {
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
	if !sourceSet {
		var err error
		sql, err = readSQL(positional, sqlFile)
		if err != nil {
			render.Error(stderr, c.output, err.Error())
			return 1
		}
	}

	r, code := session.Setup(toSessionFlags(c), stderr)
	if code != 0 {
		return code
	}

	if sourceSet {
		sq, err := savedquery.Load(sourceName, category)
		if err != nil {
			render.Error(stderr, c.output, sourceUnavailable(sourceName, category))
			return 1
		}
		// A saved query is provider-bound; running it against a host of a
		// different provider would send provider-specific SQL to the wrong
		// client. Refuse rather than let it fail obscurely mid-flight.
		if sq.Provider != r.Host.Provider {
			render.Error(stderr, c.output, fmt.Sprintf(
				"saved query %s/%s is bound to provider %q, but host %q uses provider %q; refusing to run",
				sq.Category, sq.Name, sq.Provider, r.Host.Name, r.Host.Provider))
			return 1
		}
		sql = sq.SQL
	}

	// The schema cache is a silent side effect: build it the first time a
	// host+database is seen, or when --refresh-schema forces a rebuild,
	// before the user query runs. A build failure stops here — the user
	// query never runs against a schema we could not introspect.
	cachePath := schema.CachePath(r.Host.Host, r.Host.Database)
	if refreshSchema || !schema.Exists(cachePath) {
		if code := session.BuildSchema(r, cachePath, toSessionFlags(c), stderr); code != 0 {
			return code
		}
	}

	rows, code := session.RunOnce(r, adapter.Query{SQL: sql, Params: params}, toSessionFlags(c), true, stderr)
	if code != 0 {
		return code // a non-zero run saves nothing and returns its own code
	}
	if code := renderRows(rows, c.output, noHeaders, maxColWidth, border, stdout, stderr); code != 0 {
		return code
	}

	// Save on success only: the query ran, exited 0, and its output has been
	// printed. The stored SQL carries placeholders, never the resolved --param
	// values. A refusal (a duplicate or an existing file, without --force) is a
	// usage error surfaced on stderr honouring --output — the run itself already
	// stands, so its output is not retracted.
	if set["save"] {
		sq, err := savedquery.Save(saveName, category, r.Host.Provider, sql, force)
		if err != nil {
			render.Error(stderr, c.output, "saving query: "+err.Error())
			return 1
		}
		fmt.Fprintf(stderr, "db-query: saved as %s/%s\n", sq.Category, sq.Name)
	}
	return 0
}

// runList lists the saved queries in the store, optionally restricted to
// one --category. text prints a table (category, name, provider, short hash,
// SQL preview) through the shared render pivot; json emits the full records
// (category, name, provider, sqlhash, sql) so a caller can match against them.
func runList(args []string, globals commonFlags, stdout, stderr io.Writer) int {
	var c commonFlags
	var category string
	fs := newFlagSet("list", stderr)
	addCommon(fs, &c, globals)
	fs.StringVar(&category, "category", "", "restrict to one saved-query category")
	fs.StringVar(&category, "C", "", "shorthand for --category")
	if err := fs.Parse(args); err != nil {
		return exitParse(err, stdout)
	}
	if err := render.Valid(c.output); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	list, err := savedquery.List(category)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if c.output == "json" {
		return renderListJSON(list, c.output, stdout, stderr)
	}
	rows := adapter.Rows{Columns: []string{"category", "name", "provider", "hash", "sql"}}
	for _, q := range list {
		cat, name, prov := q.Category, q.Name, q.Provider
		hash := shortHash(q.SQLHash)
		preview := previewSQL(q.SQL)
		rows.Rows = append(rows.Rows, []*string{&cat, &name, &prov, &hash, &preview})
	}
	return renderRows(rows, c.output, false, defaultMaxColWidth, render.DefaultBorder, stdout, stderr)
}

// queryListing is the JSON shape of one saved query in the list command:
// exactly the fields a caller needs to match a request against the store.
type queryListing struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	SQLHash  string `json:"sqlhash"`
	SQL      string `json:"sql"`
}

// renderListJSON emits the listing as a JSON array (empty as []), honouring
// --output on the error path.
func renderListJSON(list []savedquery.SavedQuery, output string, stdout, stderr io.Writer) int {
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

// runSchema presents the cached schema for a host+database: the full
// catalogue by default, one table's rows when a table name is given, or the
// distinct table names with --tables. It is the read counterpart of
// introspect: cache-first, silently building the cache when absent (like
// the query path) and hitting the live database only then or under
// --refresh-schema.
func runSchema(args []string, globals commonFlags, stdout, stderr io.Writer) int {
	var c commonFlags
	var tablesOnly, refreshSchema, noHeaders bool
	var maxColWidth int
	var border string
	fs := newFlagSet("schema", stderr)
	addCommon(fs, &c, globals)
	fs.BoolVar(&tablesOnly, "tables", false, "print one schema-qualified table name per line")
	fs.BoolVar(&tablesOnly, "T", false, "shorthand for --tables")
	fs.BoolVar(&refreshSchema, "refresh-schema", false, "rebuild the schema cache first")
	fs.BoolVar(&noHeaders, "no-headers", false, "text/table output: omit the header line")
	fs.IntVar(&maxColWidth, "max-col-width", defaultMaxColWidth, "table output: truncate cells wider than this (0 = unlimited)")
	fs.StringVar(&border, "border", render.DefaultBorder, "table output: frame style ("+strings.Join(render.Borders(), "|")+")")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return exitParse(err, stdout)
	}
	if err := render.ValidBorder(border); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if len(positional) > 1 {
		render.Error(stderr, c.output, fmt.Sprintf("expected at most one table name, got %d", len(positional)))
		return 1
	}
	if tablesOnly && len(positional) > 0 {
		render.Error(stderr, c.output, "--tables and a table name are mutually exclusive; pick one")
		return 1
	}

	r, code := session.Setup(toSessionFlags(c), stderr)
	if code != 0 {
		return code
	}

	cachePath := schema.CachePath(r.Host.Host, r.Host.Database)
	if refreshSchema || !schema.Exists(cachePath) {
		if code := session.BuildSchema(r, cachePath, toSessionFlags(c), stderr); code != 0 {
			return code
		}
	}
	rows, err := schema.Read(cachePath)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}

	switch {
	case tablesOnly:
		rows, err = tableNames(rows)
	case len(positional) == 1:
		var matched bool
		rows, matched, err = filterTable(rows, positional[0])
		if err == nil && !matched {
			render.Error(stderr, c.output, fmt.Sprintf(
				"table %q not found in the cached schema\nhint: if it was created recently, re-run with --refresh-schema to rebuild the schema cache",
				positional[0]))
			return 3
		}
	}
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	return renderRows(rows, c.output, noHeaders, maxColWidth, border, stdout, stderr)
}

// catalogColumns locates the schema- and table-name columns in a cached
// catalogue. The lookup is case-insensitive because providers differ in
// catalog identifier case (postgres: table_schema, sqlserver: TABLE_SCHEMA).
// A cache without both columns is not a catalogue this command can present.
func catalogColumns(rows adapter.Rows) (schemaIdx, tableIdx int, err error) {
	schemaIdx, tableIdx = -1, -1
	for i, col := range rows.Columns {
		switch {
		case strings.EqualFold(col, "table_schema"):
			schemaIdx = i
		case strings.EqualFold(col, "table_name"):
			tableIdx = i
		}
	}
	if schemaIdx < 0 || tableIdx < 0 {
		return 0, 0, fmt.Errorf("the schema cache has no table_schema/table_name columns; rebuild it with db-query introspect")
	}
	return schemaIdx, tableIdx, nil
}

// cell returns a row's value at idx, treating a missing or NULL cell as "".
func cell(row []*string, idx int) string {
	if idx >= len(row) || row[idx] == nil {
		return ""
	}
	return *row[idx]
}

// tableNames reduces a catalogue to one schema-qualified name per distinct
// table, in catalogue order, as a single "table" column — one name per line
// in text output, the grep/xargs-friendly shape of --tables.
func tableNames(rows adapter.Rows) (adapter.Rows, error) {
	schemaIdx, tableIdx, err := catalogColumns(rows)
	if err != nil {
		return adapter.Rows{}, err
	}
	out := adapter.Rows{Columns: []string{"table"}}
	seen := map[string]bool{}
	for _, row := range rows.Rows {
		name := cell(row, tableIdx)
		if s := cell(row, schemaIdx); s != "" {
			name = s + "." + name
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		n := name
		out.Rows = append(out.Rows, []*string{&n})
	}
	return out, nil
}

// filterTable keeps only the catalogue rows for one table. A bare name
// matches that table in any schema; a dotted name ("public.users") pins the
// schema. Matching is case-insensitive on both parts, as SQL identifiers
// are unless quoted. The bool reports whether anything matched, so the
// caller can distinguish an unknown table from an empty one.
func filterTable(rows adapter.Rows, name string) (adapter.Rows, bool, error) {
	schemaIdx, tableIdx, err := catalogColumns(rows)
	if err != nil {
		return adapter.Rows{}, false, err
	}
	wantSchema, wantTable, qualified := strings.Cut(name, ".")
	if !qualified {
		wantSchema, wantTable = "", name
	}
	out := adapter.Rows{Columns: rows.Columns}
	for _, row := range rows.Rows {
		if !strings.EqualFold(cell(row, tableIdx), wantTable) {
			continue
		}
		if qualified && !strings.EqualFold(cell(row, schemaIdx), wantSchema) {
			continue
		}
		out.Rows = append(out.Rows, row)
	}
	return out, len(out.Rows) > 0, nil
}

func runIntrospect(args []string, globals commonFlags, stdout, stderr io.Writer) int {
	var c commonFlags
	// --refresh-schema is accepted for symmetry with query; introspect
	// always rebuilds the cache regardless, so the flag is a no-op here.
	var refreshSchema bool
	fs := newFlagSet("introspect", stderr)
	addCommon(fs, &c, globals)
	fs.BoolVar(&refreshSchema, "refresh-schema", false, "rebuild the schema cache (introspect always rebuilds)")
	if err := fs.Parse(args); err != nil {
		return exitParse(err, stdout)
	}

	r, code := session.Setup(toSessionFlags(c), stderr)
	if code != 0 {
		return code
	}

	// introspect always rebuilds: run the provider catalog query, persist
	// it to the cache, then print it.
	rows, code := session.RunOnce(r, adapter.Query{SQL: r.Adapter.IntrospectSQL()}, toSessionFlags(c), false, stderr)
	if code != 0 {
		return code
	}
	if err := schema.Write(schema.CachePath(r.Host.Host, r.Host.Database), rows); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	// The connection is already open and this command exists to rebuild caches,
	// so the database list is refreshed in the same run. Failure is a warning.
	refreshDatabaseList(r, c, stderr)
	return renderRows(rows, c.output, false, defaultMaxColWidth, render.DefaultBorder, stdout, stderr)
}

// refreshDatabaseList re-runs a host's catalog listing and persists the names
// for --database completion. It is a bonus attached to introspect, so every
// failure is a warning and never an exit code: introspect's contract is to
// rebuild the schema, which has already succeeded by the time this runs.
//
// It only ever catches transport-level failure — a client that could not start
// or exited nonzero. Neither provider errors on a permission shortfall; a
// restricted login simply sees fewer databases, and a truncated list is
// indistinguishable from a complete one (design.md §13.9). The warning is plain
// text even under --output json, because the command is succeeding and stderr
// carries structured errors only on failure.
func refreshDatabaseList(r session.Resolved, c commonFlags, stderr io.Writer) {
	// io.Discard: the underlying error must not print as though introspect
	// itself had failed. The warning points at the command that will show it.
	if _, code := listDatabases(r, c, io.Discard); code != 0 {
		fmt.Fprintf(stderr, "db-query: warning: could not refresh the database list for %s; "+
			"--database completion may be stale. Run 'db-query --host %s databases' to see why.\n",
			r.Host.Name, r.Host.Name)
	}
}

// runDatabases lists the databases reachable on a host and persists the names
// for shell completion. It is the only writer of that cache: the completion
// helper reads the file and never opens a connection of its own, which is what
// keeps a TAB from resolving a credential (design.md §13.9).
func runDatabases(args []string, globals commonFlags, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := newFlagSet("databases", stderr)
	addCommon(fs, &c, globals)
	if err := fs.Parse(args); err != nil {
		return exitParse(err, stdout)
	}

	r, code := session.Setup(toSessionFlags(c), stderr)
	if code != 0 {
		return code
	}

	rows, code := listDatabases(r, c, stderr)
	if code != 0 {
		return code
	}
	return renderRows(databaseListRows(rows), c.output, false, defaultMaxColWidth, render.DefaultBorder, stdout, stderr)
}

// listDatabases runs a host's catalog listing and persists the names for
// --database completion, returning the rows so a caller can render them too.
// The listing lives in internal/session so internal/tui's startup picker can
// run the same one; this wrapper is only the commonFlags conversion.
func listDatabases(r session.Resolved, c commonFlags, errOut io.Writer) (adapter.Rows, int) {
	return session.ListDatabases(r, toSessionFlags(c), errOut)
}

// databaseListRows renames the provider's column to a neutral "database".
// Postgres returns datname and SQL Server returns name; the CLI presents one
// vocabulary so a caller parsing the output does not have to know the provider.
// It stays here rather than beside the listing because it is a presentation
// concern of the databases command, not of the query the listing runs.
func databaseListRows(rows adapter.Rows) adapter.Rows {
	if len(rows.Columns) > 0 {
		rows.Columns = append([]string{"database"}, rows.Columns[1:]...)
	}
	return rows
}

func runHosts(args []string, globals commonFlags, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := newFlagSet("hosts", stderr)
	addCommon(fs, &c, globals)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return exitParse(err, stdout)
	}
	if len(positional) > 1 {
		render.Error(stderr, c.output, fmt.Sprintf("expected at most one host name, got %d", len(positional)))
		return 1
	}
	cfg, err := session.LoadConfig(c.config)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if len(positional) == 1 {
		h, err := cfg.Host(positional[0])
		if err != nil {
			render.Error(stderr, c.output, err.Error())
			return 1
		}
		return renderRows(hostDetailRows(h), c.output, false, defaultMaxColWidth, render.DefaultBorder, stdout, stderr)
	}
	rows := adapter.Rows{Columns: []string{"host", "provider", "database"}}
	for _, name := range cfg.HostNames() {
		h := cfg.Hosts[name]
		n, p, d := name, h.Provider, h.Database
		rows.Rows = append(rows.Rows, []*string{&n, &p, &d})
	}
	return renderRows(rows, c.output, false, defaultMaxColWidth, render.DefaultBorder, stdout, stderr)
}

// hostDetailRows renders one host's effective configuration as key/value/source
// triples: core keys in a fixed order, then adapter keys sorted, so the output
// is stable enough to read by eye and to diff between two hosts. Unset keys are
// omitted.
//
// Nothing here is resolved: 'username' and 'credential' print as the literal
// resolver URIs the config holds. Those are pointers, already visible in the
// file, so listing a host never touches a keychain, a vault, or the network.
func hostDetailRows(h config.HostConfig) adapter.Rows {
	rows := adapter.Rows{Columns: []string{"key", "value", "source"}}
	add := func(key, value string) {
		if value == "" {
			return
		}
		k, v, s := key, value, h.Origins[key]
		rows.Rows = append(rows.Rows, []*string{&k, &v, &s})
	}
	add("provider", h.Provider)
	add("host", h.Host)
	if h.Port != 0 {
		add("port", strconv.Itoa(h.Port))
	}
	add("database", h.Database)
	add("username", h.Username)
	add("credential", h.Credential)
	// readonly always prints, unlike the keys above, because its default is
	// true and the absence of a line would read as "not set" rather than
	// "writes are refused here". A posture has to be visible to be trusted.
	{
		k, v, src := "readonly", strconv.FormatBool(h.ReadOnly), h.Origins["readonly"]
		if src == "" {
			src = "default"
		}
		rows.Rows = append(rows.Rows, []*string{&k, &v, &src})
	}

	extras := make([]string, 0, len(h.Extra))
	for k := range h.Extra {
		extras = append(extras, k)
	}
	sort.Strings(extras)
	for _, k := range extras {
		add(k, h.Extra[k])
	}
	return rows
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

// renderRows writes rows through the single render pivot, honouring the
// output format and cross-format options. It returns 0 or exit code 1.
//
// This is also where render.AutoFormat is resolved: it is the one place every
// command's rows converge on and it already holds the destination writer, so
// the TTY probe happens once, in the CLI layer, and no renderer has to inspect
// what it is writing to.
func renderRows(rows adapter.Rows, output string, noHeaders bool, maxColWidth int, border string, stdout, stderr io.Writer) int {
	output = resolveOutput(output, stdout)
	opts := render.Options{NoHeaders: noHeaders, MaxColWidth: maxColWidth, Border: border}
	if err := render.Render(stdout, output, rows, opts); err != nil {
		render.Error(stderr, output, err.Error())
		return 1
	}
	return 0
}

// isTerminal reports whether w is a character device — the same class a
// real terminal belongs to. It is a type assertion, not a build-tagged
// isatty call: dependency-free, and a *bytes.Buffer or *strings.Builder in a
// test is not an *os.File, so it reads false exactly like a pipe or redirect.
// isTerminal and launchTUI are variables rather than plain functions so a test
// can exercise the branch that only exists on a terminal — running no command
// at all opens the interactive mode — without needing a real one.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

var launchTUI = tui.Run

// resolveOutput maps render.AutoFormat onto a concrete format: table when w
// is a terminal, text otherwise. Any explicit format passes through
// untouched.
func resolveOutput(format string, w io.Writer) string {
	if format != render.AutoFormat {
		return format
	}
	if isTerminal(w) {
		return "table"
	}
	return "text"
}
