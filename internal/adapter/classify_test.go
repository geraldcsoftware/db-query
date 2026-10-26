package adapter

import (
	"context"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// countingRunner records whether a classifier reached for a connection, and
// hands over whatever conn it was given.
type countingRunner struct {
	calls int
	conn  Conn
}

func (r *countingRunner) With(_ context.Context, f func(Conn) error) error {
	r.calls++
	return f(r.conn)
}

// The point of one Classify call is that a caller never asks which provider it
// is holding: each adapter picks its mechanism and reports which it used.
func TestClassifyMechanismIsChosenByTheAdapter(t *testing.T) {
	ctx := context.Background()

	t.Run("postgres decides offline and never connects", func(t *testing.T) {
		if !sqlscan.ParserAvailable() {
			t.Skip("built without cgo: the postgres classifier refuses every submission by design")
		}
		r := &countingRunner{}
		v, err := postgresAdapter{}.Classify(ctx, "SELECT $1", map[string]string{"1": "x"}, r)
		if err != nil || v.Mechanism != sqlscan.MechanismParser || v.Class != sqlscan.ClassRead {
			t.Fatalf("verdict = %+v, err = %v", v, err)
		}
		if r.calls != 0 {
			t.Fatal("the parser mechanism reached for a connection")
		}
		if (postgresAdapter{}).Dialect() != sqlscan.DialectPostgres {
			t.Fatal("dialect")
		}
	})
	t.Run("sqlserver without a connection cannot decide", func(t *testing.T) {
		v, err := sqlserverAdapter{}.Classify(ctx, "SELECT 1", nil, nil)
		if err == nil || v.Mechanism != sqlscan.MechanismPlanner {
			t.Fatalf("verdict = %+v, err = %v; want an error naming the planner", v, err)
		}
		if (sqlserverAdapter{}).Dialect() != sqlscan.DialectTSQL {
			t.Fatal("dialect")
		}
	})
	t.Run("sqlserver refuses a bad param name before connecting", func(t *testing.T) {
		r := &countingRunner{}
		if _, err := (sqlserverAdapter{}).Classify(ctx, "SELECT @who", map[string]string{"@who": "x"}, r); err == nil {
			t.Fatal("want the name refused")
		}
		if r.calls != 0 {
			t.Fatal("a refused submission reached for a connection")
		}
	})
	t.Run("sqlserver will not plan on another provider's connection", func(t *testing.T) {
		r := &countingRunner{conn: &pgConn{}}
		if _, err := (sqlserverAdapter{}).Classify(ctx, "SELECT 1", nil, r); err == nil {
			t.Fatal("planning on a postgres connection must fail closed")
		}
	})
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
