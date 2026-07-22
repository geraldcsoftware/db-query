package credential

import (
	"fmt"
	"strings"
)

// keychainResolver reads a macOS Keychain generic password:
// keychain:<service> or keychain:<service>/<account>. The account, when
// given, doubles as the username.
type keychainResolver struct{}

func (keychainResolver) Resolve(rest string) (Credential, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Credential{}, fmt.Errorf("keychain: credential needs a service, e.g. keychain:<service>[/<account>]")
	}
	service, account, _ := strings.Cut(rest, "/")
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, "-w")
	out, err := runBackend(nil, "security", args...)
	if err != nil {
		return Credential{}, err
	}
	pw := strings.TrimRight(string(out), "\n")
	if pw == "" {
		return Credential{}, fmt.Errorf("keychain: empty password for service %q", service)
	}
	return Credential{Username: account, Password: pw}, nil
}
