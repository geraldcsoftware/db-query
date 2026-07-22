package credential

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// bwResolver reads a Bitwarden CLI vault item: bw:item/<item-id-or-name>.
// The item JSON carries username, password, and custom fields, so this
// resolver can populate Extra richly. Needs an unlocked vault
// (BW_SESSION); the locked case must be a clear error, not a hang.
type bwResolver struct{}

func (bwResolver) Resolve(rest string) (Credential, error) {
	sel := strings.TrimPrefix(strings.TrimSpace(rest), "item/")
	if sel == "" {
		return Credential{}, fmt.Errorf("bw: credential needs an item, e.g. bw:item/<id-or-name>")
	}
	if os.Getenv("BW_SESSION") == "" {
		return Credential{}, fmt.Errorf("bw: BW_SESSION is not set — unlock the vault first (bw unlock)")
	}
	out, err := runBackend(nil, "bw", "get", "item", sel, "--nointeraction")
	if err != nil {
		return Credential{}, err
	}
	var item struct {
		Login struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"login"`
		Fields []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return Credential{}, fmt.Errorf("bw: unexpected output for item %s: %w", sel, err)
	}
	cred := Credential{
		Username: item.Login.Username,
		Password: item.Login.Password,
	}
	if len(item.Fields) > 0 {
		cred.Extra = make(map[string]string, len(item.Fields))
		for _, f := range item.Fields {
			if f.Name != "" {
				cred.Extra[strings.ToLower(f.Name)] = f.Value
			}
		}
	}
	if cred.Password == "" {
		return Credential{}, fmt.Errorf("bw: item %s has no login password", sel)
	}
	return cred, nil
}
