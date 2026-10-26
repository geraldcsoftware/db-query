package adapter

import (
	"strings"
	"testing"

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

func TestSQLServerPlanTextDeclaresBoundParams(t *testing.T) {
	tests := []struct {
		name   string
		stmt   string
		params map[string]string
		want   string
	}{
		{"no params, untouched", "SELECT 1", nil, "SELECT 1"},
		{"unreferenced params are not declared", "SELECT 1", map[string]string{"who": "Ada"}, "SELECT 1"},
		{"declared at the length the driver binds", "SELECT @who",
			map[string]string{"who": "Grace"}, "DECLARE @who nvarchar(5);\nSELECT @who"},
		{"an empty value is max, as the driver sends it", "SELECT @who",
			map[string]string{"who": ""}, "DECLARE @who nvarchar(max);\nSELECT @who"},
		{"length counts UTF-16 units, not bytes", "SELECT @who",
			map[string]string{"who": "héllo"}, "DECLARE @who nvarchar(5);\nSELECT @who"},
		{"over 4000 is max", "SELECT @who",
			map[string]string{"who": strings.Repeat("x", 4001)}, "DECLARE @who nvarchar(max);\nSELECT @who"},
		{"the --param spelling is declared", "SELECT @WHO",
			map[string]string{"who": "Ada"}, "DECLARE @who nvarchar(3);\nSELECT @WHO"},
		{"a local variable is left to the batch", "SELECT @a, @b",
			map[string]string{"b": "1"}, "DECLARE @b nvarchar(1);\nSELECT @a, @b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mssqlPlanText(tt.stmt, tt.params); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLServerShowplanClass(t *testing.T) {
	tests := []struct {
		name string
		plan string
		want sqlscan.Class
	}{
		{
			"select",
			`<StmtSimple StatementType="SELECT" />`,
			sqlscan.ClassRead,
		},
		{
			// The engine's label for a SELECT that touches no table
			// (SELECT 1, SELECT @@VERSION). A read, not an unknown verb.
			"select without query",
			`<StmtSimple StatementType="SELECT WITHOUT QUERY" />`,
			sqlscan.ClassRead,
		},
		{
			"delete",
			`<StmtSimple StatementType="DELETE" />`,
			sqlscan.ClassDestructive,
		},
		{
			"update",
			`<StmtSimple StatementType="UPDATE" />`,
			sqlscan.ClassWrite,
		},
		{
			// SELECT INTO creates the target table, so it is a schema change
			// on this provider for the same reason it is on postgres.
			"select into",
			`<StmtSimple StatementType="SELECT INTO" />`,
			sqlscan.ClassDestructive,
		},
		{
			"a batch takes its worst statement",
			`<StmtSimple StatementType="SELECT" /><StmtSimple StatementType="DELETE" />`,
			sqlscan.ClassDestructive,
		},
		{
			"a plan with no statement type is opaque",
			`<ShowPlanXML/>`,
			sqlscan.ClassOpaque,
		},
		{
			"an unrecognised verb denies rather than passing",
			`<StmtSimple StatementType="SOMETHING NEW" />`,
			sqlscan.ClassOpaque,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := mssqlShowplanClass(tt.plan)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
