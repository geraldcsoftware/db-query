package precheck

import (
	"context"
	"fmt"
	"sort"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// Input is everything a verdict depends on.
type Input struct {
	Adapter adapter.Adapter
	Host    config.HostConfig
	SQL     string
	Params  map[string]string
	Source  Source
	Tool    Tool
	Key     []byte
	// Runner is the connection a classifier that asks the engine's planner
	// may use. It is supplied by the caller rather than reached for by this
	// package, so the caller decides whether a connection may be made at all;
	// a nil Runner means it may not, which for such a provider yields an
	// incomplete document, never a clean one.
	Runner     adapter.Runner
	IncludeSQL bool
}

// Evaluate produces the dry-run document. It never runs the user's SQL; for a
// planner-mechanism provider it runs plan-only probes, which compile without
// executing. ctx bounds those probes.
func Evaluate(ctx context.Context, in Input) Document {
	doc := Document{
		SchemaVersion: SchemaVersion,
		Status:        StatusClassified,
		Tool:          in.Tool,
		Target: Target{
			Provider: in.Host.Provider,
			Host:     in.Host.Name,
			Database: in.Host.Database,
		},
		Source: in.Source,
		ReadOnly: ReadOnly{
			Configured: in.Host.ReadOnly,
			// The connect-time privilege probe specified in §13.12 is not
			// implemented yet, so it is reported as skipped rather than
			// claimed. Engine enforcement is postgres-only: T-SQL has no
			// session-level equivalent.
			Probe:          "skipped",
			EngineEnforced: in.Host.ReadOnly && in.Host.Provider == "postgres",
		},
		Params: paramsOf(in.Params),
	}
	if in.IncludeSQL {
		doc.SQL = in.SQL
	}

	tuple := Tuple{
		Provider:    in.Host.Provider,
		Host:        in.Host.Name,
		Database:    in.Host.Database,
		SQL:         in.SQL,
		ParamValues: in.Params,
	}
	doc.Digest = DigestField{Alg: "hmac-sha256", Value: Digest(tuple, in.Key)}

	// The lexical pre-pass runs first, for every provider. A client directive
	// is a psql or sqlcmd command and never reaches a server, so neither
	// mechanism can see one: it is caught here or not at all. It is also the
	// one refusal that holds with the database unreachable.
	_, directives := sqlscan.Scan(in.SQL, in.Adapter.Dialect())
	if len(directives) > 0 {
		doc.Classification = sqlscan.Opaque(
			in.Adapter.Dialect().String(), "lexical pre-pass",
			"client directive: "+directives[0])
		doc.Decision = sqlscan.Decision{
			Action:     sqlscan.ActionBlock,
			ReasonCode: sqlscan.ReasonClientDirective,
			Reason:     fmt.Sprintf("%q is executed by the client, not the server", directives[0]),
		}
		return doc
	}

	// Both providers bind parameters server-side, so a placeholder is part of
	// the grammar or the plan and the submission is classified as it stands.
	verdict, err := in.Adapter.Classify(ctx, in.SQL, in.Params, in.Runner)
	if err != nil {
		doc.Status = StatusIncomplete
		doc.Classification = sqlscan.Opaque(verdict.Mechanism, "unavailable", err.Error())
		doc.Decision = sqlscan.Decision{
			Action:     sqlscan.ActionChallenge,
			ReasonCode: sqlscan.ReasonPrecheckIncomplete,
			Reason:     "could not classify: " + err.Error(),
		}
		return doc
	}

	doc.Classification = verdict
	doc.Decision = sqlscan.Decide(verdict, in.Host.ReadOnly)

	// A token exists only for SQL that classified clean. Without that rule the
	// token would prove that a pre-check happened rather than that it passed,
	// which a caller satisfies by running the pre-check itself and ignoring
	// the answer.
	if doc.Decision.Action == sqlscan.ActionAllow {
		v := doc.Digest.Value
		doc.PrecheckToken = &v
	}
	return doc
}

func paramsOf(p map[string]string) Params {
	names := make([]string, 0, len(p))
	for k := range p {
		names = append(names, k)
	}
	sort.Strings(names) // stable output: the document is meant to be diffable
	return Params{Names: names, Count: len(names)}
}
