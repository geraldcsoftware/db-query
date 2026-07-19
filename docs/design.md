# db-query — Credential Resolution, Execution & Output Design

> Status: design locked (core) · Language: **Go** · Owner: Gerald
> Scope: personal CLI first, extended to power the `db-query` Claude Code skill

## 1. Purpose

`db-query` is a Go CLI wrapping native database clients (`psql`, `sqlcmd`) that:

1. Resolves credentials from a configured source (Bitwarden SM, Bitwarden CLI, Apple Keychain, env/`.env`) **on demand**, per invocation.
2. Injects them the way each **provider** expects (`PGUSER`/`PGPASSWORD` for Postgres; the SQL Server equivalents for `sqlcmd`) via an **environment overlay**, never argv.
3. Runs a query — ad-hoc SQL or a **named, saved query** — and returns output in a selectable format (`--output text|json`, default `text`).

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

**[LOCKED] `--output text|json`, default `text`.** Format branch lives at **one pivot point**, not per adapter — avoid the `providers × formats` matrix.

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
    "text": textRenderer{}, // tab-separated, default
    "json": jsonRenderer{},
    // "csv", "table" drop in later with zero pipeline change
}
```

**[LOCKED] Consequences:**

- **Always parse into neutral `Rows`.** The earlier "clean-text passthrough" shortcut does not survive `--output json`: once you parse for JSON, parsing is the always-path. Cost relocates into reliable structured parsing per client (§7).
- **NULL fidelity is `[][]*string`.** `null` vs `""` is real once JSON exists; nil pointer = NULL, `&""` = empty. psql CSV gives this cheaply. **v1: sqlcmd Path A does not distinguish** NULL from `"NULL"` (accepted; §7.2) — the row type still holds, Path A just never emits a nil. v2 restores fidelity via Path B.
- **Errors honor `--output`.** In `json` mode a failure emits **structured JSON** (`{"error": "..."}`) to stderr, not a bare string mid-stream, so `--output json | jq` never breaks on the error path. The format flag governs the whole output contract, not just the happy path.
- **Default `text` when the flag is omitted.** No TTY auto-detection — an explicit default is more predictable than magic.

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
- `--output text|json` (default text); parse-once into neutral `Rows`, render-once per format; errors honor the format.
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
