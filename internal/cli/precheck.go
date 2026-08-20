package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/precheck"
	"github.com/geraldcsoftware/db-query/internal/render"
	"github.com/geraldcsoftware/db-query/internal/session"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// Environment forms of the precheck flags. A hook spawns the same argv with
// one of these set rather than editing the command string: appending a flag to
// an agent-authored line is string surgery that breaks on `db-query q "$SQL"`,
// on compound commands and on heredocs (docs/design.md §13.12).
const (
	envDryRun   = "DB_QUERY_DRY_RUN"
	envPrecheck = "DB_QUERY_PRECHECK"
)

// precheckFlags carries the safety-gate options through runQuery.
type precheckFlags struct {
	dryRun  bool
	token   string
	showSQL bool
	source  precheck.Source
}

// resolve folds the environment into the flags. A flag wins over the
// environment, matching how every other setting in this CLI resolves.
func (p *precheckFlags) resolve() {
	if !p.dryRun && os.Getenv(envDryRun) != "" && os.Getenv(envDryRun) != "0" {
		p.dryRun = true
	}
	if p.token == "" {
		p.token = os.Getenv(envPrecheck)
	}
}

// sourceOf names which of the five inputs the SQL arrived through, which a
// hook uses to judge how much its own pre-check is worth: stdin cannot be
// replayed, and a file can change between the check and the run.
func sourceOf(positional []string, file, savedName string) precheck.Source {
	switch {
	case savedName != "":
		return precheck.Source{Kind: "saved", Ref: savedName}
	case len(positional) > 0:
		return precheck.Source{Kind: "arg"}
	case file != "" && file != "-":
		return precheck.Source{Kind: "file", Ref: file}
	default:
		return precheck.Source{Kind: "stdin"}
	}
}

// evaluate builds the dry-run document for a resolved invocation.
func evaluate(r session.Resolved, c commonFlags, p precheckFlags, sql string, params map[string]string) (precheck.Document, precheck.Tuple, []byte, error) {
	key, err := precheck.LoadKey()
	if err != nil {
		return precheck.Document{}, precheck.Tuple{}, nil, err
	}
	tuple := precheck.Tuple{
		Provider:    r.Host.Provider,
		Host:        r.Host.Name,
		Database:    r.Host.Database,
		SQL:         sql,
		ParamValues: params,
	}
	doc := precheck.Evaluate(precheck.Input{
		Adapter:    r.Adapter,
		Host:       r.Host,
		SQL:        sql,
		Params:     params,
		Source:     p.source,
		Tool:       precheck.Tool{Version: buildInfo.Version, Commit: buildInfo.Commit},
		Key:        key,
		RunPlan:    plannerFor(r, c),
		IncludeSQL: p.showSQL,
	})
	return doc, tuple, key, nil
}

// plannerFor runs a plan-only probe for the providers that classify through
// the engine. The closure lives here rather than in the adapter or the gate so
// that §11's rule holds: the executor runs invocations, nothing else does.
func plannerFor(r session.Resolved, c commonFlags) precheck.PlanRunner {
	return func(inv executor.Invocation) (executor.RawResult, error) {
		inv.Env = r.Adapter.Env(r.Cred, r.Host)
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		return executor.Run(ctx, inv)
	}
}

// emitDocument writes the dry-run document and returns the process exit code.
// It is always JSON, whatever --output says: this is a machine interface, and
// a caller that asked for a dry run asked for this document.
func emitDocument(doc precheck.Document, stdout io.Writer) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		fmt.Fprintln(os.Stderr, "db-query: "+err.Error())
		return 1
	}
	// A dry run reports; it does not refuse. The document carries the
	// decision, and exit 0 means "this document is valid", not "this query is
	// permitted". Conflating the two would make a transport failure
	// indistinguishable from a refusal.
	return 0
}

// gate applies the decision to a real invocation.
func gate(doc precheck.Document, p precheckFlags, tuple precheck.Tuple, key []byte, c commonFlags, stderr io.Writer) int {
	outcome, why := precheck.Gate(doc, p.token, tuple, key, precheck.TTYChallenge)
	switch outcome {
	case precheck.Proceed:
		return 0
	case precheck.Mismatch:
		render.Error(stderr, c.output, "refusing to run: "+why)
		return outcome.ExitCode()
	default:
		render.Error(stderr, c.output, refusalMessage(doc, why))
		return outcome.ExitCode()
	}
}

// refusalMessage tells an operator what to do next without describing the
// precheck mechanism. Naming the legitimate routes is what keeps a refusal
// from stranding someone; naming the mechanism would hand a caller the recipe.
func refusalMessage(doc precheck.Document, why string) string {
	msg := fmt.Sprintf("refusing to run against %s on %s: %s",
		doc.Target.Database, doc.Target.Host, why)
	switch doc.Decision.ReasonCode {
	case sqlscan.ReasonClientDirective:
		return msg + "\nRemove the client directive: db-query sends SQL to the server, not commands to psql or sqlcmd."
	case sqlscan.ReasonParserUnavailable:
		return msg
	default:
		if doc.ReadOnly.Configured {
			return msg + fmt.Sprintf(
				"\nRun it yourself against %s, or set readonly = false for that host in the config file.",
				doc.Target.Host)
		}
		return msg + fmt.Sprintf("\nRun it yourself against %s.", doc.Target.Host)
	}
}
