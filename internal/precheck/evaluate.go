package precheck

import (
	"fmt"
	"sort"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// PlanRunner executes a planner probe. It is supplied by the caller rather
// than reached for by this package, which keeps §11's rule intact: the
// executor runs invocations, and neither the adapter nor this gate does.
// A nil PlanRunner means no connection is available, which for a
// planner-mechanism provider yields an incomplete document, never a clean one.
type PlanRunner func(inv executor.Invocation) (executor.RawResult, error)

// Input is everything a verdict depends on.
type Input struct {
	Adapter    adapter.Adapter
	Host       config.HostConfig
	SQL        string
	Params     map[string]string
	Source     Source
	Tool       Tool
	Key        []byte
	RunPlan    PlanRunner
	IncludeSQL bool
}

// Evaluate produces the dry-run document. It never runs the user's SQL; for a
// planner-mechanism provider it runs plan-only probes, which compile without
// executing.
func Evaluate(in Input) Document {
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
	// is executed by psql or sqlcmd and never reaches a server, so neither
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

	// Placeholders are expanded by the client, so neither a grammar nor a
	// planner can parse one. Classification sees them replaced by inert
	// literals; the digest above binds the original text, and the adapter
	// validates the values themselves.
	classifiable := sqlscan.NormalisePlaceholders(in.SQL, in.Adapter.Dialect())

	verdict, err := in.Adapter.Classify(classifiable)
	if err == sqlscan.ErrNeedsPlan {
		verdict, err = classifyByPlan(in)
		if err != nil {
			doc.Status = StatusIncomplete
			doc.Classification = sqlscan.Opaque(sqlscan.MechanismPlanner, "unavailable", err.Error())
			doc.Decision = sqlscan.Decision{
				Action:     sqlscan.ActionChallenge,
				ReasonCode: sqlscan.ReasonPrecheckIncomplete,
				Reason:     "could not classify: " + err.Error(),
			}
			return doc
		}
	} else if err != nil {
		doc.Status = StatusIncomplete
		doc.Classification = sqlscan.Opaque(sqlscan.MechanismParser, "unavailable", err.Error())
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

// classifyByPlan drives the probe pair one statement at a time, which is why
// the pre-pass splits the submission: a planner takes one statement.
func classifyByPlan(in Input) (sqlscan.Verdict, error) {
	if in.RunPlan == nil {
		return sqlscan.Verdict{}, fmt.Errorf("this provider classifies through the engine, and no connection is available")
	}
	statements, _ := sqlscan.Scan(sqlscan.NormalisePlaceholders(in.SQL, in.Adapter.Dialect()), in.Adapter.Dialect())
	if len(statements) == 0 {
		return sqlscan.Verdict{}, fmt.Errorf("no statements found")
	}
	v := sqlscan.Verdict{Mechanism: sqlscan.MechanismPlanner, Version: in.Host.Provider + " engine planner"}
	for i, stmt := range statements {
		inv, err := in.Adapter.PlanInvocation(in.Host, stmt)
		if err != nil {
			return sqlscan.Verdict{}, err
		}
		res, err := in.RunPlan(inv)
		if err != nil {
			return sqlscan.Verdict{}, err
		}
		class, why, err := in.Adapter.ParsePlan(res)
		if err != nil {
			return sqlscan.Verdict{}, err
		}
		v.Statements = append(v.Statements, sqlscan.Statement{Index: i + 1, Class: class, DecidedBy: why})
	}
	v.Reduce()
	return v, nil
}

func paramsOf(p map[string]string) Params {
	names := make([]string, 0, len(p))
	for k := range p {
		names = append(names, k)
	}
	sort.Strings(names) // stable output: the document is meant to be diffable
	return Params{Names: names, Count: len(names)}
}
