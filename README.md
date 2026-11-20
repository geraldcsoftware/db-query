# db-query

A Go CLI that runs SQL against PostgreSQL and SQL Server, resolves
credentials on demand from a configured source, and returns query output
in a selectable format — an aligned table at a terminal, tab-separated text
when piped. Both databases are reached over their own wire protocols, so no
client binary is needed. See `docs/design.md` for the full design.

```
 cred URI ─▶ [resolver] ─▶ Credential ─▶ [adapter.Execute] ─▶ []Rows (one per result set) ─▶ [renderer[format]] ─▶ stdout
                                              │
                    postgres: pgx, wire protocol · sqlserver: go-mssqldb, TDS
```

## Usage

```sh
db-query query      --host prod-core "SELECT id, name FROM people WHERE name = $1" --param 1=Ada
db-query query      --host reporting --output json -f report.sql
db-query query      --host prod-core --save people-by-name --category reports \
                    "SELECT id, name FROM people WHERE name = $1" --param 1=Ada
db-query query      --host prod-core --source people-by-name --category reports --param 1=Ada
db-query query      --host prod-core -f migrate-and-check.sql  # several statements, one transaction
db-query query      --host prod-core --dry-run "DELETE FROM people"   # classify it, run nothing
db-query            --host prod-core "SELECT id, name FROM people"   # at a terminal, query may be left out
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
  argument (`… "SELECT …" --param 1=Ada` and `… --param 1=Ada "SELECT …"` are equivalent);
  SQL that itself begins with `-` must be passed via `-f` or stdin.
- Most flags have a single-letter shorthand: `--host (-H)`, `--database (-d)`,
  `--config (-c)`, `--output (-o)`, `--param (-p)`, `--file (-f)`, `--source (-s)`,
  `--category (-C)`, `--timeout (-t)`, `--help (-h)`, `--version (-v)`. Deliberate
  actions (`--save`, `--force`, `--refresh-schema`, `--no-headers`,
  `--max-col-width`, `--border`, `--dry-run`, `--precheck`, `--show-sql`) are
  long-only.
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
there; a command's own flags (`--tables`, `--param`, `--dry-run`, …) belong
after the command and error out before it, reporting the flag by name:

```sh
db-query --host test --dry-run q "SELECT 1"   # flag provided but not defined: -dry-run
db-query --host test q --dry-run "SELECT 1"   # correct
```

At a terminal the SQL can also stand in the command position, with `query`
left out; see "Without the query command" below.

### Without the query command

At a terminal, `query` can be left out. Quote the SQL as one argument, or send
it on stdin:

```sh
db-query --host test "SELECT * FROM todos;"
db-query --host test < report.sql
```

A single argument that contains whitespace is taken as SQL, because the shell
strips the quotes and the space is the only trace of them left. A bare word
never contains one, so it stays a command name: `db-query scehma` fails with
`unknown command "scehma" (did you mean "schema"?)` instead of sending `scehma`
to the server. Unquoted SQL (`db-query select * from t`) is therefore an
unknown command too, and the error says to pass it as one quoted argument.

Both forms apply only when stdout is a terminal. Anything that captures or
redirects the output (`| jq`, `> out.tsv`, `$(db-query ...)`, an agent, cron,
CI) needs the explicit `query` command. There, a quoted SQL argument fails with
`unknown command "SELECT 1"; outside a terminal, run SQL through the query
command: db-query query "<SQL>"`, and SQL on stdin with no command prints usage
and exits 1. With nothing on stdin either, `db-query` with no command prints
usage on stderr and exits 1, at a terminal or not.

Query flags go after the SQL, as they do after `query`:

```sh
db-query --host test 'SELECT * FROM todos WHERE id = $1' --param 1=7 --dry-run
db-query --host test -f report.sql   # flag provided but not defined: -f
db-query --host test query -f report.sql
```

Only the five shared flags may come before the SQL.

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

`DB_QUERY_DRY_RUN` set to anything other than `0` forces `--dry-run` for every
invocation, which is how a wrapper that cannot add a flag screens what it is
about to run. See [Query safety screening](#query-safety-screening).

Precedence is flag (either position) → environment → the
[remembered host and database](#remembered-host-and-database), on a terminal →
config file. Note that an
exported host is invisible state: `db-query hosts` shows what is configured, but
the host in effect is whatever `DB_QUERY_HOST` says until you override it.
- Params are values, never SQL text. On PostgreSQL they are positional and
  bound by the server: write `$1`, `$2` in the SQL and pass `--param 1=Ada
  --param 2=42`. The server infers each one's type from where it is used, so
  `WHERE id = $1` takes `--param 1=42` as an integer. On SQL Server they are
  named and also bound by the server: write `@name` in the SQL and pass
  `--param name=value`. See [Parameters](#parameters).
- A submission may hold several statements, and every result set is printed.
  On PostgreSQL they run in order inside one transaction, and the first error
  stops the run and rolls it back. On SQL Server they run batch by batch, split
  at `GO`, and the first error stops the run. See
  [Scripts](#scripts-several-statements).
- `--output json|table|text|auto` (default `auto`). `auto` renders a bordered
  table when stdout is a terminal and tab-separated `text` otherwise, so
  results are readable by eye while a pipe, a redirect, or a program calling
  `db-query` still gets the stable machine-readable shape. Pass `--output`
  explicitly to force either one, or export `DB_QUERY_OUTPUT` to pin it for a
  whole shell. Every command that prints rows — `query`, `list`, `schema`,
  `introspect`, `hosts` — honours the setting. In `json` mode errors are
  emitted as structured JSON on stderr.
- In `json`, numeric columns are JSON numbers and boolean columns are `true`
  or `false`; everything else, dates and times included, is a string, and
  NULL is `null`. A number keeps the server's digits exactly, so a `bigint`
  or a `numeric(38,10)` is not rounded, but a consumer that reads numbers as
  doubles (JavaScript, `jq` before 1.7) rounds beyond 2^53. A value the type
  cannot hold as JSON, such as a float `NaN`, stays a string. `text` and
  `table` output are unchanged: they print the server's own text (`t`/`f` for
  a postgres boolean, `1`/`0` for a SQL Server `bit`).

  | Provider | JSON number | JSON boolean |
  |---|---|---|
  | PostgreSQL | `smallint`, `integer`, `bigint`, `oid`, `real`, `double precision`, `numeric` | `boolean` |
  | SQL Server | `tinyint`, `smallint`, `int`, `bigint`, `real`, `float`, `decimal`/`numeric`, `money`, `smallmoney` | `bit` |
- `--stats` (`query`) prints a summary of the run on stderr once the rows are
  out: rows returned, rows affected and how long the server took, for example
  `db-query: 12 rows returned, 3 rows affected (41ms)`. It is on by default
  when stderr is a terminal and off otherwise, so a script that captures
  stderr sees nothing new on success; pass `--stats` or `--stats=false` to
  choose. Under `--output json` the summary is one JSON line,
  `{"stats": {...}}`, with `result_sets`, `rows_returned`, `rows_affected`,
  `elapsed_ms` and a `statements` array holding the server's report on each
  statement (`tag` is the PostgreSQL command tag, such as `INSERT 0 3`, and
  `rows` is `null` where the server gave no count). Stdout is unchanged.
  `rows_affected` is `null`, and the prose says `ok`, when no statement gave a
  count: a DDL statement has none, and SQL Server sends none under
  `SET NOCOUNT ON`. On SQL Server an `INSERT`, `UPDATE` or `DELETE` with an
  `OUTPUT` clause counts as rows returned, because the server does not say
  which statement a count belongs to; PostgreSQL's `RETURNING` counts as both.
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

  Export `DB_QUERY_BORDER` to pin the frame for a whole shell. It applies to
  every command that prints a table, including `list`, `introspect`, `hosts`,
  and `databases`, and `--border` beats it.
- `--database <db>` (`-d`) overrides the host's configured `database` for this
  run (on `query`, `schema`, `introspect`, and `databases`), so one host entry
  can reach sibling databases on the same server without a second config block.
  Its value completes from the cached database list — see
  [Shell completion](#shell-completion-zsh).
- `--no-headers` omits the header line. In `text` it tab-separates the rows for
  any shape, so a 1×1 result prints just the bare value; in `table` it drops the
  header row and the row-count footer, leaving only the framed data. It is a
  no-op for `--output json`, whose objects are already self-describing.

### Remembered host and database

On a terminal, `query` (its short form and SQL piped into it included),
`schema`, `introspect` and `databases` remember the `--host` and `--database`
given to them as flags, once the run has reached them. A later run on a
terminal, under the same config file, fills in what it does not name, and says
so on stderr:

```sh
db-query -H testhost -d testdb "select 1"
db-query "select count(*) from todos"   # db-query: testhost / testdb (remembered)
db-query s --tables                     # db-query: testhost / testdb (remembered)
```

The host is `--host`, then `DB_QUERY_HOST`, then the remembered host. The
database is `--database`, then `DB_QUERY_DATABASE`, then the remembered
database, then the host's configured `database`. A remembered database belongs
to the host it was remembered with: it is used only while the host in use is
the remembered one, and is never carried to another host. The pair is tied to
the config file in use as well: a run under another `--config` or
`DB_QUERY_CONFIG` does not see it, because the same host name there can be
another server.

| Remembered | Run | Uses | Remembered afterwards |
|---|---|---|---|
| testhost / testdb | `-H testhost "..."` | testhost / testdb | unchanged |
| testhost / testdb | `-H other "..."` | other / its configured database | other, no database |
| testhost / testdb | `-H other -d x "..."` | other / x | other / x |
| testhost / testdb | `-d y "..."` | testhost / y | testhost / y |
| testhost / testdb | `"..."` with `DB_QUERY_HOST=other` exported | other / its configured database | unchanged |
| nothing | `-H a "..."` | a / its configured database | a, no database |

A run has reached its target when its connection opened, or when it succeeded
without one (a schema answered from its cache). A query whose SQL fails still
counts; a mistyped host or database never does.

Nothing reads or writes the remembered target when stdout is piped or
redirected (so a script, an agent or `| less` still needs `--host`), or in a
`--dry-run` or `--precheck` run, `DB_QUERY_DRY_RUN` and `DB_QUERY_PRECHECK`
included. Exported values are never remembered. Shell completion reads the
remembered host but never writes it.

It is kept in `$XDG_CACHE_HOME/db-query/target/last.json` (else
`~/.cache/db-query/target/last.json`), readable only by you. A terminal run
that reaches its target with a flagged host or database updates it, as the
table shows; delete the file to forget it. A remembered database that has since
been dropped fails to connect, and a run that flags `-d` (or another `-H`)
replaces it.

### Query safety screening

Every submission is classified before it runs. On PostgreSQL the classification
comes from PostgreSQL's own grammar, so a statement is judged by the parse tree
it actually produces rather than by pattern matching over its text; SQL Server
goes through the planner instead. A submission the mechanism cannot understand
is reported as `opaque`, which is treated as the least safe class rather than
the safest.

| Class | Means | Example |
|---|---|---|
| `read` | returns rows and changes nothing | `SELECT`, `WITH … SELECT`, `EXPLAIN` |
| `write` | changes rows and nothing else | `INSERT`, `UPDATE`, `MERGE`, `COPY`, `REFRESH MATERIALIZED VIEW` |
| `destructive` | removes data, or changes the shape of the database | `DELETE`, `DROP`, `TRUNCATE`, `CREATE TABLE`, `CREATE INDEX`, `CREATE VIEW`, `ALTER TABLE`, `ALTER … RENAME`, `SELECT … INTO` |
| `admin` | changes the server or its accounts | `CREATE ROLE`, `GRANT` |
| `opaque` | no class could be established | a syntax error, or a statement type the mapping does not recognise, such as `VACUUM` |

Three of those cut against the usual reading and are worth knowing.

`write` means data and nothing else. **Every schema change is `destructive`**,
including ones whose verb sounds harmless: `CREATE INDEX` meets the same
challenge as `DROP TABLE`. The line is drawn at data versus schema rather than
at how lossy a particular statement looks, because the lossiness is not visible
at the point of classification: `ALTER TABLE … DROP COLUMN` and `… ADD COLUMN`
arrive as the same node, so splitting them would mean enumerating every
subcommand PostgreSQL has and misclassifying whichever ones a later release
adds. The cost is a challenge on a harmless `CREATE INDEX`; the benefit is that
a column and every value in it cannot go unattended.

`DELETE` is `destructive`, not `write`, whether or not it names a `WHERE`
clause, so it meets a human even on a writable host.

`SELECT … INTO` is `destructive` despite reading like a query: it creates and
populates a table, and is caught through its `IntoClause` rather than its
statement node, which is a bare `SelectStmt`.

Classification is deny-by-default: a statement type the mapping does not list is
`opaque` rather than assumed harmless, which is why an unremarkable `VACUUM` is
challenged. That is also why the lists stay short — unenumerated DDL fails
closed on its own.

A submission is exactly as safe as its least safe statement, so
`SELECT 1; DROP TABLE t` classifies `destructive` as a whole. The document names
the first statement at the deciding class, so a refusal says which statement of
twelve caused it.

What a class is allowed to do depends on the host's posture, set by the
`readonly` key, which **defaults to true** (see [Configuration](#configuration)):

| Class | `readonly = true` (default) | `readonly = false` |
|---|---|---|
| `read` | runs | runs |
| `write` | operator challenge | runs |
| `destructive`, `admin`, `opaque` | operator challenge | operator challenge |

A challenge prints the target and the deciding statement on `/dev/tty` and asks
for a short nonce to be typed back. It opens the terminal directly rather than
testing whether stdout is one, so it still reaches you when SQL arrives on stdin
or output is piped to `jq`. Anything other than the nonce cancels, and a closed
terminal is a decline rather than an approval.

On PostgreSQL a read-only host also asks the engine to refuse writes, by
opening the session with `default_transaction_read_only=on`. That catches what no text scan
can see, such as a `DROP` built inside a `DO` block whose submission reads as a
harmless call. Treat it as a second layer rather than the guarantee: it is a
default rather than a constraint, and a submission can turn it off with
`set_config`. Database grants remain the actual control.

Client directives such as `\copy` are refused outright rather than challenged.
They are commands to `psql` or `sqlcmd`, not SQL, and never reach a server, so
no grammar or planner can see them, and no operator approval applies.

#### Screening without running: `--dry-run`

`--dry-run` classifies a query, prints the decision as JSON, and runs nothing.
On PostgreSQL it opens no connection, so it works against a host that is
unreachable, though the host's credential must still resolve. On SQL Server it
connects and asks the planner for each statement's plan under
`SET SHOWPLAN_XML ON`, which compiles without executing. Bound parameters are
declared for the planner with the type they will be sent as, so a
parameterised statement is classified as it will run.

```sh
db-query -H prod-core -d core q "SELECT id FROM people" --dry-run
```

```json
{
  "schema_version": 1,
  "status": "classified",
  "target":   { "provider": "postgres", "host": "prod-core", "database": "core" },
  "readonly": { "configured": true, "probe": "skipped", "engine_enforced": true },
  "classification": {
    "class": "read",
    "mechanism": "parser",
    "decided_by_version": "pg_query_go/v6 (PostgreSQL 17 grammar)",
    "statements": [ { "index": 1, "class": "read", "decided_by": "SelectStmt" } ]
  },
  "params": { "names": [], "count": 0 },
  "digest": { "alg": "hmac-sha256", "value": "bc2cea36…" },
  "precheck_token": "bc2cea36…",
  "decision": { "action": "allow", "reason_code": "OK_READ", "reason": "permitted on this host: read" }
}
```

The field names and the reason codes are an API, not a message for a person, so
a hook can branch on them. `schema_version` is the contract: a consumer that
does not recognise the version must refuse rather than proceed, because a
renamed field would otherwise read as an empty decision and quietly allow.
`status` separates a verdict from the absence of one, so "could not classify"
never reads as "clean". `precheck_token` is explicitly `null` rather than
omitted when no token was minted, since a missing key cannot be told from a
truncated document.

A dry run reports; it does not refuse. `db-query q "DROP TABLE people"
--dry-run` exits `0` with `"action": "challenge"` and a null token, because the
caller asked for the document and the document is what they get.

| `action` | Meaning |
|---|---|
| `allow` | permitted on this host; a token is minted |
| `challenge` | may run only after an operator answers at the terminal |
| `block` | refused outright; no operator may wave it through |

Reason codes: `OK_READ`, `CLASS_WRITE`, `CLASS_DESTRUCTIVE`, `CLASS_ADMIN`,
`CLASS_OPAQUE`, `CLIENT_DIRECTIVE`, `PARAM_UNSAFE`, `READONLY_PROBE_FAILED`,
`PARSER_UNAVAILABLE`, `PRECHECK_INCOMPLETE`, `DIGEST_MISMATCH`. They are
append-only and never repurposed; treat an unknown one as a refusal.

#### Reusing a verdict: `--precheck`

A clean read mints a `precheck_token`, an HMAC over the invocation: the SQL, the
parameter values, and the target. `--precheck <token>` presents it so an
approved query runs without being classified twice.

```sh
tok=$(db-query -H prod-core q "SELECT id FROM people" --dry-run | jq -r .precheck_token)
db-query -H prod-core q "SELECT id FROM people" --precheck "$tok"
```

Because the token covers the whole invocation and not just the text, changing
the SQL, a parameter value, or the target invalidates it, and the invocation
exits `6` rather than running something that was never checked. A token is
minted only for a verdict that classified clean, so presenting one alongside a
`challenge` does not answer the challenge.

#### Inspecting the statement: `--show-sql`

`--dry-run` omits the SQL by default, because literals written into a statement
(an account number in a `WHERE` clause) would otherwise reach the document and
any audit record built from it. `--show-sql` adds a `sql` field when the
statement itself needs inspecting. Parameter **values** are never included under
any flag: the document carries only their names and a count.

#### Parameters

On PostgreSQL, parameters are the server's own: `$1`, `$2` in the SQL, bound by
position with `--param 1=value`. The value travels separately from the
statement and is never spliced into its text, so there is nothing to quote and
nothing to inject: `--param "1=x'; DROP TABLE people; --"` is compared as a
string and matches nothing. Because they are part of PostgreSQL's grammar, a
parameterised statement is classified exactly as written.

Two mistakes are refused before anything is sent, as usage errors (exit `1`):

```
db-query: --param "who": postgres parameters are positional; write $1, $2 in the SQL and bind them with --param 1=value
db-query: $2 is not bound; pass --param 2=value
```

The first catches SQL written for `psql`'s `:'name'` interpolation, which this
tool no longer uses. A parameter can stand only where a value can: a table or
column name cannot be a parameter.

On SQL Server, parameters are named: `@name` in the SQL, bound with
`--param name=value`. They are bound by the server too, through
`sp_executesql`, so a value is never spliced into the statement and any value
is accepted, quotes and semicolons included:

```sh
db-query query --host reporting --param who=Grace \
  "SELECT id FROM people WHERE name = @who"
