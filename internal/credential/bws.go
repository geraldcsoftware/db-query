package credential

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// bwsResolver reads a Bitwarden Secrets Manager secret: bws:<secret-id>,
// optionally bws:<secret-id>#<field> to pick a field of the secret JSON
// (default "value"). A BWS secret is a single value — host config holds
// two refs (one user, one password) rather than structured JSON in one.
// Requires BWS_ACCESS_TOKEN in the environment, sourced outside db-query.
type bwsResolver struct{}

func (bwsResolver) Resolve(rest string) (Credential, error) {
	id, field, _ := strings.Cut(rest, "#")
	id = strings.TrimSpace(id)
	if id == "" {
		return Credential{}, fmt.Errorf("bws: credential needs a secret id, e.g. bws:<uuid>")
	}
	if field == "" {
		field = "value"
	}
	if os.Getenv("BWS_ACCESS_TOKEN") == "" {
		return Credential{}, fmt.Errorf("bws: BWS_ACCESS_TOKEN is not set (source it outside db-query)")
	}
	out, err := runBackend("bws", "secret", "get", id, "--output", "json")
	if err != nil {
		return Credential{}, err
	}
	var secret map[string]any
	if err := json.Unmarshal(out, &secret); err != nil {
		return Credential{}, fmt.Errorf("bws: unexpected output for secret %s: %w", id, err)
	}
	val, ok := secret[field].(string)
	if !ok || val == "" {
		return Credential{}, fmt.Errorf("bws: secret %s has no %q field", id, field)
	}
	return Credential{Password: val}, nil
}
