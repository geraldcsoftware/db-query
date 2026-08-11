// Package session resolves a host's adapter and credential once, and runs a
// query through the shared adapter/executor pipeline. It is the seam
// between internal/cli's command implementations and internal/tui's
// interactive session — both call Setup and RunOnce so host/credential
// resolution and query execution are defined exactly once. cli imports
// session and tui; tui imports session only. Neither cli nor session ever
// imports tui, so there is no import cycle.
package session

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/render"
	"github.com/geraldcsoftware/db-query/internal/schema"
)

// CommonFlags carries the five flags shared by every db-query command
// (--host, --config, --output, --timeout, --database), mirroring
// internal/cli's unexported commonFlags shape.
type CommonFlags struct {
	Host     string
	Config   string
	Output   string
	Timeout  time.Duration
	Database string
}

// Resolved bundles a host's adapter, its resolved credential, and the merged
// host config — the setup shared by every query-running command.
type Resolved struct {
	Adapter adapter.Adapter
	Cred    credential.Credential
	Host    config.HostConfig // after MergeCredential; Host/Database are final
}

// LoadConfig loads the config file at path, or the default location when
// path is empty.
func LoadConfig(path string) (config.Config, error) {
	if path == "" {
		path = config.DefaultPath()
	}
	if path == "" {
		return config.Config{}, fmt.Errorf("cannot determine config path; set --config or DB_QUERY_CONFIG")
	}
	return config.Load(path)
}

// Setup validates the output format and host flag, loads config, selects
// the adapter, resolves the credential lazily (only this host's secret is
// touched), and merges it into the host config. On any failure it renders
// the error and returns exit code 1.
func Setup(c CommonFlags, stderr io.Writer) (Resolved, int) {
	if err := render.Valid(c.Output); err != nil {
		render.Error(stderr, c.Output, err.Error())
		return Resolved{}, 1
	}
	if c.Host == "" {
		render.Error(stderr, c.Output, "--host is required (pass --host/-H before or after the command, or export DB_QUERY_HOST)")
		return Resolved{}, 1
	}
	cfg, err := LoadConfig(c.Config)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
		return Resolved{}, 1
	}
	host, err := cfg.Host(c.Host)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
		return Resolved{}, 1
	}
	a, err := adapter.For(host.Provider)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
		return Resolved{}, 1
	}
	cred, err := resolveCredential(cfg, host)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
		return Resolved{}, 1
	}
	host = config.MergeCredential(host, cred)
	// --database/-d overrides the host's configured database (and any value a
	// resolver supplied), so one host entry can reach sibling databases on the
	// same server without a second config block.
	if c.Database != "" {
		host.Database = c.Database
	}
	return Resolved{Adapter: a, Cred: cred, Host: host}, 0
}

// RunOnce builds, runs, and parses a single invocation against a resolved
// host, applying the exit-code contract: 1 (build/parse failure), 2 (client
// could not start), 3 (schema error), 4 (other SQL error), 0 (rows valid).
// It renders any error itself. When hintRefresh is set a schema error also
// hints at --refresh-schema; the internal schema build passes false, since
// that hint only makes sense for the user's query.
func RunOnce(r Resolved, q adapter.Query, c CommonFlags, hintRefresh bool, stderr io.Writer) (adapter.Rows, int) {
	inv, err := r.Adapter.Build(r.Host, q)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
		return adapter.Rows{}, 1
	}
	inv.Env = r.Adapter.Env(r.Cred, r.Host)

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	res, err := executor.Run(ctx, inv)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
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
		if r.Adapter.IsSchemaError(res) {
			if hintRefresh {
				msg += "\nhint: the schema may have changed — re-run with --refresh-schema to rebuild the schema cache"
			}
			render.Error(stderr, c.Output, msg)
			return adapter.Rows{}, 3
		}
		render.Error(stderr, c.Output, msg)
		return adapter.Rows{}, 4
	}
	rows, err := r.Adapter.Parse(res)
	if err != nil {
		render.Error(stderr, c.Output, err.Error())
		return adapter.Rows{}, 1
	}
	return rows, 0
}

// BuildSchema runs the provider introspection and persists the result to
// the schema cache. It is a silent side effect of the query path: the rows
// are cached, never rendered. It returns 0 on success or RunOnce's exit
// code on failure.
func BuildSchema(r Resolved, cachePath string, c CommonFlags, stderr io.Writer) int {
	rows, code := RunOnce(r, adapter.Query{SQL: r.Adapter.IntrospectSQL()}, c, false, stderr)
	if code != 0 {
		return code
	}
	if err := schema.Write(cachePath, rows); err != nil {
		render.Error(stderr, c.Output, err.Error())
		return 1
	}
	return 0
}

// usesBWS reports whether the host resolves any secret through the bws: scheme,
// so the configured access token is fetched only when it is actually needed.
func usesBWS(host config.HostConfig) bool {
	return strings.HasPrefix(host.Credential, "bws:") || strings.HasPrefix(host.Username, "bws:")
}

// bwsOptions resolves the configured Bitwarden Secrets Manager access token
// into resolver Options, lazily: only when the host uses a bws: URI and a
// [bws].accessToken is set. The token source must be a resolver URI and must
// not itself be bws: (chicken-and-egg); an empty section leaves the token
// empty so the resolver falls back to BWS_ACCESS_TOKEN.
func bwsOptions(cfg config.Config, host config.HostConfig) (credential.Options, error) {
	if !usesBWS(host) || cfg.BWS.AccessToken == "" {
		return credential.Options{}, nil
	}
	uri := cfg.BWS.AccessToken
	if strings.HasPrefix(uri, "bws:") {
		return credential.Options{}, fmt.Errorf("bws.accessToken must not be a bws: URI (chicken-and-egg); use env:, keychain:, etc.")
	}
	if !credential.IsURI(uri) {
		return credential.Options{}, fmt.Errorf("bws.accessToken must be a credential URI (e.g. env:NAME or keychain:service), not a raw value")
	}
	tok, err := credential.ResolveScalar(uri)
	if err != nil {
		return credential.Options{}, fmt.Errorf("resolving bws.accessToken: %w", err)
	}
	return credential.Options{BWSAccessToken: tok}, nil
}

// resolveCredential produces the final neutral record for a host: password
// from the credential URI; username from explicit host config (literal or
// URI) with the resolver's own username filling the gap. The BWS access
// token is resolved lazily via bwsOptions and injected into resolution.
func resolveCredential(cfg config.Config, host config.HostConfig) (credential.Credential, error) {
	opts, err := bwsOptions(cfg, host)
	if err != nil {
		return credential.Credential{}, err
	}
	var cred credential.Credential
	if host.Credential != "" {
		cred, err = credential.ResolveWith(host.Credential, opts)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("resolving credential for host %s: %w", host.Name, err)
		}
	}
	switch {
	case host.Username == "":
		// keep cred.Username (resolver-supplied, e.g. bw: or keychain:)
	case credential.IsURI(host.Username):
		u, err := credential.ResolveScalarWith(host.Username, opts)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("resolving username for host %s: %w", host.Name, err)
		}
		cred.Username = u
	default:
		cred.Username = host.Username
	}
	return cred, nil
}
