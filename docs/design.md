# db-query — Credential Resolution, Execution & Output Design

> Status: design locked (core) · Language: **Go** · Owner: Gerald
> Scope: personal CLI first, extended to power the `db-query` Claude Code skill

## 1. Purpose

`db-query` is a Go CLI wrapping native database clients (`psql`, `sqlcmd`) that:

1. Resolves credentials from a configured source (Bitwarden SM, Bitwarden CLI, Apple Keychain, env/`.env`) **on demand**, per invocation.
2. Injects them the way each **provider** expects (`PGUSER`/`PGPASSWORD` for Postgres; the SQL Server equivalents for `sqlcmd`) via an **environment overlay**, never argv.
3. Runs a query — ad-hoc SQL or a **named, saved query** — and returns output in a selectable format (`--output json|table|text|auto`, default `auto`).

The load-bearing idea is a **neutral credential record** between two independent seams, plus a **provider-blind central executor** at the bottom and a **parse-once / render-once** output pipeline at the top:

```
 cred URI ─▶ [resolver] ─▶ Credential ─▶ [adapter.build] ─▶ Invocation ─▶ [executor.Run] ─▶ RawResult
 (per host)  scheme picks   neutral       maps fields to      argv+env+      provider-blind    stdout/
             the backend    shape         env vars, SQL        stdin          os/exec           stderr/exit
                                          on stdin                                                  │
                                                                                                    ▼
                                        Rows (neutral) ◀─ [adapter.parse] ◀─────────────────────────┘
                                             │
                                             ▼
                                     [renderer[format]] ─▶ bytes ─▶ stdout
```

Neither seam knows the other: a resolver never knows it feeds Postgres; a renderer never knows the rows came from `sqlcmd`.

## 2. Non-goals

- Not a connection pool, ORM, or query builder.
- Not a multi-user / shared service. Single operator, local machine.
- Not making SQL portable across providers. A saved query is bound to a provider.
- Not eagerly resolving or caching secrets. Resolution is lazy and per-invocation.

## 3. The neutral credential record

Contract between the seams. Resolvers **produce** it; adapters **consume** it.

```json
{
  "username": "core_app",
  "password": "•••••••••",
  "extra": { "database": "core", "host": "core.internal", "port": 5432 }
}
```

- `username` / `password` are the only fields every adapter can assume.
- `extra` is an open bag for anything a provider *might* use. Adapters read what they know, ignore the rest.
- Resolvers need not populate `extra` — connection details usually come from host config (§5). `extra` covers cases where the secret store *is* the source of truth (a Bitwarden item bundling host+user+password).

**[LOCKED] Precedence:** when host config and a resolver's `extra` both supply a field, **explicit host config wins; `extra` fills gaps only.** Rationale: config is what you read at a glance; a surprise override buried in a secret item is hard to debug.

## 4. Credential sources (resolvers)

