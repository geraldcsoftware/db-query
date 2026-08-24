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

### 5.1 Shared configuration (profiles)

Hosts cluster: several boxes in one environment differ only by address while sharing provider, database, user, and credential. A `[profiles.<name>]` section holds what they share; a host claims it with `inherit = "<name>"`. Profiles may inherit profiles, so a base profile can carry the provider while narrower ones carry per-group credentials.

**[LOCKED] Precedence, extending §3:** explicit host key → nearest profile in the chain → resolver `extra`. Inheritance sits in the middle because inherited config is still config, just less specific than what is written on the host itself; `extra` remains the last resort it was.

**[LOCKED] Merge before interpretation.** The inherit chain is flattened as raw TOML keys, and only the merged map is then interpreted. Two consequences, both load-bearing:

- An inherited key gets exactly the same validation as a literal one — port parsing, the `password`-typo trap, adapter passthrough — with no second code path to keep in step.
- There is no need to distinguish "unset" from "explicitly set to the zero value", which merging typed `HostConfig` structs would have forced for every field.

Each merged key carries the section that supplied it (`host lionel`, `profile eus`). Origins serve error messages, which must blame the section that actually holds the mistake, and `hosts <name>`, which reports the effective config.

**Profiles are not connectable.** They live in their own map, so they cannot appear in `hosts`, in `--host` completion, or as a query target; naming one as a host produces a distinct error rather than "unknown host". `inherit` is consumed during flattening and never reaches an adapter.

**Profiles are validated through the hosts that use them.** A profile is partial by design and cannot be checked for completeness alone, so one no host reaches is inert. `provider` is therefore required only *after* merging.

Load-time failures — unknown profile, inherit cycle, empty `inherit`, non-string `inherit` — abort the whole config, consistent with every other config error.

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
- **Destructive-statement safety is specified in §13.12**, which amends this section: hosts default to `readonly = true`, client directives are rejected, parameter validation applies to both providers, and escalation is an operator challenge rather than a terminal test.
- **The no-values rule extends to SQL literals (§13.14).** Logging param names only is not sufficient: a literal written into the SQL text, such as an account number in a `WHERE` clause, would reach the dry-run output and the audit record. The audit record stores the tuple digest and never the SQL; the dry-run document omits the SQL unless explicitly asked for.

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
- **Destructive-statement safety (§13.12):** `readonly = true` by default, grants as the control, `opaque` classified as `destructive`, precheck tokens minted only for clean SQL, no automation exemption.
- **Classifier mechanism (§13.13):** postgres classifies offline with PostgreSQL's own grammar, SQL Server through the engine planner; callers switch on a capability error, not a provider name.
- **Dry-run document (§13.14):** a versioned API with stable reason codes; unrecognised version or code denies; no SQL text or param values, ever, in the audit record.

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

`--border ascii|light|markdown|none` (default `ascii`) selects the frame. It is
a presentation knob only: the NULL marker, the control-character collapse and
the width cap are properties of the renderer, not of a style, so they hold
across all four — a test pins that. Two of the styles are not literally borders
in go-pretty and are adapted:

- **`markdown`** is a separate render mode (`RenderMarkdown()`), not a `Style`.
  It also drops the row-count footer, because that output exists to be pasted
  verbatim into a document where a trailing count reads as a stray paragraph.
  With `--no-headers` it emits data rows and no `---` rule — not a standalone
  table, but exactly what appending to an existing one needs.
- **`none`** uses `OptionsNoBordersAndSeparators`, which leaves the cell padding
  hanging off both edges. The renderer trims one leading space and any trailing
  spaces per line so rows start at column zero, matching `text`.

### 13.9 `--database` completion from a cached list (extends §13.2, §13.7)

`--database` takes a value the shell cannot guess, so it completes to nothing.
The obvious fix — have the completion callback ask the server what databases
exist — is not available, because `runComplete` is bound by a rule stated in
its own doc comment: it *never resolves a credential or opens a database, and
on any error prints nothing and returns 0*. That rule is not fussiness. A
credential may be a `bw:`/`bws:` URI, which shells out to the Bitwarden CLI;
resolving one on a keystroke means a TAB that stalls for seconds, or a vault
unlock prompt fighting the prompt line. **The rule is not relaxed here.**

The list is instead produced by a visible command and read from a file, which
is the same split §13.2 already draws between `schema` (reads the cache) and
`introspect` (goes live and rebuilds it):

```
db-query --host X databases        connects, prints the list, writes the cache
              │
   ~/.cache/db-query/databases/X-<hash>.json      names only
              │
db-query __complete database       reads that file and nothing else
              │
       --database <TAB>
```

**The `databases` command.** Canonical form is `db-query --host X databases`,
with the connection up front per §13.7; `databases --host X` is equally valid,
since the five shared flags are accepted in both positions and this command is
not an exception. It always goes live — there is no cached-read mode, because
the only consumer of the cache is the completion helper, which reads the file
directly. It honours `--output`, `--timeout` and `--config` like every other
row-printing command (§8), renders one column headed `database`, and accepts
`--database` to override which database it attaches to. Exit codes follow
§13.3 unchanged.

It attaches to `host.Database` (falling back to the provider's own default when
that is unset, exactly as `Env` already arranges). It does **not** omit the
database from the client invocation: `internal/adapter/postgres.go` sets every
`PG*` var even to its default precisely so an inherited shell `PGDATABASE`
cannot silently redirect a query, and skipping it would reopen that hole. The
attach target does not affect the answer in any case — both catalogs below are
server-scoped — so it only decides whether the connection succeeds at all.

**Adapter surface.** `Adapter` gains `ListDatabasesSQL() string` beside
`IntrospectSQL()`, so a third client remains one new adapter and zero executor
changes (§7).

```sql
-- postgres
SELECT datname
FROM pg_database
WHERE datallowconn
  AND NOT datistemplate
  AND has_database_privilege(current_user, datname, 'CONNECT')
ORDER BY datname;
```

```sql
-- sqlserver
SELECT name
FROM sys.databases
WHERE state_desc = 'ONLINE'
  AND name <> 'tempdb'
  AND HAS_DBACCESS(name) = 1
ORDER BY name;
```

