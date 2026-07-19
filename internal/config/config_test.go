package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/credential"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeConfig(t, `
[hosts.prod-core]
provider   = "postgres"
host       = "core.internal"
port       = 5432
database   = "core"
username   = "bws:1a2b-user"
credential = "bws:1a2b-3c4d#password"

[hosts.reporting]
provider   = "sqlserver"
host       = "sql01.internal"
database   = "reports"
username   = "keychain:reporting-sql/svc_reports"
credential = "keychain:reporting-sql"
encrypt    = true
instance   = "SQLEXPRESS"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := cfg.Host("prod-core")
	if err != nil {
		t.Fatal(err)
	}
	if pc.Provider != "postgres" || pc.Host != "core.internal" || pc.Port != 5432 ||
		pc.Database != "core" || pc.Username != "bws:1a2b-user" ||
		pc.Credential != "bws:1a2b-3c4d#password" {
		t.Fatalf("prod-core = %+v", pc)
	}
	if len(pc.Extra) != 0 {
		t.Fatalf("prod-core extra = %+v, want empty", pc.Extra)
	}

	rep, _ := cfg.Host("reporting")
	if rep.Extra["encrypt"] != "true" || rep.Extra["instance"] != "SQLEXPRESS" {
		t.Fatalf("adapter keys must pass through untouched, got %+v", rep.Extra)
	}
	if rep.Port != 0 {
		t.Fatalf("port = %d, want 0 (unset)", rep.Port)
	}

	if got := cfg.HostNames(); !reflect.DeepEqual(got, []string{"prod-core", "reporting"}) {
		t.Fatalf("HostNames = %v", got)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Run("missing provider", func(t *testing.T) {
		path := writeConfig(t, "[hosts.x]\nhost = \"h\"\n")
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "provider is required") {
			t.Fatalf("want provider error, got %v", err)
		}
	})
	t.Run("bad port", func(t *testing.T) {
		path := writeConfig(t, "[hosts.x]\nprovider = \"postgres\"\nport = \"abc\"\n")
		if _, err := Load(path); err == nil {
			t.Fatal("want port error")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
			t.Fatal("want error for missing file")
		}
	})
	t.Run("unknown host lists configured", func(t *testing.T) {
		path := writeConfig(t, "[hosts.a]\nprovider = \"postgres\"\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = cfg.Host("b")
		if err == nil || !strings.Contains(err.Error(), "a") {
			t.Fatalf("error should list configured hosts, got %v", err)
		}
	})
}

func TestPortAsString(t *testing.T) {
	path := writeConfig(t, "[hosts.x]\nprovider = \"postgres\"\nport = \"5433\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.Host("x")
	if h.Port != 5433 {
		t.Fatalf("port = %d", h.Port)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Run("explicit env wins", func(t *testing.T) {
		t.Setenv("DB_QUERY_CONFIG", "/etc/custom.toml")
		if got := DefaultPath(); got != "/etc/custom.toml" {
			t.Fatalf("path = %q", got)
		}
	})
	t.Run("xdg fallback", func(t *testing.T) {
		t.Setenv("DB_QUERY_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		want := filepath.Join("/xdg", "db-query", "config.toml")
		if got := DefaultPath(); got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
	})
}

func TestMergeCredential(t *testing.T) {
	cred := credential.Credential{Extra: map[string]string{
		"host": "from-secret", "port": "9999", "database": "from-secret-db",
	}}
	t.Run("explicit host config wins", func(t *testing.T) {
		h := HostConfig{Host: "explicit", Port: 5432, Database: "explicit-db"}
		got := MergeCredential(h, cred)
		if got.Host != "explicit" || got.Port != 5432 || got.Database != "explicit-db" {
			t.Fatalf("merged = %+v (extra must not override explicit config)", got)
		}
	})
	t.Run("extra fills gaps", func(t *testing.T) {
		got := MergeCredential(HostConfig{}, cred)
		if got.Host != "from-secret" || got.Port != 9999 || got.Database != "from-secret-db" {
			t.Fatalf("merged = %+v", got)
		}
	})
	t.Run("no extra at all", func(t *testing.T) {
		got := MergeCredential(HostConfig{Host: "h"}, credential.Credential{})
		if got.Host != "h" || got.Port != 0 {
			t.Fatalf("merged = %+v", got)
		}
	})
}
