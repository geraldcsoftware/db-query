package config

import (
	"strings"
	"testing"
)

// A config with no profiles and no inherit must behave exactly as before.
func TestLoadWithoutProfilesIsUnchanged(t *testing.T) {
	path := writeConfig(t, `
[hosts.solo]
provider = "postgres"
host     = "solo.internal"
database = "core"
sslmode  = "require"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("profiles = %v, want none", cfg.Profiles)
	}
	h, _ := cfg.Host("solo")
	if h.Provider != "postgres" || h.Host != "solo.internal" || h.Database != "core" {
		t.Fatalf("solo = %+v", h)
	}
	if h.Extra["sslmode"] != "require" {
		t.Fatalf("extra = %+v", h.Extra)
	}
	if got := h.Origins["provider"]; got != "host solo" {
		t.Fatalf("origin of provider = %q, want %q", got, "host solo")
	}
}

func TestInheritSingleLevel(t *testing.T) {
	path := writeConfig(t, `
[profiles.pg]
provider   = "postgres"
database   = "postgres"
username   = "postgres"
credential = "bws:shared"

[hosts.node-a]
inherit = "pg"
host    = "10.0.0.1"

[hosts.node-b]
inherit = "pg"
host    = "10.0.0.2"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, addr string }{{"node-a", "10.0.0.1"}, {"node-b", "10.0.0.2"}} {
		h, err := cfg.Host(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		if h.Provider != "postgres" || h.Database != "postgres" ||
			h.Username != "postgres" || h.Credential != "bws:shared" || h.Host != tc.addr {
			t.Fatalf("%s = %+v", tc.name, h)
		}
	}
}

// A profile may itself inherit, and the nearest ancestor wins.
func TestInheritChainNearestWins(t *testing.T) {
	path := writeConfig(t, `
[profiles.base]
provider = "postgres"
database = "postgres"
username = "root"

[profiles.eus]
inherit  = "base"
username = "gchifanzwa"

[hosts.lionel]
inherit = "eus"
host    = "lionel.eus.v.co.zw"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.Host("lionel")
	if h.Username != "gchifanzwa" {
		t.Fatalf("username = %q, want the nearer profile's value", h.Username)
	}
	if h.Provider != "postgres" || h.Database != "postgres" {
		t.Fatalf("root profile keys lost: %+v", h)
	}
	want := map[string]string{
		"provider": "profile base",
		"database": "profile base",
		"username": "profile eus",
		"host":     "host lionel",
	}
	for k, v := range want {
		if h.Origins[k] != v {
			t.Errorf("origin of %s = %q, want %q", k, h.Origins[k], v)
		}
	}
}

func TestInheritHostOverridesProfile(t *testing.T) {
	path := writeConfig(t, `
[profiles.pg]
provider = "postgres"
database = "postgres"
port     = 5432

[hosts.odd]
inherit  = "pg"
host     = "odd.internal"
database = "reporting"
port     = 6432
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.Host("odd")
	if h.Database != "reporting" || h.Port != 6432 {
		t.Fatalf("host keys must win, got %+v", h)
	}
	if h.Origins["database"] != "host odd" {
		t.Fatalf("origin of database = %q", h.Origins["database"])
	}
}

// Adapter keys merge one at a time rather than wholesale, so a host adding an
// Extra key keeps the ones its profile supplied.
func TestInheritExtraMergesKeyByKey(t *testing.T) {
	path := writeConfig(t, `
[profiles.mssql]
provider = "sqlserver"
encrypt  = true
sslmode  = "require"

[hosts.urukhai]
inherit  = "mssql"
host     = "urukhai.internal"
instance = "SQLEXPRESS"
sslmode  = "disable"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.Host("urukhai")
	if h.Extra["encrypt"] != "true" {
		t.Errorf("profile extra key lost: %+v", h.Extra)
	}
	if h.Extra["instance"] != "SQLEXPRESS" {
		t.Errorf("host extra key lost: %+v", h.Extra)
	}
	if h.Extra["sslmode"] != "disable" {
		t.Errorf("sslmode = %q, want the host's value", h.Extra["sslmode"])
	}
}

// provider is validated after merging, so a profile may supply it.
func TestInheritProviderFromProfile(t *testing.T) {
	path := writeConfig(t, `
[profiles.pg]
provider = "postgres"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := cfg.Host("node"); h.Provider != "postgres" {
		t.Fatalf("provider = %q", h.Provider)
	}
}

func TestInheritErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{
			name: "unknown profile",
			body: `
[profiles.pg]
provider = "postgres"

[hosts.node]
inherit = "typo"
`,
			want: []string{"host node", "unknown profile", "typo", "pg"},
		},
		{
			name: "self cycle",
			body: `
[profiles.a]
inherit  = "a"
provider = "postgres"

[hosts.node]
inherit = "a"
`,
			want: []string{"cycle", "a"},
		},
		{
			name: "indirect cycle",
			body: `
[profiles.a]
inherit = "b"
[profiles.b]
inherit = "c"
[profiles.c]
inherit = "a"

[hosts.node]
inherit = "a"
`,
			want: []string{"cycle", "a", "b", "c"},
		},
		{
			name: "empty inherit",
			body: `
[hosts.node]
inherit  = ""
provider = "postgres"
`,
			want: []string{"host node", "inherit is empty"},
		},
		{
			name: "inherit is not a string",
			body: `
[hosts.node]
inherit  = ["a", "b"]
provider = "postgres"
`,
			want: []string{"host node", "inherit must be a profile name"},
		},
		{
			name: "provider missing after merge",
			body: `
[profiles.pg]
database = "postgres"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`,
			want: []string{"host node", "provider is required"},
		},
		{
			// The credential-typo trap must fire on inherited keys too, and
			// name the profile that actually carries the mistake.
			name: "password key inside a profile",
			body: `
[profiles.pg]
provider = "postgres"
password = "hunter2"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`,
			want: []string{"profile pg", "credential"},
		},
		{
			name: "unparseable port inside a profile",
			body: `
[profiles.pg]
provider = "postgres"
port     = "abc"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`,
			want: []string{"profile pg", "port", "not a number"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// A profile is not connectable; naming one as a host says so rather than
// listing it among the unknown-host candidates.
func TestProfileIsNotAHost(t *testing.T) {
	path := writeConfig(t, `
[profiles.pg]
provider = "postgres"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Host("pg"); err == nil || !strings.Contains(err.Error(), "is a profile") {
		t.Fatalf("Host(\"pg\") error = %v, want a profile-not-host error", err)
	}
	if _, ok := cfg.Hosts["pg"]; ok {
		t.Fatal("a profile must not appear among the hosts")
	}
	if len(cfg.HostNames()) != 1 {
		t.Fatalf("HostNames() = %v, want only the real host", cfg.HostNames())
	}
}

// inherit is consumed by the loader and must never reach an adapter.
func TestInheritDoesNotLeakIntoExtra(t *testing.T) {
	path := writeConfig(t, `
[profiles.pg]
provider = "postgres"

[hosts.node]
inherit = "pg"
host    = "node.internal"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.Host("node")
	if _, ok := h.Extra["inherit"]; ok {
		t.Fatalf("inherit leaked into Extra: %+v", h.Extra)
	}
}