`NOT datistemplate` is load-bearing rather than belt-and-braces: `template1`
has `datallowconn = true` and passes the privilege check, so only the template
flag excludes it. `tempdb` is excluded **by name**, not by `database_id` — the
familiar `master=1, tempdb=2` mapping is not documented by Microsoft as a
stable contract. Both queries need no privilege beyond an ordinary login, and
both produce a single non-NULL column, so neither exercises the `0x01` NULL
sentinel nor makes the `0x1f` separator load-bearing.

**Both providers degrade to a short list, never an error.** A login with
restricted visibility silently sees fewer databases; it does not fail. This is
worth stating because it is the opposite of what "non-fatal refresh" below
might imply — there is no error to warn about, and a truncated candidate list
is indistinguishable from a complete one. No mitigation is proposed: a missing
completion candidate is a minor harm, and inferring truncation would be worse.

**Cache location and naming (`internal/dblist`).** A sibling to
`internal/schema`, not a reuse of it — same shape, different key.

- Directory: `$XDG_CACHE_HOME/db-query/databases/`, fallback
  `~/.cache/db-query/databases/`.
- Filename: `<sanitized-name>-<8hexhash>.json`, hash being the first 8 hex
  characters of `sha256(name)`. As in §13.2 the hash carries uniqueness and the
  readable part is legibility only; no NUL separator is needed, there being one
  component.
- Contents: a bare JSON array of names, `["postgres","reporting"]`. The command
  builds `adapter.Rows` for the renderer and flattens to `[]string` for the
  file. The two representations are accepted because the flat array is what the
  helper wants and keeps the file trivially readable by eye.
- No TTL. Nothing in this design auto-refreshes, so an expiry could only ever
  remove candidates and never replace them with fresher ones. The cache lives
  until `databases` runs again.

**The key is the config label — `HostConfig.Name` — and this differs from
§13.2 deliberately.** The schema cache keys on `HostConfig.Host`, the resolved
*server address*, which is the better identity: repointing a host entry yields
a different key, so a stale file is simply never read. That scheme cannot be
used here. `MergeCredential` fills a blank `host`, `port` or `database` from
the resolved secret's Extra bag, so for a credential-supplied host the address
does not exist until a credential has been resolved — which the helper may not
do. Keying on the address would leave `--database <TAB>` permanently and
silently dead for exactly the `bws:`-backed hosts most worth completing.

The cost is accepted and bounded. Repointing a host entry, or changing a
`host`/`port` on a profile it inherits, leaves the previous server's names
cached under that label until `databases` is run again; and two config files
that define the same label share one file, the `--config` path being
deliberately absent from the key. Both cost a stale *suggestion*. A name that
no longer exists fails at connect time with the client's own error; nothing is
silently corrupted. Stamping the resolved identity inside the file to detect
drift was considered and rejected — it would catch only the edited-config case
(never the changed-secret case, where there is nothing in config to compare
against) and would force the file to become an object wrapping the array.

`internal/schema` keeps its keying scheme untouched. Its consumers have already
resolved a credential, so it can afford a key the helper cannot compute. Only
the shared `CacheDir`/`sanitize` helpers move out, into a small `internal/cache`
used by both — a mechanical extraction that changes no behaviour and no path.

**`introspect` refreshes the list too** (§13.2). It already holds a live
connection and already exists to rebuild caches, so a host that has ever been
introspected has database completion without a separate step. `query` does not
— it is the hot path and must not grow a second round-trip. A failure of this
extra refresh is **non-fatal**: warn on stderr, still write the schema cache,
still exit 0. `introspect`'s contract is to rebuild the schema, and a failed
bonus must not break the command the user actually ran. Per the paragraph
above, in practice this path catches transport failure — a client that exits
non-zero or a broken pipe — not permission problems, which do not error.

**Completion helper.** `runComplete` gains a `database` target. It determines
the host from `--host` passed by the shell script, else `DB_QUERY_HOST`, which
needs no work — the helper is a subprocess and inherits it. With no
determinable host, or no cache file, it prints nothing and returns 0, as every
other target already does. Candidates are names only: the helper's line format
is `name<TAB>description` and the database target simply omits the tab.

**`completion.zsh`.** Four changes:

- A `__dbq_databases` function calling `__dbq_complete database`, and
  `:database:__dbq_databases` replacing the empty `:database:` action in all
  four specs — top-level, `query`, `schema`, `introspect`.
- `--host` pass-through in `__dbq_complete`, testing **both** `opt_args[--host]`
  and `opt_args[-H]` and normalising to `--host` on the helper's argv. The two
  forms land in separate keys — `-H` does not fold into `--host` — which is why
  the existing `--config`/`-c` and `--category`/`-C` pairs are each tested
  twice, and this follows them.
- A guard on the display string, so a candidate with no description renders as
  a bare name rather than a dangling `name  --  `.
- Nothing else. A cold cache adds no candidates, and verification against zsh
  5.9 confirms this completes nothing rather than falling through to filenames
  — under the default completer and under chains including `_expand`,
  `_correct`, `_approximate` and an explicit `_files`. No `_message` fallback is
  therefore needed.

The top-level `_arguments -C` populates the same `opt_args` map the per-command
functions read, so a host given before the command reaches a `--database`
completing after it. Verified in all four orderings, including the
`db-query --host X --database <TAB>` case with no command typed at all, which
is the §13.7 workflow this feature most serves.

### 13.10 The no-args interactive mode (extends §6, amends §9)

Invoked with no command **and** with stdout attached to a terminal, `db-query`
opens a four-pane Bubble Tea UI — Schema, Query, Saved, Results — rather than
printing usage. The TTY test is the same `*os.File` + `ModeCharDevice`
assertion §13.8 uses for `auto` output, so a pipe, a redirect, or a program
calling `db-query` still gets the usage text and exit 1 unchanged. That
fallback is load-bearing: agent callers and scripts must never find themselves
attached to an interactive program.