```

- A value of plain ASCII is sent as `varchar`, any other as `nvarchar`, sized to
  the value. `varchar` keeps an index seek on a `varchar` column, which an
  `nvarchar` parameter would turn into a scan. SQL Server converts the value
  where the statement compares it with another type, so `WHERE id = @min`
  against an `int` column works as written. A few places take no implicit
  conversion, such as `TOP` and `OFFSET`: write `TOP (CAST(@n AS int))`.
- A batch that uses a parameter is run through `sp_executesql`, which scopes
  what it creates: a `#temporary` table, a `SET` or a `USE` in that batch is
  gone when it ends. Create the table in a batch of its own, without
  parameters, and fill it in the next.
- Names match case-insensitively, as T-SQL variable names do under the default
  collations, and a name may not start with a digit. Pass the name without its
  `@`; `--param @who=Grace` is refused with a pointer to `--param who=Grace`.
- An `@name` with no `--param` is left alone, because it may be a local
  variable the batch declares for itself. If it is not, the server reports
  `Must declare the scalar variable "@name"`.
- A value bound but never referenced is ignored, as it is on PostgreSQL.
- The pre-check plans each statement on its own, so a statement that reads a
  variable declared by an earlier statement (`DECLARE @n int = 1; SELECT …
  WHERE id = @n`) does not compile there and is refused as `opaque`. Bind the
  value with `--param` instead.

