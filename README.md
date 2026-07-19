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
- Exit codes: `0` success, `1` config/credential/usage errors, `2` the
  client binary could not start; a client that ran and failed propagates
  its own exit code.

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
make test           # unit tests
make cover          # unit tests + coverage summary
make integration    # docker compose up, run psql + sqlcmd suites, tear down
```

Integration tests need Docker and local `psql`/`sqlcmd` binaries. Image
refs are overridable when pulling through a mirror:

```sh
DBQ_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:16-alpine make integration
```