The mode adds no new resolution, execution or rendering path. Host and database
resolve through the ordinary precedence (§5, §13.7), rows come back through the
same adapter parse (§7) and are rendered by the same table renderer (§8), and
`--timeout` bounds each run exactly as it bounds a `query`. What it adds is a
session: one host, held open, with the Query pane replacing the shell as the
place SQL is edited between runs. The Schema pane reads the §13.2 cache and
never introspects on its own, so browsing costs no round trip. Results are paged
client-side over rows already fetched — the user's SQL is never rewritten with
`LIMIT`/`OFFSET` — with `DB_QUERY_TUI_PAGE_SIZE` (default 100) setting the page.
A page is normally taller and wider than the pane drawn for it, so the pane
scrolls within the page it is showing: the arrows and their vim doubles move a
row or a column, `g` and `G` reach the page's ends, `Home` and `End` the first
and last column. Both offsets are positions in the current page rather than in
the whole result, so a page change starts at the new page's first row while
keeping the column position, the columns being the same ones either way. The
header row and the row-number gutter sit outside the two windows and so stay
put, and an edge marker names whichever side still has columns behind it. A run
that returns rows moves focus to the pane as the rows land, so the scroll keys
are live without a further keystroke; a run that fails, times out or is
cancelled does not, leaving the user beside the SQL rather than beside an error
they have already read on the label row.

Startup fills in what the invocation left open, with a name-only picker: no
host resolved from flag, environment or config prompts for one of the
configured hosts, and choosing a name behaves exactly as if `--host <name>` had
been passed. A database picker follows when no `--database` was given and
either the host was picked interactively or the host resolved no database at
all — the first because choosing a host is already a "choose my session" flow,
the second because a session with no database cannot run anything. A `--host`
whose config block names a database launches straight in, unprompted. The
picker's names come from the same live catalog listing the `databases` command
runs, refreshing the §13.9 completion cache on the way past, and fall back to
that cache when the host cannot be reached — a listing failure alone does not
strand a session that has been listed before, though a host with neither is an
error rather than a UI that cannot query. Neither picker writes anything back
to config or the environment, and quitting either exits 0 without starting the
UI: nothing to do is not a failure.

**The credential is resolved once, at startup — a deliberate amendment to §9's
"lazy, per-invocation resolution" rule.** The adapter's `Env(cred, host)` call
runs a single time as the session starts, and the resulting environment overlay
is held for the life of the process and reused by every run in it. §9's rule
exists to stop the tool *bulk*-resolving hosts nobody touched, so that unused
vault paths are never hit; it is not a requirement to re-resolve the one host a
session is already connected to. Honouring it literally here would mean a
Bitwarden CLI shell-out or a Keychain prompt on every Ctrl+Enter, which is the
wrong trade for an interactive session — functionally one connection, not a
batch of independent invocations. The narrower guarantees §9 actually protects
are all preserved: still exactly one host resolved, still no password on argv,
still nothing written back to the config file or the environment. Startup
credential failure stays fatal — the error prints on stderr and the process
exits 1 before the Bubble Tea loop starts, matching every other command's
credential-error path.

The implementation lives in `internal/tui`, over an `internal/session` package
holding the setup/run-once logic both it and `internal/cli` need. The import
direction is `cli → tui → session`; `tui` never imports `cli`, which is what
keeps that shared logic in `session` rather than duplicated.

### 13.11 Database selection and in-session switching (amends §13.10)

§13.10 specified a **name-only picker** and a session fixed to the database it
started on. Both are amended here. The picker becomes an explained, filterable
selection screen; the session gains a way to move between databases on its host;
and a rule is added that governs both: **a session only ever lands on a database
whose schema it has cached.**

**The selection screen.** The picker printed a bare list under a one-line
prompt, which left the two questions a first-time user actually has unanswered:
what is asking, and why now. It now prints the build, which flags the invocation
left unresolved, and that nothing is written back — then the list under a
heading naming what it holds. The block prints once per flow: a second picker
carries a one-line record of the host already chosen instead, because Bubble
Tea's inline renderer leaves each finished picker on screen and repeating the
explanation would read as a stutter.

Selection itself gains a full-width accent bar on the current row, a cursor
that **wraps** at both ends, and a case-insensitive **substring** filter that
any printable key starts. Substring rather than fuzzy: for lists this size an
unambiguous rule beats a clever one. Wrapping deliberately differs from the pane
focus grid, which clamps — running off the bottom of a list means "back to the
top", whereas an accidental extra `^h` should stay where it is.

The list logic lives in a `chooser` widget shared with the switcher below, so
the filter and its cursor arithmetic are defined once. The cursor indexes the
**filtered** list, never the underlying slice; that is the off-by-one the shared
widget exists to prevent.

**The introspection rule.** Choosing a database the host has never introspected
used to open a session whose Schema pane was a message telling the user to quit
and run `db-query introspect` from a shell — a poor answer from a flow whose
purpose is to hand back a working session. Databases with no cached schema are
now marked in the list, so the cost of a choice is visible before it is made,
and choosing one offers to build the cache.

The offer is deliberately two-way: introspect and proceed, or go back. "Proceed
without one" is not offered. Two outcomes are easier to reason about than three,
and the invariant they buy is what the Schema pane, and the switcher, rest on. A
failed or cancelled introspection is the same answer as declining: no switch.

The rule binds the interactive paths only. Passing `--host` and `--database`
explicitly still launches straight in, so the Schema pane's "no cached schema"
hint stays reachable and §13.2's manual-refresh model is untouched.

**Where the introspection runs.** At startup it runs in the foreground, between
Bubble Tea programs: the picker has exited, so there is no event loop to freeze,
and the wait is the same one `db-query introspect` already is. In-session it
cannot be: blocking inside `Update` would stop the program repainting, resizing
or handling `^c` for the duration. It is dispatched as a `tea.Cmd` instead, with
the popup showing the wait, swallowing keys and offering `^c`. From the user's
side it is the blocking operation they agreed to; from the program's side
nothing is blocked. Both paths go through the TUI's own ctx-aware `execute`
rather than `session.RunOnce`, which owns its context and cannot be cancelled
from outside.

**The switcher.** `F2` opens a popup listing the host's databases. It opens on
the §13.9 completion cache and refreshes from the live listing behind itself:
the listing is a subprocess against a possibly-distant host, and waiting for it
before drawing anything would make the key feel broken. The refresh holds the
cursor on the name it was on, and a listing failure is reported only when no
cached names were there to stand in for it.

`F2` rather than a `Ctrl` chord: bubbles' textarea binds `^d`, `^b`, `^n`, `^p`
and most of the alphabet in the Query pane, and a session-level action should
not be the one shortcut that stops working in one of the four panes. `⌘D` was
considered and rejected — macOS terminals claim it by default (Terminal.app and
iTerm2 both split the window), it does not exist off macOS, and it needs the
Kitty protocol negotiated to arrive as a distinct key at all. That is the same
reasoning §7 already applies to `F5`.

