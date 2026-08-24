// Package config loads host configuration. Host is the config key:
// provider, credential source, and connection details vary together per
// host. Provider behavior lives in adapters, shared across hosts.
//
// Hosts that share settings pull them from a named profile through inherit,
// so a value common to several hosts is written once. See inherit.go.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/geraldcsoftware/db-query/internal/credential"
)

// HostConfig describes one configured host. Keys the core doesn't
// understand (encrypt, instance, sslmode, ...) pass through in Extra for
// the adapter to read.
type HostConfig struct {
	Name       string
	Provider   string
	Host       string
	Port       int
	Database   string
	Username   string // literal or resolver URI
	Credential string // resolver URI for the password
	// ReadOnly gates writes against this host (docs/design.md §13.12). It
	// defaults to true: a host that has not said otherwise is treated as one
	// nobody intended to write to. Setting it false does not disable the
	// classifier, only the read-only posture.
	ReadOnly bool
	Extra    map[string]string
	// Origins maps each effective key to the section that supplied it —
	// "host lionel" or "profile eus". Populated for every key, inherited
	// or not.
	Origins map[string]string
}

// BWSConfig configures the Bitwarden Secrets Manager backend. AccessToken is a
// resolver URI (env:, keychain:, …) naming the source of the BWS access token;
// empty falls back to the BWS_ACCESS_TOKEN environment variable.
type BWSConfig struct {
	AccessToken string
}

// Config is the full parsed configuration file. Profiles holds the names of
// declared [profiles.*] sections — they supply shared keys to hosts through
// inherit and are never connectable themselves, so only their names are kept,
// to tell a mistaken --host <profile> from an unknown host.
type Config struct {
	Hosts    map[string]HostConfig
	Profiles map[string]bool
	BWS      BWSConfig
}

// coreKeys are the host-config keys the core interprets; everything else
// passes through to the adapter untouched.
var coreKeys = map[string]bool{
	"provider": true, "host": true, "port": true,
	"database": true, "username": true, "credential": true,
	"inherit": true, "readonly": true,
}

// credentialMistakeKeys are the keys users reach for when they mean
// 'credential'. The password source is a resolver URI under 'credential';
// a value under one of these would otherwise fall through to Extra and be
// silently ignored, leaving the client with no password and prompting
// interactively (which fails in a non-TTY subprocess). Reject it up front
// and point at the right key. Compared case-insensitively.
var credentialMistakeKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "pass": true,
}

// DefaultPath returns the config file location: $DB_QUERY_CONFIG if set,
// else $XDG_CONFIG_HOME/db-query/config.toml, else ~/.config/db-query/config.toml.
func DefaultPath() string {
	if p := os.Getenv("DB_QUERY_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "db-query", "config.toml")
}

// Load parses the TOML config at path.
func Load(path string) (Config, error) {
	var raw struct {
		Hosts    map[string]map[string]any `toml:"hosts"`
		Profiles map[string]map[string]any `toml:"profiles"`
		BWS      struct {
			AccessToken string `toml:"accessToken"`
		} `toml:"bws"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return Config{}, fmt.Errorf("loading config %s: %w", path, err)
	}
	cfg := Config{
		Hosts:    make(map[string]HostConfig, len(raw.Hosts)),
		Profiles: make(map[string]bool, len(raw.Profiles)),
		BWS:      BWSConfig{AccessToken: raw.BWS.AccessToken},
	}
	for name := range raw.Profiles {
		cfg.Profiles[name] = true
	}
	for name, own := range raw.Hosts {
		// Merge the inherit chain first; the switch below then interprets
		// inherited and literal keys identically, blaming whichever section
		// origins says a bad value came from.
		r, err := flatten("host "+name, own, raw.Profiles)
		if err != nil {
			return Config{}, err
		}
		// ReadOnly defaults to true before the merge, so a host that never
		// mentions the key gets the safe posture rather than the zero value.
		h := HostConfig{Name: name, ReadOnly: true, Extra: map[string]string{}, Origins: r.origins}
		for k, v := range r.keys {
			switch k {
			case "provider":
				h.Provider, _ = v.(string)
			case "host":
				h.Host, _ = v.(string)
			case "port":
				switch n := v.(type) {
				case int64:
					h.Port = int(n)
				case string:
					p, err := strconv.Atoi(n)
					if err != nil {
						return Config{}, fmt.Errorf("%s: port %q is not a number", r.origins[k], n)
					}
					h.Port = p
				default:
					return Config{}, fmt.Errorf("%s: port has unsupported type %T", r.origins[k], v)
				}
			case "database":
				h.Database, _ = v.(string)
			case "username":
				h.Username, _ = v.(string)
			case "credential":
				h.Credential, _ = v.(string)
			case "readonly":
				b, err := parseBoolKey(v)
				if err != nil {
					return Config{}, fmt.Errorf("%s: readonly %v", r.origins[k], err)
				}
				h.ReadOnly = b
			default:
				if credentialMistakeKeys[strings.ToLower(k)] {
					return Config{}, fmt.Errorf(
						"%s: unknown key %q — the password source belongs under 'credential' as a resolver URI (e.g. credential = %q)",
						r.origins[k], k, "bws:<secret-id>")
				}
				h.Extra[k] = stringify(v)
			}
		}
		// Checked after merging, so a profile may be the one that supplies it.
		if h.Provider == "" {
			return Config{}, fmt.Errorf("host %s: provider is required", name)
		}
		cfg.Hosts[name] = h
	}
	return cfg, nil
}

// parseBoolKey reads a boolean host key. A misspelling such as readonly = "yes"
// is an error rather than a value quietly ignored: this key decides whether
// writes are permitted, so a config that does not say what it meant must not
// be guessed at.
func parseBoolKey(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(t)
		if err != nil {
			return false, fmt.Errorf("must be true or false, got %q", t)
		}
		return b, nil
	default:
		return false, fmt.Errorf("must be true or false, got %T", v)
	}
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// Host returns the named host or an error listing what is configured.
func (c Config) Host(name string) (HostConfig, error) {
	h, ok := c.Hosts[name]
	if !ok {
		if c.Profiles[name] {
			return HostConfig{}, fmt.Errorf(
				"%q is a profile, not a host — profiles supply shared keys to hosts via inherit (hosts: %v)",
				name, c.HostNames())
		}
		return HostConfig{}, fmt.Errorf("unknown host %q (configured: %v)", name, c.HostNames())
	}
	return h, nil
}

// HostNames returns configured host names, sorted.
func (c Config) HostNames() []string {
	names := make([]string, 0, len(c.Hosts))
	for n := range c.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// MergeCredential fills gaps in host config from a resolved credential's
// Extra bag. Explicit host config always wins; Extra fills gaps only —
// a surprise override buried in a secret item is hard to debug.
func MergeCredential(h HostConfig, cred credential.Credential) HostConfig {
	if h.Host == "" {
		h.Host = cred.Extra["host"]
	}
	if h.Port == 0 {
		if p, err := strconv.Atoi(cred.Extra["port"]); err == nil {
			h.Port = p
		}
	}
	if h.Database == "" {
		h.Database = cred.Extra["database"]
	}
	return h
}
