# db-query

A Go CLI that wraps native database clients (`psql`, `sqlcmd`), resolves
credentials on demand from a configured source, and returns query output
in a selectable format — an aligned table at a terminal, tab-separated text
when piped. See `docs/design.md` for the full design.

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
db-query list       --category reports        # list saved queries
db-query schema     --host prod-core          # show the cached schema (tables + columns)
db-query schema     --host prod-core people   # one table's columns (bare or schema-qualified name)
db-query schema     --host prod-core --tables # one schema-qualified table name per line
db-query introspect --host prod-core          # list tables + columns, always live
db-query --host prod-core databases           # list databases, caching them for --database completion
db-query hosts                                # list configured hosts
db-query hosts      lionel                    # one host's effective config, with each key's source
```

- SQL is given as one positional argument, via `-f file` (`-` for stdin), piped on stdin,
  or by name with `--source` (a saved query). Flags may appear before or after the SQL
  argument (`… "SELECT …" --param who=Ada` and `… --param who=Ada "SELECT …"` are equivalent);
  SQL that itself begins with `-` must be passed via `-f` or stdin.
- Most flags have a single-letter shorthand: `--host (-H)`, `--database (-d)`,
  `--config (-c)`, `--output (-o)`, `--param (-p)`, `--file (-f)`, `--source (-s)`,
  `--category (-C)`, `--timeout (-t)`, `--help (-h)`, `--version (-v)`. Deliberate
  actions (`--save`, `--force`, `--refresh-schema`, `--no-headers`,
  `--max-col-width`, `--border`) are long-only.
- Commands have shorthands too: `query (q)`, `schema (s)`, `introspect (i)`,
  `list (ls, l)`.

### Shared flags before the command

The five shared flags — `--host`, `--database`, `--config`, `--output`,
`--timeout` — may also be given **before** the command. Typical use is a
schema lookup followed by the query it informs: with the connection up front,
the part that changes stays at the end of the line, so the previous command
can be edited rather than retyped.

```sh
db-query --host test --database testdb schema --tables
db-query --host test --database testdb query "SELECT * FROM todos;"
```

The same flag given after the command wins over one given before it
(`--host a query --host b` runs against `b`). Only those five are accepted
there; a command's own flags (`--tables`, `--param`, …) belong after the
command and error out before it.

### Environment defaults

`DB_QUERY_HOST` and `DB_QUERY_DATABASE` supply the defaults for `--host` and
`--database`, so a shell that works on one database can drop them entirely:

```sh
export DB_QUERY_HOST=testhost
export DB_QUERY_DATABASE=testdb
db-query s --tables
db-query q "SELECT count(*) FROM todos;"
```

`DB_QUERY_OUTPUT` does the same for `--output`. Because the default is `auto`,
this is mainly how a caller that must never receive tables opts out once
instead of passing `--output` on every invocation:

```sh
export DB_QUERY_OUTPUT=text   # or json — pin the machine-readable shape
```

Piping already selects `text` on its own, so this is only needed when something
runs `db-query` with a terminal attached and still wants the stable format.

Precedence is flag (either position) → environment → config file. Note that an
exported host is invisible state: `db-query hosts` shows what is configured, but
the host in effect is whatever `DB_QUERY_HOST` says until you override it.
- Params bind through the client's own `-v` mechanism: `:'name'` in psql
  SQL, `$(name)` in sqlcmd SQL. Values are never substituted into SQL by
  this tool.
- `--output json|table|text|auto` (default `auto`). `auto` renders a bordered
  table when stdout is a terminal and tab-separated `text` otherwise, so
  results are readable by eye while a pipe, a redirect, or a program calling
  `db-query` still gets the stable machine-readable shape. Pass `--output`
  explicitly to force either one, or export `DB_QUERY_OUTPUT` to pin it for a
  whole shell. Every command that prints rows — `query`, `list`, `schema`,
  `introspect`, `hosts` — honours the setting. In `json` mode errors are
  emitted as structured JSON on stderr.
- `--max-col-width <n>` (`query`, `schema`; default 50, `0` = unlimited) caps a
  **table** cell at `n` display cells, truncating with `…`. Only `table` output
  is affected; `text` and `json` always carry values whole.
- `--border ascii|light|markdown|none` (`query`, `schema`; default `ascii`)
  picks the table frame:

  | Style | Frame | Use |
  |---|---|---|
  | `ascii` | `+--+ \| --` | portable — any terminal, font, locale, or plain-text paste |
  | `light` | `┌─┬┐ │` | Unicode box-drawing; sharper, needs a UTF-8 locale and glyphs |
  | `markdown` | `\| --- \|` | paste straight into an issue, PR, or notes file |
  | `none` | none | aligned columns, no frame — `text`'s shape, padded into place |

  `markdown` omits the row-count footer so the output pastes verbatim. Combined
  with `--no-headers` it emits data rows without the `---` rule, which appends
  to an existing table rather than standing alone.
- `--database <db>` (`-d`) overrides the host's configured `database` for this
  run (on `query`, `schema`, `introspect`, and `databases`), so one host entry
  can reach sibling databases on the same server without a second config block.
  Its value completes from the cached database list — see
  [Shell completion](#shell-completion-zsh).
- `--no-headers` omits the header line. In `text` it tab-separates the rows for
  any shape, so a 1×1 result prints just the bare value; in `table` it drops the
  header row and the row-count footer, leaving only the framed data. It is a
  no-op for `--output json`, whose objects are already self-describing.

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
- `db-query list [--category <cat>] [--output <format>]` (`ls`, `l`) lists the store.
  `text`/`table` show category, name, provider, short hash and SQL preview;
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

- `db-query schema` presents the cache without touching the database: the full
  catalogue, one table's columns (`schema <table>`, bare or schema-qualified,
  case-insensitive), or the distinct table names (`--tables`, one
  schema-qualified name per line — grep/xargs-friendly). A missing cache is
  built silently first; a table not in the cache exits `3` with a
  `--refresh-schema` hint. Example round trip:

  ```sh
  db-query schema --tables --host prod-core | grep -m1 people | xargs db-query schema --host prod-core
  ```

- `--refresh-schema` (on `query`, `schema`, and `introspect`) rebuilds the cache first.
- `db-query introspect` always rebuilds the cache and prints the schema.
- A schema error (exit code `3`) does **not** auto-rebuild — re-run with
  `--refresh-schema`, the only trigger that rebuilds the cache.

### Database list cache

Separate from the schema cache and used only to complete `--database`:

```sh
db-query --host prod-core databases      # prints the list and caches the names
```

Names land in `$XDG_CACHE_HOME/db-query/databases/` (fallback
`~/.cache/db-query/databases/`), one file per host, holding a plain JSON array.
`db-query introspect` refreshes it too, since it already has a connection open;
`query` does not. There is no expiry — the list is rebuilt when you next run
`databases`. Only databases the login can actually connect to are listed.

The file is keyed on the **host's config name**, not the server it resolves to,
because completion must be able to find it without resolving a credential. So
if you repoint a host entry at a different server, re-run `databases` to
replace the previous server's names.

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

The password source is always the **`credential`** key — a resolver URI, never
a plaintext password. A host key named `password` (or `pwd`/`pass`/`passwd`) is
rejected at load with a pointer to `credential`, so a value under the wrong key
can't be silently ignored and leave the client prompting for a password.

### Sharing configuration between hosts

Hosts that differ only in their address don't need to repeat everything else. A
`[profiles.<name>]` section holds the shared keys and a host picks them up with
`inherit`:

```toml
[profiles.pg]
provider   = "postgres"
database   = "postgres"