**What a switch rebuilds.** The Schema pane, because its cache is keyed on
host+database. The Results pane is cleared, because rows fetched from one
database must never sit under a top bar naming another. The Query buffer
survives: re-running the same statement elsewhere is the commonest reason to
switch at all. Saved queries are not database-scoped and are left alone. The
credential is **not** re-resolved — the host has not changed, so §13.10's
resolve-once amendment continues to hold; switching *host* mid-session would
break it and is out of scope.

**Generation-stamped results.** A switch cancels any run in flight, but a cancel
is a request, not an event: the result may already be in the channel. Query
results therefore carry the session generation they were dispatched under, and a
result whose generation no longer matches is discarded. Without it, rows fetched
from the database just left would render into the pane of the one just arrived
at — the failure mode that makes a switcher worse than no switcher.

### 13.12 Destructive-statement safety (extends §6, §7.3; amends §9)

SQL reaches `query` five ways: a positional argument, `-f file`, stdin, a
`--source` saved query, and shell expansion into any of those. Parameter values
arrive separately and are interpolated by the client, not by Go. A caller
inspecting a command line therefore cannot know what will be executed, which
matters because agent tooling increasingly sits in front of this CLI and is
expected to decide whether a command is safe before it runs.

Two problems hide behind that, and conflating them produces controls that do not
work. **Transparency**: only db-query knows the final resolved SQL, so any
external check must ask the tool rather than parse the command line.
**Decidability**: even holding the exact text, effect is not statically
decidable, because both providers can generate statements at runtime.

#### What was measured

Seven payloads were run against PostgreSQL 16.13 through the built binary using
the adapter's real flags. All seven executed:

| Payload as written | What actually happened |
| --- | --- |
| `SELECT 1 AS ok;` then `\! echo … > file` | shell command ran, no SQL effect |
| `SELECT 'DROP TABLE victims'` then `\gexec` | table dropped |
| `SELECT 1 AS harmless;` then `\i payload.sql` | table dropped from an unseen file |
| `SELECT * FROM victims WHERE id = :id`, `--param "id=1; DROP TABLE victims"` | row returned **and** table dropped |
| `DO $$ BEGIN EXECUTE 'DROP TABLE victims'; END $$;` | table dropped |
| `WITH g AS (DELETE FROM victims WHERE id > 1 RETURNING *) SELECT …` | rows deleted, statement opens with `WITH` |
| `\copy (SELECT * FROM victims) TO PROGRAM 'cat > file'` | rows piped to a shell command |

A keyword scanner catches none of rows 1, 2, 3, 5 or 7. Row 4 is invisible even
with the full SQL text, because the payload rides in a parameter value. Row 6
defeats first-keyword classification. This is the evidence for treating
classification as an affordance rather than a control.

#### Classification informs, the engine enforces

The classifier's verdict is advisory by construction. Stripping string literals
is required to avoid false-positives on the word "delete" in prose, and it is
exactly what hides `DO … EXECUTE` and `\gexec`. Anything the lexer cannot
classify with confidence is therefore `opaque`, and **`opaque` is treated as
`destructive`**, never as safe. A scanner that permits what it failed to
understand manufactures false assurance, which is worse than no scanner.

Statements are classified into `read`, `write`, `destructive`, `admin`,
`client_directive` and `opaque`. `WITH` is followed to its terminal statement
and any DML inside a CTE body is flagged, so row 6 above classifies as
`destructive`. `EXPLAIN ANALYZE` classifies as the statement it wraps, because it
executes it.

**The write/destructive boundary is data versus schema.** `write` covers
statements that change rows and nothing else: `INSERT`, `UPDATE`, `MERGE`,
`COPY` in either direction, and `REFRESH MATERIALIZED VIEW`. Everything that
changes the shape of the database is `destructive` alongside the statements
that lose data outright, including `CREATE TABLE`, `CREATE INDEX`, `CREATE
VIEW`, `CREATE FUNCTION`, `CREATE TABLE AS`, `ALTER TABLE` and `ALTER …
RENAME`. There is no separate `ddl` class: the two would gate identically on
both host postures, so a split would add a class and a reason code without
changing any outcome.

The boundary is drawn at data versus schema rather than at the lossiness of
the individual statement, because lossiness is not visible where the
classification happens. `ALTER TABLE t DROP COLUMN x` discards a column and
every value in it, but the grammar gives it the same `AlterTableStmt` node as
`ADD COLUMN`; separating them means enumerating every `AlterTableCmd` subtype
and misclassifying whichever ones a later PostgreSQL adds. The consequence is
that a harmless `CREATE INDEX` meets the same challenge as a `DROP TABLE`,
which is accepted: both warrant a human look.

`SELECT … INTO` needs naming separately. It creates and populates a table, yet
parses as a bare `SelectStmt` whose target survives only as an `IntoClause`
hanging off it, so a classifier reading statement nodes alone passes it as an
ordinary read. The clause itself is therefore what classifies. On SQL Server
the equivalent is the `SELECT INTO` plan `StatementType`, which classifies
destructive for the same reason. Other DDL is not enumerated on either
provider: an unlisted statement type falls to `opaque`, which this section
already treats as `destructive`, so the allowlists stay short and stay
fail-closed.

#### The `readonly` host property

`readonly` is a core config key, defaulting to `true`. It is a core key rather
than an `Extra` passthrough so that `readonly = "yes"` is a config error instead
of a silently ignored value. Because `flatten` merges raw key maps before any
key is interpreted, it inherits through `[profiles.*]` with no change to the
inheritance machinery, and `db-query hosts <name>` reports its origin for free.

The obvious implementation, setting `PGOPTIONS=-c
default_transaction_read_only=on`, is **not sufficient on its own**, and the
measurements say why. Delivered on stdin as the adapter delivers it:

