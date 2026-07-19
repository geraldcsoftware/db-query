// Package credential resolves secrets from configured backends into a
// neutral Credential record. Resolvers produce the record; provider
// adapters consume it. Neither side knows about the other.
package credential

import (
	"fmt"
	"strings"
)

// Credential is the neutral record between the resolver seam and the
// adapter seam. Username and Password are the only fields every adapter
// may assume; Extra is an open bag of provider-maybe-useful fields
// (host, port, database, ...) that adapters read if they understand.
type Credential struct {
	Username string
	Password string
	Extra    map[string]string
}

// Resolver resolves the scheme-stripped tail of a credential URI.
type Resolver interface {
	Resolve(rest string) (Credential, error)
}

var resolvers = map[string]Resolver{
	"bws":      bwsResolver{},
	"bw":       bwResolver{},
	"keychain": keychainResolver{},
	"env":      envResolver{},
}

// IsURI reports whether s looks like a credential URI with a known scheme.
// Values without a registered scheme are treated as literals by callers.
func IsURI(s string) bool {
	scheme, _, ok := strings.Cut(s, ":")
	if !ok {
		return false
	}
	_, known := resolvers[scheme]
	return known
}

// Resolve dispatches a credential URI to the resolver registered for its
// scheme. The tail after "scheme:" is that resolver's private business.
func Resolve(uri string) (Credential, error) {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok {
		return Credential{}, fmt.Errorf("credential is not a URI: %q", uri)
	}
	r, ok := resolvers[scheme]
	if !ok {
		return Credential{}, fmt.Errorf("unknown credential scheme: %q", scheme)
	}
	return r.Resolve(rest)
}

// ResolveScalar resolves a URI that names a single value (a username ref,
// for example). Single-value backends (env:, bws:) put their one value in
// Password by convention; multi-field backends may carry a Username.
func ResolveScalar(uri string) (string, error) {
	cred, err := Resolve(uri)
	if err != nil {
		return "", err
	}
	if cred.Password != "" {
		return cred.Password, nil
	}
	if cred.Username != "" {
		return cred.Username, nil
	}
	return "", fmt.Errorf("credential %q resolved to an empty value", uri)
}
