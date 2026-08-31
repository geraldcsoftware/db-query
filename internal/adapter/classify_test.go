package adapter

import (
	"io"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/executor"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// The whole point of the ErrNeedsPlan contract is that a caller never asks
// which provider it is holding.
func TestClassifyMechanismIsChosenByTheAdapter(t *testing.T) {
	tests := []struct {
		provider      string
		wantNeedsPlan bool
		wantDialect   sqlscan.Dialect
	}{
		{"postgres", false, sqlscan.DialectPostgres},
		{"sqlserver", true, sqlscan.DialectTSQL},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			a, err := For(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Classify("SELECT 1")
			gotNeedsPlan := err == sqlscan.ErrNeedsPlan
			if gotNeedsPlan != tt.wantNeedsPlan {
				t.Errorf("ErrNeedsPlan: got %v, want %v (err=%v)", gotNeedsPlan, tt.wantNeedsPlan, err)
			}
			if a.Dialect() != tt.wantDialect {
				t.Errorf("dialect: got %v, want %v", a.Dialect(), tt.wantDialect)
			}
		})
	}
}

func TestSQLServerPlanInvocationCompilesWithoutExecuting(t *testing.T) {
	inv, err := sqlserverAdapter{}.PlanInvocation(config.HostConfig{}, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(inv.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SET SHOWPLAN_XML ON") {
		t.Errorf("probe does not request a plan: %q", body)
	}
	// The probe must not turn execution back on inside the batch it is
	// planning.
	if strings.Contains(strings.ToUpper(string(body)), "SHOWPLAN_XML OFF") {
		t.Errorf("probe re-enables execution within the same batch: %q", body)
	}
}

func TestSQLServerParsePlan(t *testing.T) {
	tests := []struct {
		name   string
		result executor.RawResult
		want   sqlscan.Class
	}{
		{
			"select",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="SELECT" />`)},
			sqlscan.ClassRead,
		},
		{
			// The engine's label for a SELECT that touches no table
			// (SELECT 1, SELECT @@VERSION). A read, not an unknown verb.
			"select without query",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="SELECT WITHOUT QUERY" />`)},
			sqlscan.ClassRead,
		},
		{
			"delete",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="DELETE" />`)},
			sqlscan.ClassDestructive,
		},
		{
			"update",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="UPDATE" />`)},
			sqlscan.ClassWrite,
		},
		{
			// SELECT INTO creates the target table, so it is a schema change
			// on this provider for the same reason it is on postgres.
			"select into",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="SELECT INTO" />`)},
			sqlscan.ClassDestructive,
		},
		{
			"a batch takes its worst statement",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="SELECT" /><StmtSimple StatementType="DELETE" />`)},
			sqlscan.ClassDestructive,
		},
		{
			// Not compiling is not the same as being safe.
			"failed compile is opaque",
			executor.RawResult{ExitCode: 1, Stderr: []byte("Msg 102: incorrect syntax")},
			sqlscan.ClassOpaque,
		},
		{
			"a plan with no statement type is opaque",
			executor.RawResult{Stdout: []byte(`<ShowPlanXML/>`)},
			sqlscan.ClassOpaque,
		},
		{
			"an unrecognised verb denies rather than passing",
			executor.RawResult{Stdout: []byte(`<StmtSimple StatementType="SOMETHING NEW" />`)},
			sqlscan.ClassOpaque,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := sqlserverAdapter{}.ParsePlan(tt.result)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