Queries written for the old `sqlcmd` `$(name)` substitution must be rewritten
with `@name`. Nothing substitutes `$(name)` any more, so inside quotes it is an
ordinary string and outside them a syntax error.

### Scripts: several statements

A PostgreSQL submission holding more than one statement runs as a script:

- The statements run in order **inside one transaction**. If every one
  succeeds the transaction commits; the first one to fail stops the run, nothing
  after it is sent, and the whole script is rolled back.
- The error names the failing statement, e.g. `statement 2 of 3: ERROR: column
  "nope" does not exist (SQLSTATE 42703)`, and the exit code is that statement's
  (`3` or `4`). A failed script prints no results.
- **Every result set is printed**, one per statement that returns rows
  (an `INSERT` or `UPDATE` without `RETURNING` contributes none). `text` and
  `table` output separate them with a blank line; `json` wraps them in an outer
  array, one array per result set. A single result set prints exactly as before.
- Parameters are shared: `$1` is the same value in every statement that uses it.
- A single statement runs on its own, outside any explicit transaction, so
  statements PostgreSQL refuses inside one (`VACUUM`, `CREATE DATABASE`,
  `CREATE INDEX CONCURRENTLY`) work only when submitted alone.

A SQL Server submission runs the way `sqlcmd` ran it, batch by batch:

