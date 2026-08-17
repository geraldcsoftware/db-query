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

## Interactive mode

Run `db-query` with no command, at a terminal, and it opens a four-pane
interactive UI instead of printing usage:

```sh
db-query                      # host from --host/DB_QUERY_HOST/config, else a picker
db-query --host prod-core     # skip the picker
```

The host is resolved the usual way — `--host`, then `DB_QUERY_HOST`, then the
config file. When that resolves nothing, a selection screen lists the configured
hosts first, then the chosen host's databases:

```
db-query 1.4.0
No --host or --database was given, so there is no session to open yet.
Choose below to start one. Nothing is written back to your config.

Configured hosts                                                    5
❯ prod-core
  prod-eu
  staging
  analytics
  localdev

↑/↓ move · type filter · Enter select · Esc quit
```

Type to filter the list, arrows (or `Ctrl+p`/`Ctrl+n`) to move — the cursor
wraps at both ends — and `Enter` to take the highlighted name. Choosing a name
behaves exactly as if the matching flag had been passed; nothing is written back
to your config or environment, and `Esc` exits 0 without opening anything.

A database with no cached schema is marked `no schema` in the list, and choosing
one offers to introspect it there and then. Declining returns you to the list:
whatever the session opens on has a catalogue to browse.

The credential is resolved **once**, at startup, and reused for every query in
the session, so a `bw:`/`bws:` vault unlock happens at most once per session
rather than on every run.

Piped or redirected output is unaffected: the UI only opens when stdout is a
terminal, so `db-query | cat` still prints usage and exits 1 exactly as before.

```
db-query 1.4.0                                                 orders ● postgres · prod-core
───────────────────────┬────────────────────────────────────────────────────────────────────
▌SCHEMA                │ QUERY                                                       NORMAL
 ▶ orders            5 │  1 select * from orders limit 20;
 ▼ people            2 │
   id          integer ├────────────────────────────────────────────────────────────────────
   name           text │ RESULTS                                                     2 rows
                       │ #  id  name
                       │ 1   1  ada
───────────────────────┤ 2   2  grace
 SAVED                 │
 default/recent-orders │
 reports/people-by-name│
                       │
───────────────────────┴────────────────────────────────────────────────────────────────────
^h/j/k/l move · ^/⌘Enter/F5 run · F2 switch db · Enter load/expand      ^c cancel · Esc quit
```

The bottom bar drops hints from the right when the terminal is too narrow for
all of them; the exits are never dropped.

The pane the next keystroke reaches is marked `▌` beside its label and drawn in
the accent colour. The number beside a table is how many columns it has; the row
under a sidebar cursor is a full-width bar; result columns whose every value is
a number are right-aligned and coloured.

| Pane | What it holds |
|------|---------------|
| Schema | the current database's **cached** catalogue (`db-query introspect` builds it; the UI introspects only when you ask it to, switching database) |
| Query | an editable SQL buffer |
| Saved | the saved-query store, `category/name` |
| Results | the last run's rows as a table, or its error text |

| Key | Action |
|-----|--------|
| `Ctrl+h/j/k/l` | move focus between panes (also: click a pane) |
| `Ctrl+Enter` or `F5` | run — the Query pane's SQL, or a `SELECT` preview of the Schema pane's selected table |
| `F2` | switch the session to another database on this host |
| `F10` | quit, from every pane |
| `Enter` | Schema: expand/collapse a table · Saved: load the query into the Query pane |
| `PgUp` / `PgDn` | page through the results |
| `↑` `↓` `←` `→` or `k` `j` `h` `l` | Results: scroll a row or a column |
| `g` / `G` | Results: first / last row of the page |
| `Home` / `End` | Results: first / last column |
| `Ctrl+C` | cancel the running query, or quit when idle |
| `Esc` | quit |

A result is usually taller and wider than the pane drawn for it, so the Results
pane scrolls within the page it is showing while `PgUp`/`PgDn` move between
pages. The scroll keys belong to the Results pane and work when it has focus;
paging works from anywhere. A run that returns rows moves focus there for you,
so the scroll keys are live the moment the rows land. A run that fails, times
out or is cancelled leaves focus where it was, beside the SQL. The header row
and the row-number gutter stay put as
you scroll, and a `‹` or `›` against the edge marks columns still hidden that
way. The label row carries both positions, e.g.
`4312 rows · page 2/44 · showing 118-130 · cols 3-7/12`.

