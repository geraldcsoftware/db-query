// Package cli wires resolvers, adapters, executor, and renderers into
// the user-facing command surface.
package cli

import (
	"context"
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
	"github.com/geraldcsoftware/db-query/internal/schema"
)

const usage = `db-query — run SQL against configured hosts via native clients

Usage:
  db-query query      --host <name> [flags] [SQL]   run ad-hoc SQL (positional, -f file, or stdin)
  db-query introspect --host <name> [flags]         list tables and columns, rebuild the schema cache
  db-query hosts      [flags]                       list configured hosts

Flags:
  --host <name>       host entry from the config file
  --config <path>     config file (default: $DB_QUERY_CONFIG or ~/.config/db-query/config.toml)
  --output text|json  output format (default text)
  --param k=v         bind a query parameter (repeatable); psql: :'k', sqlcmd: $(k)
  -f <path>           read SQL from file ("-" for stdin)
  --timeout <dur>     per-invocation deadline (default 30s)
  --refresh-schema    rebuild the cached schema first (query, introspect)
  --no-headers        text output only: omit the header line, tab-separate rows (query)

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

func runQuery(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	params := paramFlags{}
	var sqlFile string
	var refreshSchema, noHeaders bool
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	fs.Var(params, "param", "bind a query parameter (repeatable)")
	fs.StringVar(&sqlFile, "f", "", `read SQL from file ("-" for stdin)`)
	fs.BoolVar(&refreshSchema, "refresh-schema", false, "rebuild the schema cache before running")
	fs.BoolVar(&noHeaders, "no-headers", false, "text output: omit the header line")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	sql, err := readSQL(fs.Args(), sqlFile)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}

	r, code := setup(c, stderr)
	if code != 0 {
		return code
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
		return code
	}
	return renderRows(rows, c.output, noHeaders, stdout, stderr)
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
