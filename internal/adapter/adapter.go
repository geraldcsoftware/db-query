// Package adapter quarantines everything provider-specific: SQL
// delivery, credential env vars, error meaning, output shape. Round
// trip: adapter builds → executor runs → adapter parses. Adding a third
// client is one new adapter, zero executor changes.
package adapter

import (
	"fmt"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/credential"
	"github.com/geraldcsoftware/db-query/internal/executor"
)

// Query is provider-native SQL plus its parameter values. Params are
// written in native placeholder syntax in the SQL (:'name' for psql,
// $(name) for sqlcmd) and always bound via the client's own -v — Go
// never substitutes values into SQL text.
type Query struct {
	SQL    string
	Params map[string]string
}

// Rows is the neutral rowset every adapter parses into and every
// renderer renders from. A nil *string is SQL NULL; &"" is the empty
// string.
type Rows struct {
	Columns []string
	Rows    [][]*string
}

// Adapter is one provider's build/parse pair around the central executor.
type Adapter interface {
	Name() string
	Env(cred credential.Credential, host config.HostConfig) map[string]string
	Build(host config.HostConfig, q Query) (executor.Invocation, error)
	Parse(r executor.RawResult) (Rows, error)
	IsSchemaError(r executor.RawResult) bool // gates re-introspection
	IntrospectSQL() string                   // lists user tables + columns
	ListDatabasesSQL() string                // lists connectable database names
}

var adapters = map[string]Adapter{
	"postgres":  postgresAdapter{},
	"sqlserver": sqlserverAdapter{},
}

// For returns the adapter for a provider name.
func For(provider string) (Adapter, error) {
	a, ok := adapters[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (supported: postgres, sqlserver)", provider)
	}
	return a, nil
}

// sortedKeys gives deterministic argv ordering for map-carried params.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
