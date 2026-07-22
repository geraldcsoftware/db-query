// Package config loads host configuration. Host is the config key:
// provider, credential source, and connection details vary together per
// host. Provider behavior lives in adapters, shared across hosts.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

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
	Extra      map[string]string
}

// BWSConfig configures the Bitwarden Secrets Manager backend. AccessToken is a
// resolver URI (env:, keychain:, …) naming the source of the BWS access token;
// empty falls back to the BWS_ACCESS_TOKEN environment variable.
type BWSConfig struct {
	AccessToken string
}

// Config is the full parsed configuration file.
type Config struct {
	Hosts map[string]HostConfig
	BWS   BWSConfig
}

// coreKeys are the host-config keys the core interprets; everything else
// passes through to the adapter untouched.
var coreKeys = map[string]bool{
	"provider": true, "host": true, "port": true,
	"database": true, "username": true, "credential": true,
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
		Hosts map[string]map[string]any `toml:"hosts"`
		BWS   struct {
			AccessToken string `toml:"accessToken"`
		} `toml:"bws"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return Config{}, fmt.Errorf("loading config %s: %w", path, err)
	}
	cfg := Config{Hosts: make(map[string]HostConfig, len(raw.Hosts)), BWS: BWSConfig{AccessToken: raw.BWS.AccessToken}}
	for name, keys := range raw.Hosts {
		h := HostConfig{Name: name, Extra: map[string]string{}}
		for k, v := range keys {
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
						return Config{}, fmt.Errorf("host %s: port %q is not a number", name, n)
					}
					h.Port = p
				default:
					return Config{}, fmt.Errorf("host %s: port has unsupported type %T", name, v)
				}
			case "database":
				h.Database, _ = v.(string)
			case "username":
				h.Username, _ = v.(string)
			case "credential":
				h.Credential, _ = v.(string)
			default:
				h.Extra[k] = stringify(v)
			}
		}
		if h.Provider == "" {
			return Config{}, fmt.Errorf("host %s: provider is required", name)
		}
		cfg.Hosts[name] = h
	}
	return cfg, nil
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