Dispatch is **URI-scheme → resolver** (RFC 3986 style: scheme selects the backend, the tail is that resolver's private business). Dispatch stays a dumb map; each resolver is independently testable and knows nothing about the others.

### 4.1 Scheme registry

| Scheme      | Backend                     | Example URI                                              |
|-------------|-----------------------------|---------------------------------------------------------|
| `bws:`      | Bitwarden Secret Manager    | `bws:<secret-uuid>` / `bws:<uuid>#password`             |
| `bw:`       | Bitwarden CLI (vault items) | `bw:item/<item-id>` / `bw:item/<name>`                  |
| `keychain:` | Apple Keychain              | `keychain:<service>` / `keychain:<service>/<account>`  |
| `env:`      | Environment / `.env`+direnv | `env:PGPASSWORD` / `env:DB_CORE_PW`                     |

> `#fragment` / `/path` inside a URI are resolver-specific selectors for picking a field out of a multi-field item. Each resolver defines its own; the dispatcher does not interpret them.

### 4.2 Per-resolver notes

**`bws:` (`bws secret get`)** — a BWS secret is a single value. **[LOCKED] Use two secret refs** (one user, one password) from host config rather than storing structured JSON in one secret — keeps the record shape trivial. Requires `BWS_ACCESS_TOKEN` in the environment; source that *outside* `db-query` (don't resolve it via `bws:` — chicken-and-egg).

**`bw:` (`bw get item`)** — returns full item JSON: username `.login.username`, password `.login.password`, custom fields `.fields[]`. Can populate `extra` richly. Needs an **unlocked vault** (`BW_SESSION`); handle the locked case with a clear error, not a hang (see executor timeout, §7).

**`keychain:` (`security find-generic-password`)** — `security find-generic-password -s <service> -a <account> -w` prints the password; username = the account. Machine-local secrets.

**`env:`** — reads `os.Getenv(NAME)`. With direnv the `.env` is already in the environment, so this resolver does **not** parse `.env` files itself. CLI-loaded dotenv independent of direnv would be a separate `dotenv:` scheme, not this one.

### 4.3 Resolver interface (Go)

```go
type Credential struct {
    Username string
    Password string
    Extra    map[string]string
}

type Resolver interface {
    Resolve(rest string) (Credential, error)
}

var resolvers = map[string]Resolver{
    "bws":      bwsResolver{},
    "bw":       bwResolver{},
    "keychain": keychainResolver{},
    "env":      envResolver{},
}

func Resolve(uri string) (Credential, error) {
    scheme, rest, ok := strings.Cut(uri, ":")
    if !ok {
        return Credential{}, fmt.Errorf("credential is not a URI: %q", uri)
    }
    r, ok := resolvers[scheme]
    if !ok {
        return Credential{}, fmt.Errorf("unknown credential scheme: %q", scheme)
    }
    return r.Resolve(rest)
}
```

Dispatch stays ~15 lines. Any libraries live *inside* a resolver where a real system is on the other end (Vault-style clients, OS keyring) — never in the dispatcher.

## 5. Host configuration

Host is the natural config key: provider, credential source, and connection details vary together per host. **Provider behavior** (invocation, env-var mapping, error reading) is a *separate* layer shared across all hosts of a type, so invocation logic isn't copy-pasted per host.

```toml
# ~/.config/db-query/config.toml

[hosts.prod-core]
provider   = "postgres"
host       = "core.internal"
port       = 5432
database   = "core"
username   = "bws:1a2b-user"
credential = "bws:1a2b-3c4d#password"

[hosts.reporting]
provider   = "sqlserver"
host       = "sql01.internal"
database   = "reports"
username   = "keychain:reporting-sql/svc_reports"
credential = "keychain:reporting-sql"
encrypt    = true                     # provider-specific; the sqlcmd adapter reads it
```

- `username` may be a resolver URI, a literal, or omitted if the record already carries it.
- Keys the adapter understands but the core doesn't (`encrypt`, `instance`, `sslmode`) pass through untouched.

## 6. Execution layer (central executor)

**[LOCKED]** One `Run` at the bottom; every adapter funnels through it. Adapters *build* an `Invocation`; the executor *runs* it and returns raw bytes. Two responsibilities, two places. The executor is **provider-blind**.

```go
type Invocation struct {
    Argv  []string          // Argv[0] is the client binary
    Env   map[string]string // overlay applied on top of os.Environ()
    Stdin io.Reader         // the SQL, piped — never on argv
}

type RawResult struct {
    Stdout   []byte
    Stderr   []byte
    ExitCode int
}

func Run(ctx context.Context, inv Invocation) (RawResult, error) {
    cmd := exec.CommandContext(ctx, inv.Argv[0], inv.Argv[1:]...)
    cmd.Env = mergeEnv(os.Environ(), inv.Env)
    cmd.Stdin = inv.Stdin

    var out, errb bytes.Buffer
    cmd.Stdout, cmd.Stderr = &out, &errb

    err := cmd.Run()
    res := RawResult{Stdout: out.Bytes(), Stderr: errb.Bytes()}

    var ee *exec.ExitError
    switch {
    case errors.As(err, &ee):
        res.ExitCode = ee.ExitCode()
        return res, nil // ran, returned nonzero — that's DATA, not a failure
    case err != nil:
        return res, fmt.Errorf("starting %s: %w", inv.Argv[0], err) // couldn't start
    default:
        return res, nil
    }
}
```

**[LOCKED] Locked execution decisions:**

- **"Failed to start" ≠ "ran and returned nonzero."** Binary-not-found / permission-denied → a real Go error (tool malfunction). A query exiting nonzero because a column doesn't exist → **not** a Go error; it's the signal schema-error detection consumes. `errors.As(&exec.ExitError)` is the fork. Collapsing both into `err != nil` loses the distinction the re-introspection logic depends on.
- **SQL over stdin, params over `-v`.** Keeps argv bounded (no length limit) and out of `ps`. The password rides the **env overlay**, never argv — the one genuinely secret thing stays out of `ps` entirely.
- **Env merge is explicit dedup, not `append`.** Duplicate-key resolution in the child is platform-dependent; build a map (overlay wins), flatten back, so the result is deterministic. `os.Environ()` is the base — it carries `PATH` and the direnv-loaded vars `env:` depends on.
- **Guard against inherited leakage.** Because we inherit `os.Environ()`, a stray `PGDATABASE`/`PGHOST` in the shell could silently redirect the query. Each adapter **sets every provider var it cares about explicitly**, even to defaults, so an inherited one can't leak in.
- **Buffer, don't stream (for now).** Normalization needs the whole output (stripping `sqlcmd` trailers/padding), and stderr must sit alongside stdout for error detection. Streaming is a later escape hatch tied to `--raw`, where normalization is bypassed anyway.
- **Always run under a context deadline.** `exec.CommandContext` + timeout kills wedged connections, lock waits, unreachable hosts, and locked-vault hangs. No invocation runs unbounded.

```go
func mergeEnv(base []string, overlay map[string]string) []string {
    m := make(map[string]string, len(base)+len(overlay))
    for _, kv := range base {
        if k, v, ok := strings.Cut(kv, "="); ok { m[k] = v }
    }
    for k, v := range overlay { m[k] = v } // overlay wins
    out := make([]string, 0, len(m))
    for k, v := range m { out = append(out, k+"="+v) }
    return out
}
```

## 7. Provider adapters

Round trip: **adapter builds → executor runs → adapter interprets/parses.** Everything provider-specific — SQL delivery, cred env vars, error meaning, output shape — stays quarantined in the adapter on both sides. Adding a third client is one new adapter, zero executor changes.

```go
type Adapter interface {
    Name() string
    Env(cred Credential, host HostConfig) map[string]string
    Build(host HostConfig, q RenderedQuery) Invocation // argv + stdin
    Parse(r RawResult) (Rows, error)                    // → neutral rowset
    IsSchemaError(r RawResult) bool                     // gate re-introspection
}
```

### 7.1 Postgres adapter (`psql`)

| Concern      | Handling |
|--------------|----------|
| Credentials  | `PGUSER`, `PGPASSWORD` (+ explicit `PGHOST`/`PGPORT`/`PGDATABASE` to block inherited leakage) via env overlay |
| Params       | `-v name=val`; reference `:'name'` for anything user-supplied — psql quotes it safely |
| Parse format | **`--csv`** (not the human tab output) → `encoding/csv` → `Rows`. Unambiguous, quoted, delimiter-safe |
| NULL         | CSV distinguishes: unquoted empty field = NULL, quoted `""` = empty string → faithful `*string` |
| Schema error | SQLSTATE `42703` (undefined column) / `42P01` (undefined table) in stderr |

### 7.2 SQL Server adapter (`sqlcmd`)

> Behavior is version- and implementation-dependent (legacy ODBC `mssql-tools` vs. `go-sqlcmd`). Verify flag semantics against the actual binary — some items below shift between builds.

**[LOCKED] Path A — coaxed tabular (universal default; works for arbitrary SQL):**

| Flag / step          | Why |
|----------------------|-----|
| `-b`                 | **Load-bearing.** Makes a batch error exit nonzero; without it `sqlcmd` often exits 0 on error and exit-code detection silently breaks |
| `-r1`                | Route error messages to stderr so they don't corrupt stdout rows |
| `SET NOCOUNT ON;`    | Prepended to the query — removes the `(N rows affected)` trailer deterministically |
| `-s $'\x1f'`         | ASCII **Unit Separator** as column delimiter. `sqlcmd` does **not** quote fields, so a comma/tab inside a value would mis-split; 0x1F won't appear in data |
| `-W`                 | Trim trailing whitespace |
| `-y 0` / `-Y 0`      | Defeat default column-width **truncation** (silent data loss on wide/var-length columns). Verify `0 = unlimited` on your build |
| keep headers         | Line 0 = column names; **skip line 1** (the `---- ----` rule); data follows. No separate column-name query needed |

- **NULL caveat (unavoidable on Path A) — [v1: ACCEPTED]:** tabular `sqlcmd` prints NULL as literal `NULL`, indistinguishable from the string `"NULL"`. Via this path a NULL cell is **best-effort**, not faithful. **v1 accepts this**; faithful NULL for SQL Server is a v2 patch (Path B / `FOR JSON`) applied as the tool takes shape. Costs nothing structurally: psql CSV already gives faithful NULL, and `[][]*string` stays the row type regardless — Path A just always emits a non-nil `*string`.
- Schema error: parse `Msg NNN` in stderr — `207` (invalid column), `208` (invalid object) → re-introspection.

**[LOCKED] Path B — server-side `FOR JSON` (opt-in, high-fidelity for saved queries):**

- `SELECT ... FOR JSON PATH, INCLUDE_NULL_VALUES` — server serializes, so delimiters/quoting/truncation/NULL are all handled correctly. `INCLUDE_NULL_VALUES` is required to emit `"col": null` (PATH omits null keys otherwise).
- **Gotcha:** `FOR JSON` splits the document across rows in ~2033-char chunks under a system column name — **concatenate all rows in order before parsing**, or it works on small results and breaks on large ones.
- Requires SQL Server 2016+ and wraps cleanly only around a `SELECT`. Auto-wrapping *arbitrary* user SQL is fragile → fits **authored saved queries**, not ad-hoc input.

### 7.3 Parameter binding — [LOCKED]

Saved queries are provider-bound (already locked), so **params are written in native provider syntax; there is no neutral placeholder and no rewrite layer.**

| Provider | Placeholder in stored SQL | Bound via | Quoting |
|----------|---------------------------|-----------|---------|
| psql     | `:'name'` (quoted) / `:name` (unquoted, e.g. identifiers) | `-v name=val` | psql quotes `:'name'` safely |
| sqlcmd   | `$(name)` | `-v name=val` | **none — textual macro** |

Locked rules:

- **Binding is always through the client's own `-v`.** Go **never** substitutes a value into SQL text (no `strings.Replace` into the query) — that would be hand-rolled escaping, the classic injection footgun.
- **`-v` is emitted only when the query has ≥1 param.** A zero-arg query gets no `-v` flags. Absence of params is the *only* thing that turns binding off — never a Go-side judgment about the values.
- **sqlcmd uses `$(name)`, NOT `@name`.** These are different layers: `$(name)` is sqlcmd's client-side `-v` scripting-variable expansion (what we drive); `@name` is a T-SQL engine variable that must be `DECLARE`d server-side and is **not reachable through `sqlcmd -v`**. A query saved with `@name` passes through untouched and errors as an undeclared scalar variable.
- **sqlcmd `$(name)` has no quoting** — the injection surface. v1 mitigation: light validation on sqlcmd-bound values (reject `;`, quotes, `--`, `$(`) before they reach `-v`. Proportionate to the threat model (personal tool, IDs/refs as params). The real fix — engine-level bind params via `go-mssqldb` instead of shelling to `sqlcmd` — is deliberately **out of scope for v1**, named so the ceiling is known.

## 8. Output formatting & rendering pipeline

**[LOCKED] `--output json|table|text|auto`, default `auto`.** Format branch lives at **one pivot point**, not per adapter — avoid the `providers × formats` matrix.

```
adapter parses client output → Rows (neutral) → renderer[format] → bytes
```

```go
type Rows struct {
    Columns []string
    Rows    [][]*string // *string: nil = NULL, &"" = empty string
}

type Renderer interface { Render(w io.Writer, rows Rows) error }

var renderers = map[string]Renderer{
    "text":  textRenderer{},  // tab-separated, the machine-readable shape
    "json":  jsonRenderer{},
    "table": tableRenderer{}, // aligned ASCII box, for a terminal
    // "csv" drops in later with zero pipeline change
}
```

**[LOCKED] Consequences:**

- **Always parse into neutral `Rows`.** The earlier "clean-text passthrough" shortcut does not survive `--output json`: once you parse for JSON, parsing is the always-path. Cost relocates into reliable structured parsing per client (§7).
- **NULL fidelity is `[][]*string`.** `null` vs `""` is real once JSON exists; nil pointer = NULL, `&""` = empty. psql CSV gives this cheaply. **v1: sqlcmd Path A does not distinguish** NULL from `"NULL"` (accepted; §7.2) — the row type still holds, Path A just never emits a nil. v2 restores fidelity via Path B.
- **Errors honor `--output`.** In `json` mode a failure emits **structured JSON** (`{"error": "..."}`) to stderr, not a bare string mid-stream, so `--output json | jq` never breaks on the error path. The format flag governs the whole output contract, not just the happy path.
- **Default `auto` when the flag is omitted.** `auto` resolves to `table` when stdout is a terminal and `text` otherwise. This **supersedes** the original v1 rule (*default `text`; no TTY auto-detection — an explicit default is more predictable than magic*). The concern behind that rule was **invisible** behaviour; `auto` is a named value that appears in `--help`, can be passed explicitly, and is overridable by flag or `DB_QUERY_OUTPUT`. Resolution happens once in the CLI layer, before the render pivot, so renderers stay pure functions of `(Rows, Options)`. See §13.8.

## 9. Security considerations

- **Passwords via env overlay, never argv** (argv is world-readable via `ps`). For `sqlcmd`, prefer the env-var form over `-P`; if a path must use `-P`, note the exposure.
- **Lazy, per-invocation resolution.** Resolve only the one host in play; never bulk-resolve the config. Unused hosts' vault paths are never hit; secrets don't linger resolved.
- **`sqlcmd` `$(...)` substitution is unsafe by default** — a textual macro with no quoting. Every param bound into a SQL Server saved query is untrusted input. Sharpest edge in the design; v1 mitigation and the v2 ceiling are locked in §7.3.
- **No secrets in the query cache or logs.** Saved queries store SQL with placeholders only. If logging invocations, log param *names*, never values.

## 10. Open decisions

Genuinely unresolved — not yet locked:

1. **Saved-query key strategy.** Discussed earlier, not yet written as a lock: human-owned names, agent *matches* against `--list` (not free-generates), dedupe on normalized-SQL hash so drift doesn't create near-duplicates. Pin before building the cache.
2. **Schema-cache refresh trigger.** Manual `--refresh-schema` vs. checksum-based auto-invalidation.
3. **Ad-hoc (non-saved) query CLI surface.** Positional SQL arg, `-f file`, or bare stdin. The executor takes SQL on stdin internally; the user-facing entry isn't pinned.

## 11. Locked decisions (summary)

- **Language: Go.**
- Two-seam architecture: URI-scheme resolvers → neutral `Credential` → provider adapters.
- Host-config precedence over resolver `extra`; `extra` fills gaps only.
- BWS: two secret refs, not structured-JSON-in-one-secret.
- Central provider-blind executor; adapters build/parse, executor runs.
- Fail-to-start (Go error) vs. ran-nonzero (data) distinction via `exec.ExitError`.
- SQL on stdin; params via `-v`; creds via env overlay; never argv.
- Explicit env dedup-merge; adapters set provider vars explicitly to block inherited leakage.
- Buffer output; run under a context deadline.
- psql parses via `--csv`; sqlcmd via coaxed-tabular (default) or `FOR JSON` (opt-in, high-fidelity).
- **Params: native per provider — psql `:'name'`/`:name`, sqlcmd `$(name)` (not `@name`). Always bound via client `-v`; Go never substitutes into SQL; `-v` emitted only when the query has ≥1 param.** sqlcmd value-validation as the v1 injection mitigation.
- `--output json|table|text|auto` (default `auto`, resolved by TTY in the CLI layer); parse-once into neutral `Rows`, render-once per format; errors honor the format.
- NULL fidelity via `[][]*string`. **v1 accepts sqlcmd Path A rendering NULL as literal `NULL`; faithful SQL Server NULL is a v2 patch.**
- **`--raw` passthrough is out of v1** (deferred to v2). Every query in v1 goes through the normal build → run → parse → render path.

## 12. Build order

1. Neutral `Credential` + `env:` resolver (no external backend — proves the seam).
2. Central executor + Postgres adapter + host-config loading + `text` renderer. End-to-end on the main provider.
3. `Rows` pivot + `json` renderer.
4. `keychain:` then `bw:` / `bws:` resolvers (one real backend at a time).
5. SQL Server adapter (harder client; Path A first, Path B when NULL fidelity is needed).
6. Saved-query cache + `--list`; then wire the Claude Code skill on top as a consumer.

> Build the CLI to stand alone for personal use first. The skill is just a consumer of a tool that already works without it.

## 13. Implementation notes (v1)

Refinements captured as the v1 CLI was built. These record how the locked
design lands in code; they do not alter the locks above.

### 13.1 psql NULL via a `-P null` sentinel (refines §7.1)

§7.1 assumed CSV quoting alone distinguishes NULL (unquoted empty field) from
the empty string (quoted `""`). Go's `encoding/csv` does not expose that
distinction — the reader hands back `""` for both a quoted and an unquoted
empty field, so the quoting signal is lost before the adapter can read it.
The psql adapter therefore sets `-P null=<sentinel>` (a control byte that
cannot occur in ordinary text) so NULL arrives as a distinct token the parser
maps back to a nil `*string`, while a genuine empty field stays `&""`. NULL
fidelity for Postgres is thus preserved through a sentinel rather than through
CSV quoting; the row type (`[][]*string`) and the rest of the pipeline are
unchanged.

### 13.2 Schema-cache lifecycle and file naming (resolves §10.2)

Open decision §10.2 (manual `--refresh-schema` vs. checksum auto-invalidation)
is resolved in favour of **manual refresh**. The cache is a silent side effect
of running a query:

- On `query`, the CLI computes the cache path for the resolved host+database.
  If the file is **absent** (or `--refresh-schema` is passed), it runs the
  provider introspection internally (`adapter.IntrospectSQL` built and run
  through the normal `Build`/`Env`/`executor.Run`/`Parse` path), writes the
  schema file, then runs the user query. If the file is **present** and no
  flag is given, it does not re-introspect. Either way the user query's result
  is what gets printed — the schema build is never rendered.
- If the internal introspection itself fails, the error is surfaced and the
  user query does **not** run.
- `introspect` always rebuilds the cache and prints the schema. `--refresh-schema`
  is accepted on both commands and is the **only** trigger that rebuilds the cache.

File location and naming (`internal/schema`):

- Directory: `$XDG_CACHE_HOME/db-query/schema/`, fallback `~/.cache/db-query/schema/`.
- Filename: `<sanitized-host>_<sanitized-db>-<8hexhash>.json`, where the hash is
  the first 8 hex characters of `sha256(host + NUL + database)`. The hash is the
  uniqueness guarantee — distinct host/database pairs never share a file even
  when sanitisation or a case-folding filesystem would collapse the readable
  parts, and the NUL separator stops a boundary shift (`ab`/`c` vs `a`/`bc`)
  from hashing alike. The sanitized parts are for human legibility only.
- Persisted as JSON; NULL versus empty string round-trips faithfully as JSON
  `null` versus `""`. Schema metadata only — never credentials.

### 13.3 Exit codes 3 and 4 (extends §6)

The fail-to-start vs. ran-nonzero distinction locked in §6 is surfaced to the
process exit code, and a ran-nonzero result is split by whether it is a schema
error:

| Code | Meaning |
|------|---------|
| `0`  | success |
| `1`  | config / usage / credential error |
| `2`  | client binary failed to start (the Go error from `executor.Run`) |
| `3`  | **schema error** — `adapter.IsSchemaError(res)` is true; the re-introspect-worthy signal |
| `4`  | other SQL error — the client ran and exited nonzero but is not a schema error |

On code `3` the CLI does **not** auto-reintrospect; it prints a hint to re-run
with `--refresh-schema`. Keeping the rebuild behind the explicit flag (and off
the error path) preserves the single, predictable cache-refresh trigger from
§13.2.

### 13.4 `--no-headers` (extends §8)

`--no-headers` (query only) applies at the single render pivot (§8): in **text**
output it omits the header line and tab-separates rows for any shape, so a 1×1
result prints just the bare value. In **json** output it is a no-op — the objects
are already self-describing. It is carried as a field on `render.Options` and
read only inside the text renderer, so no adapter learns about it — consistent
with the "format branch lives at one pivot point" lock.

### 13.5 Saved-query key strategy (resolves §10.1)

Open decision §10.1 (saved-query key strategy) is resolved as follows:

- **Caller-supplied names, not generated.** A saved query is keyed on a
  human-owned `<category>/<name>` the caller chooses (`--save <name>`,
  `--category <cat>`, default category `default`). Names and categories are
  sanitised to safe path segments; an empty name and any segment containing
  `/` or `..` are rejected, so a stored query can never escape the store. An
  agent **matches** an intent against the existing set (`db-query list`,
  which emits `{category, name, provider, sqlhash, sql}` in JSON) rather than
  free-generating SQL.
- **Global normalised-hash dedup.** Save computes a canonical hash — comments
  and whitespace stripped, trailing semicolons dropped, case preserved (quoted
  identifiers can be case-sensitive) — and, unless `--force`, refuses if **any**
  stored query anywhere in the store already holds that hash, naming it. So
  drift in layout or comments does not spawn near-duplicates. A second guard
  refuses when the target `<category>/<name>` file already exists.
- **`--force` overrides both guards** and overwrites in place.
- **Save on success only.** `--save` persists after the query has run and
  exited 0; the SQL stored is the query text with placeholders, never the
  resolved `--param` values (§9). A non-zero run saves nothing. A save refusal
  is a usage error (exit `1`), but the run already happened and its output was
  printed — the refusal is surfaced on stderr honouring `--output`.
- **Provider binding.** A saved query records the provider it was saved
  against; `--source` refuses to run it against a host of a different provider,
  since params and SQL are provider-native (§7.3) with no rewrite layer.

The store mirrors the schema cache's shape (§13.2): an XDG-aware directory
(`$DB_QUERY_QUERIES_DIR`, else `$XDG_CONFIG_HOME/db-query/queries`, else
`~/.config/db-query/queries`), one `<category>/<name>.sql` file per query
carrying a reserved `-- db-query:key=value` header above the raw SQL body, and
metadata only — never credentials.

### 13.6 `[bws].accessToken` (token source override)

The BWS access token source is configurable via a `[bws]` section holding a
resolver URI, resolved lazily only when a host uses `bws:`. Pointer-only (raw
refused), `bws:` refused (chicken-and-egg), and it falls back to
`BWS_ACCESS_TOKEN` when unset. This is the first instance of per-resolver
backend config; the token itself gets the same lazy, pointer-based treatment
as every other credential.

### 13.7 Terminal ergonomics: flag position, environment defaults, shorthands

Manual use is dominated by one loop: look at the schema, then run the query it
informs, against the same host and database. Three changes make that loop cheap
to retype — or rather, cheap to *not* retype — without adding a mode or a
stateful "current connection":

- **Shared flags before the command.** `--host`, `--database`, `--config`,
  `--output` and `--timeout` may precede the command (`db-query --host test
  --database testdb schema`), which keeps the varying tail at the end of the
  line so shell history can be edited instead of retyped. Implementation is a
  pre-pass `FlagSet` registering only the shared flags: Go's flag package stops
  at the first non-flag token, which *is* the command name. The parsed values
  become the subcommand's flag **defaults**, so the same flag after the command
  overrides one before it for free. Only those five are accepted there — a
  command's own flag before the command stays an error, so `--save` never
  becomes global for every command.
- **`DB_QUERY_HOST` / `DB_QUERY_DATABASE`.** The same two values as environment
  defaults, for a shell pinned to one database. Named for the existing
  `DB_QUERY_*` family (`DB_QUERY_CONFIG`, `DB_QUERY_QUERIES_DIR`) rather than a
  second, shorter convention. Precedence: flag → environment → config file,
  which is the §3 "explicit wins, the ambient fills gaps" rule one layer out.
- **Command shorthands.** `q`/`s`/`i`/`ls`/`l` for query/schema/introspect/list,
  as an explicit alias map — not prefix matching, which would make `l`
  ambiguous the moment a second l-command lands and lets a new command silently
  steal an established shorthand. `queries` was renamed `list` in the same pass
  (no back-compat alias; the unknown-command path prints the usage).

`hosts` deliberately has no shorthand: `h` reads as help, which `-h`/`--help`
already owns.

### 13.8 `auto` output and TTY detection (amends §8)

§8 originally locked *default `text`; no TTY auto-detection — an explicit
default is more predictable than magic*. That rule is **amended**: the default
is now `auto`, which resolves to `table` when stdout is a terminal and `text`
otherwise.

The original concern was **invisible** behaviour, not terminal detection as
such. `auto` is not invisible: it is a named value listed in `--help` and in
shell completion, it can be passed explicitly (`--output auto`), and it is
overridable three ways — `--output` before the command, `--output` after it, or
`DB_QUERY_OUTPUT` for a whole shell. What the amendment buys is that the two
audiences stop fighting over one default. A person at a prompt wants aligned
columns; a pipeline, a redirect, or an agent shelling out wants the stable
tab-separated shape. Detection lets each get it without configuration.

**The compatibility guarantee.** Whenever stdout is not a terminal, output is
byte-identical to the pre-`auto` `text` renderer. Nothing that consumed
`db-query` through a pipe sees any change.

Consequences:

- **`text` is unchanged and remains the machine format.** The table is a
  *new* renderer, not a reshaping of `text` — `cut -f` and `awk -F'\t'`
  keep working, and the exact-byte tests that pin `text` keep passing.
- **Resolution happens in the CLI layer, not in a renderer.** `renderRows` is
  the one point every command's rows converge on and it already holds the
  destination writer, so the probe runs once there. Renderers stay pure
  functions of `(Rows, Options)` and never inspect what they write to.
- **`render.For` rejects `auto` by design; `render.Valid` accepts it.** Flag
  validation uses `Valid`, rendering uses `For`. A renderer lookup that
  silently accepted `auto` would let a caller render without ever probing.
- **The probe is a type assertion, not a dependency.** `w.(*os.File)` plus
  `ModeCharDevice`; no build tags, no `isatty`. It also makes the piped path
  the default under test — a `bytes.Buffer` is not an `*os.File`, so the whole
  existing suite exercises `text` without modification.
- **`--no-headers` extends to `table`** (§13.4): it drops the header row and
  the row-count footer, leaving the framed data.
- **Every row-printing command honours the format** — `query`, `list`,
  `schema`, `introspect`, `hosts` — because they all render through the pivot.
  Note `list` keeps a JSON branch outside the pivot; `auto` is resolved before
  it, so `table` falls through into the normal `Rows` path.

**The table renderer takes the first non-stdlib rendering dependency**,
`github.com/jedib0t/go-pretty/v6` (4 modules compiled in). It was chosen over
`lipgloss` (13 modules, and it runs its own TTY/colour detection through
package globals — a second, invisible probe under this one) and over
`tablewriter` (12 modules, and its defaults rewrite headers destructively:
`user_name` renders as `USER NAME`). The stdlib `text/tabwriter` cannot be used
because it measures with `utf8.RuneCountInString`, so any double-width glyph
mis-pads every following column.

One library default must stay pinned: go-pretty upper-cases headers unless
`Style().Format.Header` is set to `text.FormatDefault`. A SQL identifier may be
a case-sensitive quoted name, so it has to survive verbatim; a test asserts a
`user_name` column renders unchanged.

Table rendering is display-oriented and lossy by design, which is safe because
`json` remains the fidelity path: NULL prints as a visible `NULL` (distinct
from the empty string), control characters collapse to spaces so an embedded
newline cannot break the row framing, and `--max-col-width` (default 50,
`0` = unlimited) truncates over-wide cells with `…`.
