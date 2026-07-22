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
db-query query      --host prod-core --save people-by-name --category reports \
                    "SELECT id, name FROM people WHERE name = :'who'" --param who=Ada
db-query query      --host prod-core --source people-by-name --category reports --param who=Ada
db-query queries    --category reports        # list saved queries
db-query introspect --host prod-core          # list tables + columns
db-query hosts                                # list configured hosts
```

- SQL is given as one positional argument, via `-f file` (`-` for stdin), piped on stdin,
  or by name with `--source` (a saved query). Flags may appear before or after the SQL
  argument (`… "SELECT …" --param who=Ada` and `… --param who=Ada "SELECT …"` are equivalent);
  SQL that itself begins with `-` must be passed via `-f` or stdin.
- Params bind through the client's own `-v` mechanism: `:'name'` in psql
  SQL, `$(name)` in sqlcmd SQL. Values are never substituted into SQL by
  this tool.
- `--output text|json` (default `text`). In `json` mode errors are
  emitted as structured JSON on stderr.
- `--no-headers` (text output only) omits the header line and tab-separates
  the rows for any shape, so a 1×1 result prints just the bare value. It is
  a no-op for `--output json`, whose objects are already self-describing.

### Saved queries

A query can be saved by name and re-run later, so a good query need not be
retyped and an agent can match a request against a fixed set rather than
free-generate SQL.

- `--save <name>` persists the SQL **only after the query runs successfully**
  (exit 0); its output is printed either way. `--category <cat>` files it
  (default `default`). The stored SQL keeps its placeholders — `--param`
  values are **never** written to disk.
- `--source <name>` runs a saved query by name; `--param` binds as usual.
  `--source` cannot be combined with a positional/`-f` SQL argument or with
  `--save`. A saved query is bound to the provider it was saved against, so
  `--source` refuses to run it against a host of a different provider. A name
  the store does not hold errors (exit `1`) and lists the queries available.
- `--force` (with `--save`) overwrites an existing name and bypasses the
  duplicate check.
- Saving refuses (exit `1`, output still printed) when another stored query
  already holds the same SQL — compared on a normalised hash, so
  whitespace/comment-only differences count as duplicates — or when the target
  name already exists. `--force` overrides both.
- `db-query queries [--category <cat>] [--output text|json]` lists the store.
  `text` is a table (category, name, provider, short hash, SQL preview);
  `json` is an array of `{category, name, provider, sqlhash, sql}` objects a
  caller can match against.

The store lives under `$DB_QUERY_QUERIES_DIR`, else
`$XDG_CONFIG_HOME/db-query/queries`, else `~/.config/db-query/queries`. Each
query is a `<category>/<name>.sql` file: a small reserved header of
`-- db-query:key=value` lines (name, category, provider, sqlhash, saved)
above the raw SQL body. A file holds SQL with placeholders only — never
resolved parameter values or credentials. Your own leading comments in the
body are preserved.

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

### Bitwarden Secrets Manager token

By default the `bws:` scheme reads its access token from the `BWS_ACCESS_TOKEN`
environment variable. To source it elsewhere, add a `[bws]` section whose
`accessToken` is itself a resolver URI:

    [bws]
    accessToken = "env:BWS_ACCESS_TOKEN"   # name any env var
    # accessToken = "keychain:bws-token"   # keep the token out of the shell env

The value must be a resolver URI (`env:`, `keychain:`, …); a raw token in
config is refused, and `bws:` is not allowed (chicken-and-egg). With no `[bws]`
section, the `BWS_ACCESS_TOKEN` environment variable is used.

## Shell completion (zsh)

`db-query completion zsh` prints a zsh completion script. Completion covers
subcommands, flags, and the `--output` values, plus **dynamic** values read
from your local files: host names (`--host`), saved queries (`--source`), and
categories (`--category`) — each shown with a short description. These come
from a hidden `db-query __complete` command the script calls on TAB; it reads
only config and saved-query files, never a credential or a database.

Installed via Homebrew, completion is set up automatically — nothing to do.
Otherwise, pick one route:

```sh
# One line in ~/.zshrc (simplest; self-registers when sourced):
source <(db-query completion zsh)

# Or a file on your fpath (no per-startup cost):
mkdir -p ~/.zsh/completions
db-query completion zsh > ~/.zsh/completions/_db-query
# then, in ~/.zshrc before `compinit`:
#   fpath=(~/.zsh/completions $fpath)
#   autoload -Uz compinit && compinit
```

After first install run `exec zsh` (or `rm -f ~/.zcompdump && compinit`) so
zsh's completion cache picks up the new function.

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
