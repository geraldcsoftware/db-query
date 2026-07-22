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
// The access token comes from accessToken if set, otherwise from the
// BWS_ACCESS_TOKEN environment variable.
type bwsResolver struct {
	// accessToken is the configured token; empty falls back to the
	// BWS_ACCESS_TOKEN environment variable.
	accessToken string
}

func (r bwsResolver) Resolve(rest string) (Credential, error) {
	id, field, _ := strings.Cut(rest, "#")
	id = strings.TrimSpace(id)
	if id == "" {
		return Credential{}, fmt.Errorf("bws: credential needs a secret id, e.g. bws:<uuid>")
	}
	if field == "" {
		field = "value"
	}
	token := r.accessToken
	if token == "" {
		token = os.Getenv("BWS_ACCESS_TOKEN")
	}
	if token == "" {
		return Credential{}, fmt.Errorf("bws: no access token — set bws.accessToken in config or the BWS_ACCESS_TOKEN environment variable")
	}
	out, err := runBackend(map[string]string{"BWS_ACCESS_TOKEN": token},
		"bws", "secret", "get", id, "--output", "json")
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