| Escape attempt | Escapes | Catchable in text |
| --- | --- | --- |
| `SET default_transaction_read_only = off` | yes | yes |
| `BEGIN; SET TRANSACTION READ WRITE; …` | yes | yes |
| `SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE` | yes | yes |
| `SELECT set_config('default_transaction_read_only','off',false)` | yes | probably |
| the same via `DO $$ PERFORM set_config(…) $$` | **yes** | **no** |
| the same with the parameter name concatenated at runtime | **yes** | **no** |
| `SET transaction_read_only = off` at top level | no | n/a |
| `RESET transaction_read_only`, `RESET ALL` | no | n/a |
| `ALTER ROLE … SET default_transaction_read_only = off` | no | n/a |

The split is per-transaction versus default-for-subsequent-transactions.
`transaction_read_only` applies to the current transaction, and on stdin each
statement is its own implicit transaction, so setting it affects nothing that
follows. `default_transaction_read_only` sets the default for the next
transaction, which is why the same payload escapes on stdin and does not under
`-c`. Delivery mode is therefore part of the threat model, not an incidental
detail. `REVOKE SET ON PARAMETER` does not close this: USERSET parameters cannot
be locked down that way.

The last two rows of the table are reachable from inside PL/pgSQL, so no text
classifier closes them. Grants do. The same battery run against a login granted
only `SELECT`, with `PGOPTIONS` unset, blocked every entry above and every
payload from the first table, while ordinary `SELECT` continued to work.

`readonly = true` therefore means two things, and the portable one is primary:

1. **A connect-time privilege probe.** The credential is asked whether it can
   write anywhere. If it can, the run is refused. This works on both providers
   and is the part that is actually true.
2. **`PGOPTIONS=-c default_transaction_read_only=on` on postgres**, as a second
   layer that turns accidental writes into a clean early error.

SQL Server gets the probe only. T-SQL has no session-level read-only switch:
`ApplicationIntent=ReadOnly` routes to a readable secondary in an availability
group and enforces nothing on a standalone server or a primary, `ALTER DATABASE
… SET READ_ONLY` is database-wide, and `EXECUTE AS USER … WITH NO REVERT` needs
a pre-created principal. Those three claims come from documentation and require
verification against a real instance before the SQL Server half is built. The
probe is what makes the property mean the same thing on both providers.

The probe runs per invocation rather than being cached with the schema. It costs
one round trip on a connection that is being made anyway, and a cached
privilege result goes stale silently after a grant change, which is the wrong
way for this particular fact to fail.

**Upgrade consequence.** A host whose credential can write and whose config does
not set `readonly = false` stops working on first upgrade. That is intended: the
alternative is a default that quietly promises a guarantee it is not providing.
The refusal names both remedies, and it names them without describing the
precheck mechanism:

```
db-query: refusing to run: host "prod-core" is readonly = true, but its
credential can write. Point the host at a read-only login, or set
readonly = false for that host in ~/.config/db-query/config.toml.
```

#### The precheck token

`--dry-run` resolves SQL from whichever of the five sources applies, classifies
it, and prints the result as JSON without connecting to run it. This is the
transparency surface: a hook re-invokes db-query in dry-run mode and reads a
verdict rather than parsing a command line.

The token binds a **canonical tuple**, not the SQL text: `provider, host,
database, resolved SQL, parameter values`. Two replays require this. Hashing SQL
alone lets a caller dry-run against `dev` and execute against `prod` with the
same digest; and it lets a caller dry-run with `--param id=1` and execute with
`--param "id=1; DROP TABLE victims"`, which is row 4 of the first table and
would defeat the entire scheme. Parameter values are covered by the digest and
never emitted: the tuple exists only long enough to be hashed, and the dry-run
JSON carries the digest and parameter *names*, preserving §9's rule that values
never reach a log.

**A token is minted only for SQL that classified clean.** Dry-run still reports
the full classification for anything else, but emits no passing token for it.
Without this the token proves only that a precheck happened, which an agent can
satisfy by running the precheck itself and ignoring the answer. With it, the
token means "checked, and clean", which is a claim the tool can stand behind,
and escalation is never something the tool hands out.

Execution classifies again, on the same function and the same resolved value.
This is not redundancy: it closes a genuine time-of-check-to-time-of-use window,
because `-f` and `--source` both read files that can change between the check
and the run, and stdin cannot be replayed at all.

| Presented | Verdict at execution | Outcome |
| --- | --- | --- |
| token, tuple matches | clean | runs |
| token, tuple differs | any | refused, exit 6 |
| no token | clean | runs |
| no token | destructive or opaque | operator challenge |
| token | destructive or opaque | operator challenge; the token cannot override |

Row 3 matters: a missing token is not itself a failure, or every scheduled
`SELECT` would become interactive. It is a missing token *together with* a
non-clean verdict that escalates.

The hook passes both through the environment, `DB_QUERY_DRY_RUN=1` and
`DB_QUERY_PRECHECK=<digest>`, rather than by appending flags. Appending to an
agent-authored command line is string surgery that breaks on `db-query q "$SQL"`,
on compound commands, and on heredocs. The environment carries the same
information with no parsing, and it matches the pattern `envDefaults` already
establishes. Equivalent flags exist for manual use.

The digest is keyed with a per-installation secret generated at first run, not a
build-time constant, so one extraction does not compromise every installation.
The key is a deterrent and is documented as one: the agent and db-query run as
the same user, so any secret the tool can read the caller can read, and the key
is not what makes the scheme work. What makes it work is that a token can only
exist for clean SQL.

The digest is deterministic over the tuple rather than single-use. The tuple
already binds everything that distinguishes one execution from another, and a
nonce would add hook-side state for no additional guarantee.

#### The operator challenge

Escalation is a challenge answered on `/dev/tty`, not a test for a terminal.
Testing for one fails in the wrong direction, which was measured with the same
`os.ModeCharDevice` check the CLI already uses for output resolution:

| Caller | stdin | stdout | `/dev/tty` |
| --- | --- | --- | --- |
| non-interactive shell | false | false | unavailable |
| the same, wrapped in `script -qec` | true | true | openable |
| the same, wrapped in a three-line `pty.spawn` | true | true | openable |
| operator at a terminal, output piped | true | **false** | openable |
| operator at a terminal, SQL on stdin | **false** | true | openable |

Rows 2 and 3 are one word of prefix and grant a full terminal. Rows 4 and 5 are
ordinary documented usage that a descriptor test would refuse. Whichever
descriptor is chosen, a wrapped caller is admitted and a real operator is
blocked.

