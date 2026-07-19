package credential

import (
	"fmt"
	"os"
	"strings"
)

// envResolver reads a single environment variable: env:PGPASSWORD.
// With direnv the .env file is already loaded into the environment, so
// this resolver never parses .env files itself.
type envResolver struct{}

func (envResolver) Resolve(rest string) (Credential, error) {
	name := strings.TrimSpace(rest)
	if name == "" {
		return Credential{}, fmt.Errorf("env: credential needs a variable name, e.g. env:PGPASSWORD")
	}
	val, ok := os.LookupEnv(name)
	if !ok || val == "" {
		return Credential{}, fmt.Errorf("env: variable %s is not set or empty", name)
	}
	return Credential{Password: val}, nil
}