`PgUp`/`PgDn`, `Ctrl+C` and `Esc` belong to the editor while the Query pane
holds an embedded Neovim, which is what it holds wherever one can run. `F10` is
the way out that works everywhere either way. See
[The Query editor](#the-query-editor).

### Switching database

`F2` opens a picker over the panes, listing the databases on the current host:

```
              ╭──────────────────────────────────────────────╮
              │ Switch database                              │
              │ Host  prod-core                              │
              │                                              │
              │    orders                                    │
              │  ❯ reporting                                 │
              │    analytics_prod                  no schema │
              │                                              │
              │ ↑/↓ move · type filter · Enter switch · Esc … │
              ╰──────────────────────────────────────────────╯
```

It opens immediately on the names cached by the last listing and refreshes from
the live catalogue behind itself, so `F2` never waits on the network. It works
from any pane, and while it is open it is the only thing keystrokes reach.

Choosing a database marked `no schema` asks before introspecting it. That wait
is cancellable with `Ctrl+C`, and cancelling or failing leaves the session where
it was — as at startup, the session only lands on a database it can browse.

A switch rebuilds the Schema pane for the new database and clears the Results
pane, since those rows came from the old one. **Your SQL is kept**, so re-running
the same statement against another database is `F2`, a name, `Enter`, `F5`. A
query still running against the previous database is cancelled and its result
discarded. Saved queries are not database-scoped and are unaffected.

`F2` rather than a `Ctrl` chord because the Query pane's editor already binds
most of them, and this is a session-level action that should work in every pane.

Results are paged client-side over rows already fetched — your SQL is never
rewritten with `LIMIT`/`OFFSET`. `DB_QUERY_TUI_PAGE_SIZE` sets the rows per page
(default 100); there is no scrolling *within* a page, so on a short terminal set
it to roughly what the Results pane can show.

### The Query editor

The Query pane is a real Neovim, embedded. db-query starts one as a child
process, draws its screen inside the pane, and forwards every keystroke it does
not reserve for itself. You get modes, motions, registers, macros, undo, and
whatever else your fingers already know.

It turns itself on wherever it can run. There is nothing to enable, no
configuration key, and no flag.

**A machine without a usable Neovim keeps exactly the pane db-query shipped
before**: a plain text area, with the keys it has always had. That fallback is
silent. Nothing is printed, nothing is logged, and nothing needs configuring.

```
db-query 1.4.0                                                 orders ● postgres · prod-core
───────────────────────┬────────────────────────────────────────────────────────────────────
 SCHEMA                │▌QUERY                                                       INSERT
 ▶ orders            5 │  1 select o.id, o.amount, c.name
 ▶ customers         3 │  2 from orders o
                       │  3 join customers c on c.id = o.cus
                       │               customer_id m bigint
                       │               customers   t 3 columns
                       ├────────────────────────────────────────────────────────────────────
                       │ RESULTS
───────────────────────┤
 SAVED                 │
───────────────────────┴────────────────────────────────────────────────────────────────────
^h/j/k/l move · ^/⌘Enter/F5 run · F2 switch db                                      F10 quit
```

The vim mode is shown at the right of the pane's label row, and the bottom bar
changes with the pane, so it always names the keys that are actually live.

#### Neovim 0.12.0 or newer

Older builds get the text area instead. The floor is 0.12 rather than 0.11
because completion as you type, without a plugin, needs Neovim's own
`'autocomplete'` option and the function-source flags of `'complete'`, and
neither exists before 0.12. Carrying a second, version-gated code path for the
sake of one minor release was not worth it, and a 0.11 user loses nothing they
had.

#### Keys in the Query pane

| Key | What it does there |
|-----|--------------------|
| `Esc` | **Neovim's.** It leaves a mode. It no longer quits db-query |
| `F10` | quit |
| `PgUp` / `PgDn` | **Neovim's.** They scroll the buffer rather than paging results |
| `Ctrl+C` | cancels a query that is running; otherwise Neovim's |
| `F5`, `Ctrl+Enter`, `⌘Enter` | run |
| `F2`, `Ctrl+h/j/k/l` | the host's, in this pane as in every other |
| everything else | Neovim's |

The other three panes are unchanged: `Esc` and `Ctrl+C` still quit there, and
`PgUp`/`PgDn` still page the results. So does the text area, when that is what
the Query pane is holding.

Two things are given up for this, both in insert mode:

- `Ctrl+H` is no longer an alias for backspace, since it moves focus left. The
  Backspace key itself is unaffected, sending `DEL` rather than `0x08`.
- `Ctrl+K` no longer starts a digraph, since it moves focus up.

#### Running part of the buffer

`F5` runs the visual selection when there is one, and the whole buffer when
there is not. Select with `v`, `V` or `Ctrl+V` and press `F5`; the selection
survives the run, because db-query takes `F5` before Neovim sees it and Neovim
therefore never leaves visual mode.

The Results pane says `selection` on its label row when only part of the buffer
was run, since nothing else on screen still says which part once the selection
is gone. An empty buffer runs nothing and says so in the status strip.

#### Completion

Completion is automatic, as you type, with no plugin installed. It is served
from the schema cache db-query already has in memory, so it costs no second
database connection and writes no credentials anywhere.

- **After a dot**, the columns of whatever the qualifier names: a table, or an
  alias declared by any `FROM` or `JOIN` in the buffer, wherever that clause
  happens to sit. Typing `c.` on line 1 finds the `c` introduced on line 12.
- **Otherwise**, the tables, the columns of the tables this query names, and SQL
  keywords. Columns of tables the statement never mentions are deliberately left
  out: the statement could not select them.

Matching is fuzzy and ignores case, so `card` finds `CardholderId`, and so does
`chid`. A table or column is offered exactly as the database spells it, since a
re-cased name is a name that does not exist; a keyword follows the case you are
typing in, so `sel` completes to `select` and `SEL` to `SELECT`. A column's type
is shown beside it.

With no cached schema, completion offers keywords alone. Run `db-query
introspect` to give it more.

#### Syntax highlighting

Stated plainly, because the gaps are real:

- **SQL keywords render bold rather than coloured.** db-query sets no colours of
  its own and ships Neovim's defaults, whose colourscheme maps `Statement`,
  `Keyword`, `Type` and the rest onto the normal foreground. This is a decision
  for this release, not an oversight. Set a colourscheme in your own `init.lua`
  (below) to change it.
- Neovim bundles no PostgreSQL and no Transact-SQL syntax file, and no
  tree-sitter SQL parser. db-query selects `sqlanywhere`, the closest of the
  dialects that are bundled, which leaves `jsonb` operators, `::` casts, `$$`
  bodies, `ILIKE` and `RETURNING` as plain text.

Nothing is mis-highlighted. There are only gaps.

#### Your own `~/.config/dbquery/init.lua`

The embedded editor runs under `NVIM_APPNAME=dbquery`, so **your real Neovim
configuration is never read**. Its configuration directory is
`$XDG_CONFIG_HOME/dbquery/`, usually `~/.config/dbquery/`, and it reads an
`init.lua` there if you have written one.

db-query never writes that file. It is yours, it is optional, and it carries
preference only. When it is absent the status strip says so once at startup and
nothing else changes.

```lua
-- ~/.config/dbquery/init.lua

-- Colour the SQL. This is the supported way to stop keywords rendering bold.
vim.cmd.colorscheme('habamax')

-- Ordinary editing preferences.
vim.o.tabstop, vim.o.shiftwidth, vim.o.expandtab = 2, 2, true
vim.o.ignorecase, vim.o.smartcase = true, true
vim.o.wrap = false

-- Mappings: anything you would put in a vimrc.
vim.keymap.set('n', '<leader>u', 'viwU', { desc = 'upper-case the word' })
vim.keymap.set('i', 'jk', '<Esc>', { desc = 'leave insert mode' })
```

db-query's own required wiring is pushed **after** your `init.lua` runs, so a
setting there cannot leave the pane unusable. Two honest limits on that:

- It beats what your configuration sets while it loads. It does not beat what
  your configuration schedules for later: an autocommand that sets
  `cmdheight = 1` on `InsertEnter` will take effect, and will cost you a row of
  the pane.
- Plugin managers, language servers and anything else that reaches the network
  or spawns processes are not what this file is for. Keep it to preference.

#### No language server

There are no diagnostics and no hover, and that is a deliberate boundary rather
than a gap waiting to be filled. Every SQL language server surveyed learns the
schema from its own live database connection, configured with credentials
written to its own file on disk. db-query resolves credentials through the
system keychain, Bitwarden or an exec helper and never writes them to disk, so
wiring one in would mean giving that up.

#### If Neovim stops

The pane cannot carry on without it, so db-query exits and prints the reason on
stderr once the screen is back. The unsaved buffer is lost: it was never a file.

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
- `nvim` 0.12.0 or newer on PATH, optional: interactive mode uses it as the
  Query pane's editor when it is there, and falls back to a plain text area when
  it is not. See [The Query editor](#the-query-editor)

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