The challenge instead opens `/dev/tty` directly, prints the fully-qualified
target and a nonce, and requires the nonce typed back. Opening `/dev/tty`
explicitly is what survives rows 4 and 5, where stdin carries SQL and stdout is
a pipe. An automated caller can still answer it, but only by reading a nonce and
echoing it, which is a deliberate and visible act rather than a passive wrapper.

There is no exemption for automation. Migrations and scheduled corrections run
through their own paths, and a caller with no `/dev/tty` cannot escalate. This
blocks some legitimate work, which is the accepted trade: an exemption is the
thing that would be found and used.

#### Client directives and parameter binding

db-query builds its own invocation, pins its own output format and delivers SQL
on stdin, so it never needs psql backslash commands or sqlcmd colon commands.
Both are rejected lexically: any statement whose first non-whitespace character
is `\` or `:` outside a literal is refused. This closes rows 1, 3 and 7 of the
first table, which engine-side read-only cannot reach because those directives
are executed by the client and never sent to the server. `-X` is added to the
sqlcmd invocation, which is that client's own switch for disabling commands that
compromise security.

§7.3's sqlcmd value validation is extended to postgres, which had none: the
postgres adapter appended `-v k=v` unchecked, which is why row 4 works there.
Unquoted `:name` interpolation is refused in favour of `:'name'` when a
parameter is bound, so a value cannot terminate a statement.

#### Where the gate lives

`session.RunOnce` and `tui.execute` are independent paths and both call
`Adapter.Build` before `executor.Run`. `Build` is therefore the only choke point
common to both, it already receives the `HostConfig` and the SQL, it is
provider-specific so it knows the dialect, and both callers already handle a
`Build` error. The gate goes there, and the CLI and the TUI are covered by
construction. A gate in `RunOnce` alone would leave the TUI open.

Exit codes extend §13.3: **5** for a policy refusal, **6** for a tuple mismatch.
They are distinct so a caller can tell "policy says no" from "the SQL changed
between check and execution", which are different failures needing different
responses.

#### What this does not do

Recorded so the control set is not over-credited. The keyed digest is a
deterrent, not a boundary, for the same-user reason above. The classifier cannot
see inside dynamic SQL and is not relied on to. The read-only GUC is escapable
and is a second layer, not the guarantee. The guarantee is the credential's
grants, and a host pointed at a writable login has no guarantee at all, which is
why `readonly = true` refuses rather than proceeds. Every run appends one record
carrying timestamp, host, database, tuple digest, classification and decision,
which is a detective control and the evidence trail for PCI DSS v4.0.1
requirement 10.2 (clause verification required against current scoping).

#### Locked decisions

- **`readonly` is a core key, default `true`**, meaning privilege probe plus, on
  postgres, `default_transaction_read_only`. It inherits through profiles.
- **Grants are the control**; the GUC and the classifier are layers above it.
- **`opaque` classifies as `destructive`.** Unclassifiable is never safe.
- **A precheck token is minted only for clean SQL**, and binds `provider, host,
  database, SQL, parameter values`, never SQL alone.
- **Execution reclassifies**, closing the check-to-use window on `-f`,
  `--source` and stdin.
- **Escalation is a `/dev/tty` challenge**, never a terminal-descriptor test.
- **No automation exemption.** Blocking legitimate work is preferred to an
  exemption that would be discovered and used.
- **Client directives are rejected lexically**; sqlcmd gains `-X`.
- **Parameter validation applies to both providers**; postgres unquoted `:name`
  is refused when a parameter is bound.
- **The gate lives in `Adapter.Build`**, the choke point shared by the CLI and
  the TUI.

### 13.13 The classifier interface: one verdict, two mechanisms (extends §13.12)

§13.12 specified *what* the classifier must decide and left *how* open. The two
providers resolve it differently, because what is available to each differs.

**Postgres classifies offline**, using `pganalyze/pg_query_go`, which vendors
PostgreSQL's own grammar. Measured on the classification corpus it is correct on
all thirty cases including the three that defeat text matching, and it falsely
rejects none of twenty legitimate read queries. A verdict costs 0.11 ms for a
short statement and 0.25 ms for a complex one, and needs no database.

**SQL Server classifies through the engine's planner**, because no importable
T-SQL parser exists as a Go module. `SET SHOWPLAN_XML ON` compiles a batch
without executing it, which is the same shape as the `EXPLAIN (FORMAT JSON)`
mechanism measured for postgres, where every write funnels through a single
`ModifyTable` node carrying `Insert`, `Update`, `Delete` or `Merge`, and
everything to be denied outright fails to plan at all. The SQL Server half rests
on documentation rather than measurement and requires verification against a
real instance.

The pure-Go alternative for postgres was rejected on evidence: it pulls 181
transitive modules against the project's fifteen and falsely rejects six of
twenty ordinary read queries, including full-text search, `GROUPING SETS` and
ordered-set aggregates. Under §13.12's fail-closed rule that is not a usable
error rate.

#### The interface

Classification joins the adapter for the same reason everything else
provider-specific does, and it keeps §11's separation intact: **adapters build
and parse, the executor runs.** No adapter is handed a way to execute anything.

```go
// Classify reports what a submission would do. An adapter that can decide from
// the text alone returns a verdict. One that needs the engine returns
// ErrNeedsPlan, and the caller then drives PlanInvocation and ParsePlan for
// each statement. Callers switch on that error, never on the provider name, so
// a third provider chooses its own mechanism without touching the call site.
Classify(sql string) (Verdict, error)

