package adapter

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
)

func TestSQLServerDSN(t *testing.T) {
	a := sqlserverAdapter{}
	cred := credential.Credential{Username: "svc reports", Password: "p@ss:w/rd?&"}

	parse := func(t *testing.T, h config.HostConfig) msdsn.Config {
		t.Helper()
		cfg, err := msdsn.Parse(a.dsn(cred, h))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	t.Run("credential survives URL escaping", func(t *testing.T) {
		cfg := parse(t, config.HostConfig{Host: "sql01", Database: "reports"})
		if cfg.User != cred.Username || cfg.Password != cred.Password {
			t.Fatalf("user = %q, password mangled", cfg.User)
		}
		if cfg.Database != "reports" || cfg.Host != "sql01" || cfg.Port != 1433 {
			t.Fatalf("cfg = %s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		}
		if cfg.AppName != "db-query" {
			t.Fatalf("app name = %q", cfg.AppName)
		}
	})
	t.Run("localhost and an explicit port", func(t *testing.T) {
		cfg := parse(t, config.HostConfig{Port: 11433})
		if cfg.Host != "localhost" || cfg.Port != 11433 {
			t.Fatalf("cfg = %s:%d", cfg.Host, cfg.Port)
		}
	})
	t.Run("instance wins over port", func(t *testing.T) {
		cfg := parse(t, config.HostConfig{
			Host: "sql01", Port: 11433, Extra: map[string]string{"instance": "SQLEXPRESS"},
		})
		if cfg.Instance != "SQLEXPRESS" || cfg.Port != 0 {
			t.Fatalf("instance = %q port = %d", cfg.Instance, cfg.Port)
		}
	})
	t.Run("encryption is required and the certificate verified by default", func(t *testing.T) {
		cfg := parse(t, config.HostConfig{Host: "sql01"})
		if cfg.Encryption != msdsn.EncryptionRequired {
			t.Fatalf("encryption = %v, want required", cfg.Encryption)
		}
		if cfg.TrustServerCertificate || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify {
			t.Fatal("the server certificate must be verified unless the host opts out")
		}
	})
	t.Run("a host can opt out of verification or encryption", func(t *testing.T) {
		cfg := parse(t, config.HostConfig{Host: "sql01", Extra: map[string]string{"trustservercertificate": "true"}})
		if !cfg.TrustServerCertificate {
			t.Fatal("trustservercertificate not passed through")
		}
		cfg = parse(t, config.HostConfig{Host: "sql01", Extra: map[string]string{"encrypt": "disable"}})
		if cfg.Encryption != msdsn.EncryptionDisabled {
			t.Fatalf("encryption = %v, want disabled", cfg.Encryption)
		}
		cfg = parse(t, config.HostConfig{Host: "sql01", Extra: map[string]string{"encrypt": "strict"}})
		if cfg.Encryption != msdsn.EncryptionStrict {
			t.Fatalf("encryption = %v, want strict", cfg.Encryption)
		}
	})
	t.Run("no database means the login's default", func(t *testing.T) {
		u, err := url.Parse(a.dsn(cred, config.HostConfig{Host: "sql01"}))
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Has("database") {
			t.Fatalf("database set without being configured: %s", u.RawQuery)
		}
	})
}

func TestSQLServerOpenDoesNotLeakTheDSN(t *testing.T) {
	cred := credential.Credential{Username: "sa", Password: "Sekr1t-pw"}
	_, err := sqlserverAdapter{}.Open(context.Background(), cred, config.HostConfig{
		Name: "bad", Host: "sql01", Extra: map[string]string{"encrypt": "sometimes"},
	})
	var le *LocalError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want a LocalError", err)
	}
	if strings.Contains(err.Error(), cred.Password) {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestSQLServerPlan(t *testing.T) {
	a := sqlserverAdapter{}

	t.Run("splits at GO and keeps a batch together", func(t *testing.T) {
		got, err := a.plan(Query{SQL: "DECLARE @x int = 1; SELECT @x\nGO 2\nSELECT 2"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].sql != "DECLARE @x int = 1; SELECT @x" || got[0].count != 2 || got[1].count != 1 {
			t.Fatalf("batches = %+v", got)
		}
	})

	t.Run("binds only the params a batch references", func(t *testing.T) {
		got, err := a.plan(Query{
			SQL:    "SELECT @who\nGO\nSELECT @Min, @local",
			Params: map[string]string{"who": "Ada", "min": "2"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want0 := []any{sql.Named("who", mssql.VarChar("Ada"))}
		want1 := []any{sql.Named("min", mssql.VarChar("2"))}
		if !reflect.DeepEqual(got[0].args, want0) || !reflect.DeepEqual(got[1].args, want1) {
			t.Fatalf("args = %v / %v", got[0].args, got[1].args)
		}
	})

	t.Run("ASCII binds as varchar, anything else as nvarchar", func(t *testing.T) {
		got, err := a.plan(Query{SQL: "SELECT @a, @b", Params: map[string]string{"a": "plain", "b": "café"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []any{sql.Named("a", mssql.VarChar("plain")), sql.Named("b", "café")}
		if !reflect.DeepEqual(got[0].args, want) {
			t.Fatalf("args = %#v", got[0].args)
		}
	})

	t.Run("hostile values are bound, not refused", func(t *testing.T) {
		for _, v := range []string{"x'y", `x"y`, "1;DROP TABLE t", "a--b", "$(oops)", "line\nbreak"} {
			got, err := a.plan(Query{SQL: "SELECT @p", Params: map[string]string{"p": v}})
			if err != nil {
				t.Fatalf("value %q refused: %v", v, err)
			}
			if got[0].sql != "SELECT @p" {
				t.Fatalf("value reached the SQL text: %q", got[0].sql)
			}
		}
	})

	t.Run("bad names are refused without echoing the value", func(t *testing.T) {
		for _, k := range []string{"1", "@who", "a-b", "a b", "", "9lives"} {
			_, err := a.plan(Query{SQL: "SELECT 1", Params: map[string]string{k: "SECRET"}})
			var le *LocalError
			if !errors.As(err, &le) {
				t.Fatalf("name %q: err = %v, want a LocalError", k, err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error echoes the value: %v", err)
			}
		}
	})

	t.Run("an @ prefix is pointed at the right spelling", func(t *testing.T) {
		_, err := a.plan(Query{SQL: "SELECT @who", Params: map[string]string{"@who": "Ada"}})
		if err == nil || !strings.Contains(err.Error(), "--param who=value") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("names differing only in case are ambiguous", func(t *testing.T) {
		_, err := a.plan(Query{SQL: "SELECT @id", Params: map[string]string{"id": "1", "ID": "2"}})
		if err == nil {
			t.Fatal("want an ambiguity error")
		}
	})

	t.Run("a client directive is refused", func(t *testing.T) {
		_, err := a.plan(Query{SQL: ":r other.sql\nSELECT 1"})
		var le *LocalError
		if !errors.As(err, &le) || !strings.Contains(err.Error(), ":r other.sql") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("empty submission is no batches", func(t *testing.T) {
		got, err := a.plan(Query{SQL: "  \nGO\n"})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
}

type fakeColumn struct {
	name  string
	scale int64
}

func (c fakeColumn) DatabaseTypeName() string { return c.name }
func (c fakeColumn) DecimalSize() (int64, int64, bool) {
	return 0, c.scale, c.scale >= 0
}

func TestSQLServerText(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 5, 7, 123456700, time.UTC)
	tests := []struct {
		name string
		v    any
		col  fakeColumn
		want string
	}{
		{"string", "Ada", fakeColumn{"NVARCHAR", -1}, "Ada"},
		{"empty string is not NULL", "", fakeColumn{"VARCHAR", -1}, ""},
		{"int", int64(-42), fakeColumn{"INT", -1}, "-42"},
		{"float", 1.5, fakeColumn{"FLOAT", -1}, "1.5"},
		{"real keeps single precision", float64(float32(0.1)), fakeColumn{"REAL", -1}, "0.1"},
		{"bit true", true, fakeColumn{"BIT", -1}, "1"},
		{"bit false", false, fakeColumn{"BIT", -1}, "0"},
		{"decimal is the driver's text", []byte("1.50"), fakeColumn{"DECIMAL", 2}, "1.50"},
		{"money", []byte("3.0000"), fakeColumn{"MONEY", -1}, "3.0000"},
		{"binary is 0x hex", []byte{0x0a, 0xff}, fakeColumn{"VARBINARY", -1}, "0x0AFF"},
		{"guid in SQL Server byte order",
			[]byte{0x67, 0x45, 0x23, 0x01, 0xab, 0x89, 0xef, 0xcd, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef},
			fakeColumn{"UNIQUEIDENTIFIER", -1}, "01234567-89AB-CDEF-0123-456789ABCDEF"},
		{"date", at, fakeColumn{"DATE", -1}, "2026-10-26"},
		{"datetime has milliseconds", at, fakeColumn{"DATETIME", -1}, "2026-10-26 09:05:07.123"},
		{"smalldatetime", at, fakeColumn{"SMALLDATETIME", -1}, "2026-10-26 09:05:07"},
		{"datetime2 at its scale", at, fakeColumn{"DATETIME2", 7}, "2026-10-26 09:05:07.1234567"},
		{"datetime2(0)", at, fakeColumn{"DATETIME2", 0}, "2026-10-26 09:05:07"},
		{"time at its scale", at, fakeColumn{"TIME", 3}, "09:05:07.123"},
		{"datetimeoffset", at.In(time.FixedZone("", 2*3600)), fakeColumn{"DATETIMEOFFSET", 2},
			"2026-10-26 11:05:07.12 +02:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mssqlText(tt.v, tt.col)
			if got == nil || *got != tt.want {
				t.Fatalf("got %v, want %q", got, tt.want)
			}
		})
	}
	if mssqlText(nil, fakeColumn{"NVARCHAR", -1}) != nil {
		t.Fatal("NULL must render as nil, not a string")
	}
}

func TestSQLServerKind(t *testing.T) {
	for name, want := range map[string]Kind{
		"TINYINT": KindNumber, "SMALLINT": KindNumber, "INT": KindNumber, "BIGINT": KindNumber,
		"REAL": KindNumber, "FLOAT": KindNumber, "DECIMAL": KindNumber,
		"MONEY": KindNumber, "SMALLMONEY": KindNumber,
		"BIT":      KindBool,
		"NVARCHAR": KindText, "DATETIME2": KindText, "UNIQUEIDENTIFIER": KindText,
		"VARBINARY": KindText, "SQL_VARIANT": KindText, "XML": KindText,
	} {
		if got := mssqlKind(name); got != want {
			t.Errorf("mssqlKind(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestSQLServerFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("engine error keeps its number and position", func(t *testing.T) {
		err := mssqlFailure(ctx, mssql.Error{Number: 102, Class: 15, State: 1, LineNo: 3, Message: "Incorrect syntax near 'SELEC'."}, "")
		want := "Msg 102, Level 15, State 1, Line 3: Incorrect syntax near 'SELEC'."
		var qe *QueryError
		if !errors.As(err, &qe) || qe.Msg != want || qe.Schema {
			t.Fatalf("err = %#v, want %q", err, want)
		}
	})
	t.Run("207 and 208 are schema errors", func(t *testing.T) {
		for _, n := range []int32{207, 208} {
			var qe *QueryError
			if !errors.As(mssqlFailure(ctx, mssql.Error{Number: n}, ""), &qe) || !qe.Schema {
				t.Errorf("Msg %d not flagged as a schema error", n)
			}
		}
	})
	t.Run("where names the batch", func(t *testing.T) {
		err := mssqlFailure(ctx, mssql.Error{Number: 8134, Class: 16, State: 1, Message: "Divide by zero error encountered."}, "batch 2 of 3")
		if !strings.HasPrefix(err.Error(), "batch 2 of 3: Msg 8134") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a cancelled run reports the cancel", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := mssqlFailure(cctx, errors.New("connection reset"), ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("a non-engine error is still a query error", func(t *testing.T) {
		var qe *QueryError
		if !errors.As(mssqlFailure(ctx, errors.New("dial tcp: refused"), ""), &qe) {
			t.Fatal("want a QueryError")
		}
	})
}

func TestSQLServerPreviewSQL(t *testing.T) {
	cases := map[string]string{
		"dbo.orders":     `SELECT TOP 100 * FROM [dbo].[orders];`,
		"orders":         `SELECT TOP 100 * FROM [orders];`,
		"dbo.Order Item": `SELECT TOP 100 * FROM [dbo].[Order Item];`, // mixed case and a space survive quoting
		"dbo.we]ird":     `SELECT TOP 100 * FROM [dbo].[we]]ird];`,    // an embedded bracket is doubled
	}
	for table, want := range cases {
		if got := (sqlserverAdapter{}).PreviewSQL(table); got != want {
			t.Errorf("PreviewSQL(%q) = %q, want %q", table, got, want)
		}
	}
}

// TestSQLServerListDatabasesSQL pins the candidate-list predicates. tempdb is
// excluded by name rather than by database_id: the familiar master=1, tempdb=2
// mapping is not documented by Microsoft as a stable contract (design.md §13.9).
func TestSQLServerListDatabasesSQL(t *testing.T) {
	sql := sqlserverAdapter{}.ListDatabasesSQL()
	for _, want := range []string{
		"sys.databases",
		"state_desc = 'ONLINE'",
		"name <> 'tempdb'",
		"HAS_DBACCESS(name) = 1",
		"ORDER BY name",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("list-databases sql missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "database_id") {
		t.Fatalf("tempdb must be excluded by name, not database_id:\n%s", sql)
	}
	if strings.Count(sql, "SELECT") != 1 || strings.Contains(sql, ",\n") {
		t.Fatalf("list-databases sql must select exactly one column:\n%s", sql)
	}
}