[profiles.eus]                        # profiles may inherit profiles
inherit    = "pg"
username   = "gchifanzwa"
credential = "bws:<secret-id>"

[hosts.lionel]
inherit    = "eus"
host       = "lionel.internal"

[hosts.norton]
inherit    = "eus"
host       = "norton.internal"
```

A profile accepts every key a host does, including provider-specific ones, which
merge key by key rather than wholesale. Precedence: **an explicit host key wins
over the nearest profile in the chain, which wins over anything the resolved
credential supplies.**

A profile is not connectable — `--host eus` says so rather than reporting an
unknown host, profiles never appear in `db-query hosts` or in `--host`
completion, and `inherit` is consumed by the loader so it can never reach a
client as a connection parameter. An `inherit` naming a profile that doesn't
exist, a cycle, or a host left with no `provider` after merging all fail at load
with the offending section named.

Inheritance means a host's settings are no longer all in one place, so
`db-query hosts <name>` prints the merged result and where each key came from:

```
$ db-query hosts lionel
+------------+-------------------+-------------+
| key        | value             | source      |
+------------+-------------------+-------------+
| provider   | postgres          | profile pg  |
| database   | postgres          | profile pg  |
| username   | gchifanzwa        | profile eus |
| credential | bws:<secret-id>   | profile eus |
| host       | lionel.internal   | host lionel |
+------------+-------------------+-------------+
```

It resolves nothing: `username` and `credential` print as the resolver URIs the
config holds, so the command never opens a vault, a keychain, or a connection.

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
subcommands (and their shorthands), flags — before or after the command — and
the `--output` values, plus **dynamic** values read from your local files: host
names (`--host`), saved queries (`--source`), categories (`--category`) — each
shown with a short description — and database names (`--database`). These come
from a hidden `db-query __complete` command the script calls on TAB; it reads
only config, cache and saved-query files, never a credential or a database.

`--database` completes from the list `db-query databases` cached for that host,
so it needs the host to be known — given as `--host`/`-H` anywhere on the line,
or exported as `DB_QUERY_HOST`:

```sh
db-query --host prod-core databases      # once, to populate the list
db-query --host prod-core --database <TAB>
```

A host that has never been listed simply offers nothing. That is deliberate:
completing it live would mean resolving a credential on a keystroke, and a
`bws:`/`bw:` resolver shells out to the Bitwarden CLI — a TAB that stalls for
seconds or prompts for a vault unlock. `db-query introspect` refreshes the list
as well, so any host you have introspected already has it.

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