// PlanInvocation builds a plan-only probe for one statement. ParsePlan turns
// the raw result into that statement's verdict. Both are unused by an adapter
// whose Classify succeeds.
PlanInvocation(host config.HostConfig, stmt string) (executor.Invocation, error)
ParsePlan(r executor.RawResult) (Verdict, error)
```

A `Verdict` records the mechanism that produced it:

```go
type Verdict struct {
    Class      Class       // the highest class across every statement
    Mechanism  string      // "parser" or "planner"
    Statements []Statement
}
```

`Mechanism` is not decoration. It reaches the dry-run JSON and the audit record
because the two carry different guarantees: a parser verdict is a statement
about the text, a planner verdict is a statement about what one server, at one
version, would do with it. An auditor reading a record needs to know which.

#### Order of operations

1. **The lexical pre-pass**, shared by both providers and running first. It
   rejects client directives per §13.12 and splits the submission into
   statements. It needs neither a parser nor a connection, which is what lets it
   reject `\!` and `:!!` on a host whose database is unreachable.
2. **The provider mechanism**, selected by the `ErrNeedsPlan` contract above.
3. **The verdict**, reduced to the highest class found.

#### Two rules that make each mechanism fail closed

**Postgres allowlists node types.** The set enumerates what is read-safe, and
anything absent classifies `opaque`, which §13.12 already treats as
`destructive`. A denylist would invert the failure: a statement type added by a
future PostgreSQL release, and therefore absent from the list, would be
permitted. The list must be an allowlist for the same reason the whole design is
fail-closed, and its maintenance is a release-tracking task, not an optional
tidy-up.

**SQL Server treats an unplannable statement as opaque.** A batch that does not
compile is not thereby safe, and the plan must contain no write operation at
all. A failure to plan for a reason that is not policy, a missing table, say,
stays distinguishable through the existing `IsSchemaError` path rather than
being reported as a refusal.

#### Build consequences

`pg_query_go` requires cgo, which the current release configuration forbids.
Measured: `CGO_ENABLED=0` yields `undefined: pg.Parse`, and the release binary
grows from 5.7 MB to 12.3 MB and stops being statically linked.

The release job runs on `ubuntu-latest` and cross-compiles, which cannot work
with cgo. The remedy is narrower than it first appears, because the only targets
are `darwin/amd64` and `darwin/arm64`: a single Apple Silicon runner can produce
both, since the Xcode SDK carries both slices. That needs proving in CI before
it is relied on.

The parser sits behind a build tag so a cgo-less build still compiles. Its
stand-in returns `opaque` with a reason naming the missing parser, so such a
build refuses postgres work loudly rather than passing it through. A safety
component must never be the thing that silently disappears from a build.

#### The asymmetry this creates, stated plainly

A postgres pre-check costs 0.11 ms and works with the database down. A SQL
Server pre-check costs a connection, a credential resolution and a round trip,
and fails when the database is unreachable. Under §13.12's rule that a missing
token plus a non-clean verdict escalates, **an unreachable SQL Server host sends
every destructive-looking query to the operator challenge**, which during an
outage is an approval storm rather than a safety net. The dry-run JSON therefore
reports the mechanism and whether the pre-check completed, so a hook can tell
"classified clean" from "could not classify".

Credential resolution is uncached and shells out (`internal/credential/bws.go`
runs `bws secret get`), so on a SQL Server host the pre-check pays that cost a
second time. Caching a resolved credential for the seconds between pre-check and
execution is the only part of that latency that can be reclaimed, and it is
deferred rather than specified here.

#### Locked decisions

- **Postgres classifies offline via `pg_query_go`; SQL Server classifies via the
  engine planner.** The mechanism is the adapter's business.
- **Callers switch on `ErrNeedsPlan`, never on the provider name.**
- **Adapters still never execute.** Planner probes are built and parsed by the
  adapter and run by the executor, as everything else is.
- **The postgres node set is an allowlist**, so an unrecognised statement type
  denies.
- **The verdict records its mechanism**, and that reaches the dry-run JSON and
  the audit record.
- **The lexical pre-pass runs first for both providers**, so client directives
  are rejected without a parser or a connection.
- **The parser is build-tagged with a fail-closed stand-in**, never silently
  absent.

### 13.14 The dry-run document (extends §13.12, §13.13; amends §9)

`--dry-run` resolves SQL from whichever of §13.12's five sources applies,
classifies it by §13.13's provider mechanism, and prints one JSON document
without running the query. It is the interface a hook reads, and it is
therefore an API: the fields below are a contract, not debug output.

```json
{
  "schema_version": 1,
  "status": "classified",
  "tool": { "version": "0.12.0", "commit": "5fa5f58" },
  "target": { "provider": "postgres", "host": "prod-core", "database": "core" },
  "source": { "kind": "file", "ref": "/tmp/report.sql" },
  "readonly": { "configured": true, "probe": "passed", "engine_enforced": true },
  "classification": {
    "class": "destructive",
    "mechanism": "parser",
    "decided_by_version": "pg_query_go/v6 (PostgreSQL 17 grammar)",
    "statements": [
      { "index": 1, "class": "read",        "decided_by": "SelectStmt" },
      { "index": 2, "class": "destructive", "decided_by": "DeleteStmt in CTE" }
    ]
  },
  "params": { "names": ["who"], "count": 1 },
  "digest": { "alg": "hmac-sha256", "value": "…" },
  "precheck_token": null,
  "decision": {
    "action": "challenge",
    "reason_code": "CLASS_DESTRUCTIVE",
    "reason": "statement 2 deletes rows"
  }
}
```

#### Why each field is there

**`schema_version`, and the rule attached to it.** A consumer that does not
recognise the version **must refuse**, never proceed. Everything else in
§13.12 rests on the hook reading this document correctly, and the failure it
guards against is quiet: rename `decision.action` in a later release and a hook
testing `action == "block"` reads undefined and allows. An ordinary upgrade
would silently disable the control. Versioning is what turns that into a
refusal.

**`status`** is `classified` or `incomplete`. §13.13's asymmetry makes this
load-bearing: the planner mechanism needs a reachable database, so a SQL Server
pre-check can fail for reasons that have nothing to do with the SQL. An
`incomplete` document carries no verdict and must never read as a clean one.

**`mechanism` and `decided_by_version`** record how the verdict was reached and
under which grammar or which server. A parser verdict is a claim about the text;
a planner verdict is a claim about what one server at one version would do with
it. A reviewer reading an audit record a year later needs to know which, and
needs the version to reproduce it.

**`source`** names which of the five inputs the SQL came from. A hook's
confidence legitimately differs: it cannot replay stdin, and a file can change
between the check and the run. It also answers the forensic question the digest
cannot, which is where the statement came from.

**`readonly`** exposes the posture actually in force, distinguishing the
configured value from whether the privilege probe passed. §13.12 lets a hook
demand a strict posture for production hosts, and it can only do that if the
posture is visible rather than assumed.

**`statements`** carries per-statement index, class and what decided it. A
refusal on statement seven of twelve that cannot say which statement is not
actionable.

**`decision.reason_code`** is a stable machine-readable code, separate from the
human prose in `reason`. Hooks branch on the code. A hook branching on English
breaks the first time the wording is improved.

**`precheck_token`** is explicitly `null` rather than absent when no token was
minted, because per §13.12 a token exists only for SQL that classified clean,
and a missing key is indistinguishable from a truncated document.

#### Reason codes

Append-only. A code is never repurposed, and a consumer meeting an unknown code
treats it as a refusal.

| Code | Meaning | Action |
| --- | --- | --- |
| `OK_READ` | every statement reads | allow |
| `CLASS_WRITE` | a statement writes | challenge |
| `CLASS_DESTRUCTIVE` | a statement drops, truncates or deletes | challenge |
| `CLASS_ADMIN` | a statement changes privileges or server state | challenge |
| `CLASS_OPAQUE` | the mechanism could not classify a statement | challenge |
| `CLIENT_DIRECTIVE` | a psql or sqlcmd directive was present | block |
| `PARAM_UNSAFE` | a parameter value carries statement-terminating syntax | block |
| `READONLY_PROBE_FAILED` | `readonly` is set but the credential can write | block |
| `PARSER_UNAVAILABLE` | built without the classifier for this provider | block |
| `PRECHECK_INCOMPLETE` | classification could not be completed | challenge |
| `DIGEST_MISMATCH` | the tuple differs from the one pre-checked | block |

`block` refuses outright. `challenge` routes to §13.12's operator challenge.
Exit codes follow §13.12: **5** for a refusal, **6** for `DIGEST_MISMATCH`,
which is distinct so a caller can tell "policy says no" from "the input changed
between check and execution".

#### What the document must never carry

§9 forbids logging parameter values. That rule does not reach literals written
into the SQL itself, and this is where the gap bites: `SELECT * FROM cards WHERE
pan = '4111…'` would put a primary account number into the dry-run output and
from there into the audit record. **§9 is amended accordingly:** the audit record
stores the digest and never the SQL text, and the dry-run document omits the SQL
by default. It appears only under an explicit debugging flag, which is also the
only way to obtain the argv preview.

Parameter values are likewise absent. `params` carries names and a count; the
values are covered by the digest and reach nothing else.

#### Compatibility rules

- Additive changes only within a major `schema_version`.
- Consumers ignore fields they do not recognise.
- An unrecognised `schema_version` or `reason_code` is a refusal.
- The digest covers §13.12's tuple, never this document, so adding a field never
  invalidates a token.
- `generated_at` belongs to the audit record, not to this document. Two
  dry-runs over identical input produce identical bytes, which makes the
  document diffable and testable as a fixture.

#### Locked decisions

- **The dry-run document is a versioned API**, and an unrecognised version or
  reason code denies.
- **`status` distinguishes "classified clean" from "could not classify".**
- **Reason codes are stable, append-only, and separate from human prose.**
- **The SQL text and parameter values never appear by default**, and never in
  the audit record, which stores the digest alone.
- **The document is byte-reproducible**: no timestamps, no ordering instability.

### 13.15 What the build changed (amends §13.12, §13.13)

Three things the specification got wrong or left open, corrected here against
what was built rather than left to diverge quietly.

**The gate does not live in `Adapter.Build`.** §13.13 put it there on the
grounds that `Build` is the choke point both execution paths share. It is, but
it cannot see what the decision depends on: the presented token, the operator's
answer, and the source the SQL arrived through. Threading those through
`adapter.Query` would put command-line concerns into the adapter contract, which
is the thing that contract exists to keep out.

What `Build` does keep is the parameter check, which is genuinely provider
knowledge. Everything else is a gate in `internal/precheck` that the query path
calls once, after every source of SQL has resolved to one final string.

**`readonly` is the gate's threshold, not just an engine setting.** §13.12
described the decision table as though every host were read-only. Applied
literally, a host explicitly configured `readonly = false` would still send
every `INSERT` to the operator challenge, which makes the gate intolerable on a
development host, and a gate people switch off protects nothing.

The threshold now follows the posture. A read-only host permits reads. A
writable host permits writes as well, because that configuration is the
operator having already said so. Destructive and administrative statements meet
a human on either kind of host, and so does anything the mechanism could not
classify. Client directives are refused outright on both.

**Placeholders had to be normalised before classification.** `:'name'` and
`$(name)` are expanded by psql and sqlcmd, so neither PostgreSQL's grammar nor
SQL Server's planner can parse one. Classified as written, every parameterised
query fell to `opaque` and was refused, which would have made `--param`
unusable. Classification therefore sees placeholders replaced by inert
literals. The digest still binds the original text, and the values are still
validated in the adapter: only the text handed to the mechanism changes.

This was caught by the existing test suite rather than by the corpus, which is
worth recording. The corpus tested SQL a database would receive; it did not
test what this tool actually sends, and the gap between those two is exactly
where a classifier goes wrong.

Deciding which colons are placeholders turned out to be the hard part, and it
is now PostgreSQL's scanner that decides. A cast is a single TYPECAST token so
it is never two colons, a literal is a single SCONST so the colons inside a
time format are not visible, and a slice bound is separated from its colon by
the scanner's own offsets, which is psql's adjacency rule expressed exactly.
Only the last step, recognising that a colon followed by a name is a psql
placeholder, stays ours: that syntax is a client feature and the scanner is the
server's.

The hand-rolled walk it replaced could not go away entirely, because T-SQL has
no PostgreSQL lexer to borrow and a build without cgo still has to refuse
client directives. It is instead checked against the real scanner by a
differential test, on the principle that two implementations of the same
judgement will drift unless something fails when they do.

#### Not yet built

- **The connect-time privilege probe** (§13.12) is unimplemented. The dry-run
  document reports `readonly.probe` as `skipped` rather than claiming a check
  that did not happen. Until it exists, a host configured `readonly = true`
  whose credential can in fact write is not detected, and the guarantee rests
  on the engine setting and the classifier alone.
- **The interactive mode is ungated.** `tui.execute` gets the read-only
  environment and the parameter check, because both live in the adapter, but
  not the classifier. The exposure is narrower than it reads, since an operator
  is at the keyboard by definition, but a destructive statement against a
  writable host is not challenged there.
- **The SQL Server planner path is untested against a real instance**, as
  §13.13 already records. The parsing is written to deny whatever it does not
  recognise, so being wrong about the plan format costs availability rather
  than safety.
