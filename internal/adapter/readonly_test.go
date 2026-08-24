package adapter

import (
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
)

func TestPostgresReadOnlySetsPGOPTIONS(t *testing.T) {
	cred := credential.Credential{Username: "u", Password: "p"}
	for _, tt := range []struct {
		name     string
		readOnly bool
		want     string
	}{
		{"read-only host", true, "-c default_transaction_read_only=on"},
		{"writable host", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := postgresAdapter{}.Env(cred, config.HostConfig{Host: "h", Database: "d", ReadOnly: tt.readOnly})
			if got := env["PGOPTIONS"]; got != tt.want {
				t.Errorf("PGOPTIONS: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresRefusesUnquotedInterpolation(t *testing.T) {
	// The vector this closes: the SQL text is clean, the payload rides in the
	// value, and no classifier can see it.
	params := map[string]string{"id": "1; DROP TABLE victims", "who": "Ada"}
	refuse := []struct{ name, sql string }{
		{"bare param", "SELECT * FROM t WHERE id = :id"},
		{"bare param among others", "SELECT * FROM t WHERE a = :'who' AND id = :id"},
		{"after a cast", "SELECT a::text FROM t WHERE id = :id"},
	}
	for _, tt := range refuse {
		t.Run(tt.name, func(t *testing.T) {
			_, err := postgresAdapter{}.Build(config.HostConfig{}, Query{SQL: tt.sql, Params: params})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			// §9: the message names the parameter, never its value.
			if strings.Contains(err.Error(), "DROP TABLE victims") {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestPostgresAllowsQuotedInterpolation(t *testing.T) {
	params := map[string]string{"who": "Ada", "id": "1"}
	allow := []struct{ name, sql string }{
		{"quoted literal form", "SELECT * FROM t WHERE name = :'who'"},
		{"quoted identifier form", `SELECT * FROM :"who"`},
		{"a cast is not an interpolation", "SELECT a::text, b::int FROM t"},
		{"a cast feeding a json operator", "SELECT col::jsonb->>'someProperty' FROM t"},
		{"a schema-qualified cast", "SELECT col::public.mytype FROM t"},
		{"nested casts", "SELECT (col::text)::int FROM t"},
		{"a time format is a literal, not a parameter", "SELECT to_char(created, 'DD/MM HH:MI:SS') FROM t"},
		{"an interval literal", "SELECT interval '1:30' FROM t"},
		{"an array slice", "SELECT arr[1:3] FROM t"},
		{"an array slice with the lower bound omitted", "SELECT arr[:3] FROM t"},
		{"an array slice with the upper bound omitted", "SELECT arr[2:] FROM t"},
		{"chained array slices", "SELECT arr[1:3][2:4] FROM t"},
		// `id` is bound, and := is named-argument notation rather than a
		// reference to it.
		{"named-argument notation", "SELECT f(id := 1) FROM t"},
		{"named-argument notation without spaces", "SELECT f(id:=1) FROM t"},
		{"a colon inside a literal is data", "SELECT ':id' AS s FROM t"},
		{"a colon in a literal with a space after it", "SELECT 'some: thing' AS s FROM t"},
		{"a cast written inside a literal", "SELECT 'a::b' AS s FROM t"},
		{"a colon inside a comment is not code", "-- :id\nSELECT 1"},
		{"a colon in a block comment", "/* :id */ SELECT 1"},
		{"a colon in a dollar-quoted body", "SELECT $tag$ :id $tag$"},
		{"an unbound name is never interpolated", "SELECT * FROM t WHERE x = :notbound"},
		{"no params at all", "SELECT * FROM t WHERE id = :id"},
	}
	for _, tt := range allow {
		t.Run(tt.name, func(t *testing.T) {
			p := params
			if tt.name == "no params at all" {
				p = nil
			}
			if _, err := (postgresAdapter{}).Build(config.HostConfig{}, Query{SQL: tt.sql, Params: p}); err != nil {
				t.Errorf("falsely refused: %v", err)
			}
		})
	}
}
