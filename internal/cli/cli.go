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
)

const usage = `db-query — run SQL against configured hosts via native clients

Usage:
  db-query query      --host <name> [flags] [SQL]   run ad-hoc SQL (positional, -f file, or stdin)
  db-query introspect --host <name> [flags]         list tables and columns
  db-query hosts      [flags]                       list configured hosts

Flags:
  --host <name>       host entry from the config file
  --config <path>     config file (default: $DB_QUERY_CONFIG or ~/.config/db-query/config.toml)
  --output text|json  output format (default text)
  --param k=v         bind a query parameter (repeatable); psql: :'k', sqlcmd: $(k)
  -f <path>           read SQL from file ("-" for stdin)
  --timeout <dur>     per-invocation deadline (default 30s)
`

// Exit codes: 0 success, 1 config/usage/credential errors, 2 client
// failed to start; a client that ran and exited nonzero propagates its
// own exit code.
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
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	fs.Var(params, "param", "bind a query parameter (repeatable)")
	fs.StringVar(&sqlFile, "f", "", `read SQL from file ("-" for stdin)`)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	sql, err := readSQL(fs.Args(), sqlFile)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	return execute(c, adapter.Query{SQL: sql, Params: params}, stdout, stderr)
}

func runIntrospect(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := flag.NewFlagSet("introspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addCommon(fs, &c)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	// The introspection SQL is provider-specific, so it is looked up
	// after the host's adapter is known; empty SQL marks that request.
	return execute(c, adapter.Query{}, stdout, stderr)
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
	r, err := render.For(c.output)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if err := r.Render(stdout, rows); err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	return 0
}

func readSQL(positional []string, file string) (string, error) {
	switch {
	case len(positional) > 0 && file != "":
		return "", fmt.Errorf("give SQL either as an argument or via -f, not both")
	case len(positional) > 1:
		return "", fmt.Errorf("expected one SQL argument, got %d (quote the query)", len(positional))
	case len(positional) == 1:
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

func execute(c commonFlags, q adapter.Query, stdout, stderr io.Writer) int {
	renderer, err := render.For(c.output)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if c.host == "" {
		render.Error(stderr, c.output, "--host is required")
		return 1
	}
	cfg, err := loadConfig(c.config)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	host, err := cfg.Host(c.host)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	a, err := adapter.For(host.Provider)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if q.SQL == "" {
		q.SQL = a.IntrospectSQL()
	}

	// Lazy, per-invocation resolution: only this host's secret is touched.
	cred, err := resolveCredential(host)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	host = config.MergeCredential(host, cred)

	inv, err := a.Build(host, q)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	inv.Env = a.Env(cred, host)

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	res, err := executor.Run(ctx, inv)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 2
	}
	if res.ExitCode != 0 {
		// go-sqlcmd prints login/connection errors to stdout even with
		// -r 1, so fall back to stdout when stderr is empty.
		detail := strings.TrimSpace(string(res.Stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(res.Stdout))
		}
		msg := fmt.Sprintf("%s exited %d: %s", inv.Argv[0], res.ExitCode, detail)
		if a.IsSchemaError(res) {
			msg += "\nhint: the schema may differ from what the query assumes — run `db-query introspect --host " + c.host + "`"
		}
		render.Error(stderr, c.output, msg)
		return res.ExitCode
	}
	rows, err := a.Parse(res)
	if err != nil {
		render.Error(stderr, c.output, err.Error())
		return 1
	}
	if err := renderer.Render(stdout, rows); err != nil {
		render.Error(stderr, c.output, err.Error())
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
