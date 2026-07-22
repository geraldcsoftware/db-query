# db-query

A Go CLI that wraps native database clients (`psql`, `sqlcmd`), resolves
credentials on demand from a configured source, and returns query output
in a selectable format. See `docs/design.md` for the full design.

```
 cred URI ─▶ [resolver] ─▶ Credential ─▶ [adapter.build] ─▶ Invocation ─▶ [executor.Run] ─▶ RawResult
                                                                                                │
                                        Rows (neutral) ◀─ [adapter.parse] ◀─────────────────────┘
                                             │
                                     [renderer[format]] ─▶ bytes ─▶ stdout
```

## Usage

```sh
db-query query      --host prod-core "SELECT id, name FROM people WHERE name = :'who'" --param who=Ada
db-query query      --host reporting --output json -f report.sql
db-query introspect --host prod-core          # list tables + columns
db-query hosts                                # list configured hosts
```

- SQL is given as one positional argument, via `-f file` (`-` for stdin), or piped on stdin.
- Params bind through the client's own `-v` mechanism: `:'name'` in psql
  SQL, `$(name)` in sqlcmd SQL. Values are never substituted into SQL by
  this tool.
- `--output text|json` (default `text`). In `json` mode errors are
  emitted as structured JSON on stderr.
- `--no-headers` (text output only) omits the header line and tab-separates
  the rows for any shape, so a 1×1 result prints just the bare value. It is
  a no-op for `--output json`, whose objects are already self-describing.

### Schema cache

The first query against a host+database silently introspects its tables and
columns and caches the result under
`$XDG_CACHE_HOME/db-query/schema/` (fallback `~/.cache/db-query/schema/`).
Subsequent queries reuse the cache and do not re-introspect. The build is a
side effect: the user query's result is still what gets printed. If that
internal introspection fails, the error is surfaced and the user query does
not run.

- `--refresh-schema` (on `query` and `introspect`) rebuilds the cache first.
- `db-query introspect` always rebuilds the cache and prints the schema.
- A schema error (exit code `3`) does **not** auto-rebuild — re-run with
  `--refresh-schema`, the only trigger that rebuilds the cache.

### Exit codes

| Code | Meaning |
|------|---------|
| `0`  | success |
| `1`  | config, usage, or credential error |
| `2`  | client binary could not start |
| `3`  | schema error — the query references an unknown table or column (re-run with `--refresh-schema`) |
| `4`  | other SQL error — the client ran and exited nonzero |

## Configuration

`~/.config/db-query/config.toml` (override with `--config` or `DB_QUERY_CONFIG`):

```toml
[hosts.prod-core]
provider   = "postgres"            # or "sqlserver"
host       = "core.internal"
port       = 5432
database   = "core"
username   = "app_user"            # literal, resolver URI, or omitted
credential = "env:DB_CORE_PW"      # resolver URI for the password

[hosts.reporting]
provider   = "sqlserver"
host       = "sql01.internal"
database   = "reports"
username   = "keychain:reporting-sql/svc_reports"
credential = "keychain:reporting-sql"
encrypt    = "true"                # provider-specific keys pass through to the adapter
```

Credential schemes: `env:VAR`, `bws:<secret-id>[#field]` (Bitwarden
Secrets Manager, needs `BWS_ACCESS_TOKEN`), `bw:item/<id-or-name>`
(Bitwarden CLI, needs `BW_SESSION`), `keychain:<service>[/<account>]`
(macOS). Resolution is lazy and per-invocation; passwords ride an
environment overlay to the client, never argv.

## Requirements

- `psql` on PATH for postgres hosts
- `sqlcmd` on PATH for sqlserver hosts ([go-sqlcmd](https://github.com/microsoft/go-sqlcmd) or legacy mssql-tools)

## Development

```sh
make build          # build bin/db-query
make install        # build, then copy the binary to ~/.local/bin (created if absent)
make test           # unit tests
make cover          # unit tests + coverage summary
make integration    # docker compose up, run psql + sqlcmd suites, tear down
```

Integration tests need Docker and local `psql`/`sqlcmd` binaries. Image
refs are overridable when pulling through a mirror:

```sh
DBQ_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:16-alpine make integration
```