- `GO` alone on a line separates batches, and `GO 3` runs the batch before it
  three times. Everything between two `GO` lines is sent as one batch, so its
  statements share their variables.
- All batches run on one connection, so a temporary table or a `SET` made in
  one batch is still there in the next, unless that batch uses a parameter
  (see [Parameters](#parameters)).
- There is **no implicit transaction**. The first batch to fail stops the run
  and nothing after it is sent, but what earlier batches did stays done. Wrap
  the script in `BEGIN TRANSACTION` … `COMMIT` with `SET XACT_ABORT ON` if it
  must apply whole or not at all.
- The error names the failing batch, e.g. `batch 2 of 3: Msg 208, Level 16,
  State 1, Line 1: Invalid object name 'ghosts'.`, and a failed script prints
  no results.
- Every result set is printed, as on PostgreSQL, and `NULL` is a real null: a
  JSON `null`, and `NULL` in table output.

The safety gate judges the script as a whole by its least safe statement, as
it always has, before any of it is sent.

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
| `2`  | the run could not happen: `--timeout` expired |
| `3`  | schema error — the query references an unknown table or column (re-run with `--refresh-schema`) |
| `4`  | other SQL error, or the server could not be reached |
| `5`  | refused by [safety screening](#query-safety-screening), or declined at the operator challenge |
| `6`  | a `--precheck` token was presented for a different query, parameters, or target |

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
# Provider-specific keys pass through to the adapter. For SQL Server:
#   encrypt                = "true" (default) | "strict" | "false" | "disable"
#   trustservercertificate = true   # skip certificate verification (self-signed servers)
#   hostnameincertificate  = "sql01.corp"  # the name to verify the certificate against
#   instance               = "SQLEXPRESS"  # a named instance, found through SQL Browser

[hosts.scratch]
provider   = "postgres"
host       = "scratch.internal"
database   = "scratch"
credential = "env:DB_SCRATCH_PW"
readonly   = false                 # allow writes here without a challenge
```

`readonly` **defaults to true**, so a host that never mentions it permits reads
and sends everything else to an operator challenge. Set it to `false` on a host
where writes are ordinary, such as a development database: that declaration is
the operator saying so once, rather than answering a challenge for every
`INSERT`. It buys row changes only. Schema changes, destructive statements and
administrative ones still meet a human on either kind of host, on the reasoning
that accepting unattended `INSERT`s is not the same as accepting unattended DDL. See [Query safety screening](#query-safety-screening) for what each
class is allowed to do, and note that on PostgreSQL a read-only host is enforced
by the engine and not only checked by this tool. The key takes a real boolean:
`readonly = "yes"` is rejected at load with the offending section named, rather
than being read as false.

Credential schemes: `env:VAR`, `bws:<secret-id>[#field]` (Bitwarden
Secrets Manager, needs `BWS_ACCESS_TOKEN`), `bw:item/<id-or-name>`
(Bitwarden CLI, needs `BW_SESSION`), `keychain:<service>[/<account>]`
(macOS). Resolution is lazy and per-invocation, and a password goes straight
to the server: no child process, environment variable or argv ever carries
it.

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
so it needs the host to be known: given as `--host`/`-H` anywhere on the line,
exported as `DB_QUERY_HOST`, or
[remembered](#remembered-host-and-database) from an earlier run on a terminal:

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

- No database client: both providers are reached over their own protocols.

## Development

```sh
make build          # build bin/db-query
make install        # build, then copy the binary to ~/.local/bin (created if absent)
make test           # unit tests
make cover          # unit tests + coverage summary
make integration    # docker compose up, run the postgres + sqlserver suites, tear down
```

Integration tests need Docker and no database client binary. Image
refs are overridable when pulling through a mirror:

```sh
DBQ_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:16-alpine make integration
```
