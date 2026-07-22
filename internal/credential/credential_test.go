package credential

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolveDispatch(t *testing.T) {
	t.Run("not a URI", func(t *testing.T) {
		_, err := Resolve("just-a-password")
		if err == nil || !strings.Contains(err.Error(), "not a URI") {
			t.Fatalf("want not-a-URI error, got %v", err)
		}
	})
	t.Run("unknown scheme", func(t *testing.T) {
		_, err := Resolve("vault:secret/foo")
		if err == nil || !strings.Contains(err.Error(), "unknown credential scheme") {
			t.Fatalf("want unknown-scheme error, got %v", err)
		}
	})
	t.Run("env scheme dispatches", func(t *testing.T) {
		t.Setenv("DBQ_TEST_SECRET", "hunter2")
		cred, err := Resolve("env:DBQ_TEST_SECRET")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Password != "hunter2" {
			t.Fatalf("password = %q, want hunter2", cred.Password)
		}
	})
}

func TestIsURI(t *testing.T) {
	cases := map[string]bool{
		"env:FOO":            true,
		"bws:uuid#password":  true,
		"bw:item/x":          true,
		"keychain:svc/acct":  true,
		"literaluser":        false,
		"vault:not-a-scheme": false,
		"":                   false,
	}
	for in, want := range cases {
		if got := IsURI(in); got != want {
			t.Errorf("IsURI(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEnvResolver(t *testing.T) {
	t.Run("missing var", func(t *testing.T) {
		_, err := Resolve("env:DBQ_DEFINITELY_UNSET_VAR")
		if err == nil || !strings.Contains(err.Error(), "not set or empty") {
			t.Fatalf("want not-set error, got %v", err)
		}
	})
	t.Run("empty var name", func(t *testing.T) {
		_, err := Resolve("env:")
		if err == nil {
			t.Fatal("want error for empty name")
		}
	})
	t.Run("empty value", func(t *testing.T) {
		t.Setenv("DBQ_EMPTY", "")
		_, err := Resolve("env:DBQ_EMPTY")
		if err == nil {
			t.Fatal("want error for empty value")
		}
	})
}

func TestResolveScalar(t *testing.T) {
	t.Setenv("DBQ_USERREF", "core_app")
	got, err := ResolveScalar("env:DBQ_USERREF")
	if err != nil {
		t.Fatal(err)
	}
	if got != "core_app" {
		t.Fatalf("scalar = %q, want core_app", got)
	}
	if _, err := ResolveScalar("nope"); err == nil {
		t.Fatal("want error for non-URI")
	}
}

// withBackend swaps the backend runner for one test.
func withBackend(t *testing.T, fn func(env map[string]string, name string, args ...string) ([]byte, error)) {
	t.Helper()
	orig := runBackend
	runBackend = fn
	t.Cleanup(func() { runBackend = orig })
}

func TestRunBackendEnvOverlay(t *testing.T) {
	// A real subprocess echoes an overlaid var, proving env reaches the child.
	out, err := runBackend(map[string]string{"DBQ_OVERLAY": "yes"},
		"sh", "-c", `printf '%s' "$DBQ_OVERLAY"`)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "yes" {
		t.Fatalf("overlay not applied: got %q", out)
	}
}

func TestBwsResolver(t *testing.T) {
	t.Run("requires access token", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "")
		_, err := Resolve("bws:some-uuid")
		if err == nil || !strings.Contains(err.Error(), "BWS_ACCESS_TOKEN") {
			t.Fatalf("want token error, got %v", err)
		}
	})
	t.Run("resolves value field", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "tok")
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			if name != "bws" {
				t.Fatalf("backend = %q, want bws", name)
			}
			return []byte(`{"key":"db-pass","value":"s3cret"}`), nil
		})
		cred, err := Resolve("bws:1a2b-3c4d")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Password != "s3cret" {
			t.Fatalf("password = %q", cred.Password)
		}
	})
	t.Run("fragment selects field", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "tok")
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			return []byte(`{"key":"the-user","value":"x"}`), nil
		})
		cred, err := Resolve("bws:1a2b#key")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Password != "the-user" {
			t.Fatalf("password = %q, want the-user", cred.Password)
		}
	})
	t.Run("empty id", func(t *testing.T) {
		_, err := Resolve("bws:")
		if err == nil {
			t.Fatal("want error for empty id")
		}
	})
	t.Run("backend failure surfaces", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "tok")
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("bws: 404 secret not found")
		})
		_, err := Resolve("bws:missing")
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("want backend error, got %v", err)
		}
	})
}

func TestBwsResolverConfiguredToken(t *testing.T) {
	t.Run("configured token is passed to the subprocess", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "") // no env token; only the configured one
		var seen string
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			seen = env["BWS_ACCESS_TOKEN"]
			return []byte(`{"key":"k","value":"v"}`), nil
		})
		cred, err := ResolveWith("bws:1a2b", Options{BWSAccessToken: "from-config"})
		if err != nil {
			t.Fatal(err)
		}
		if cred.Password != "v" {
			t.Fatalf("password = %q", cred.Password)
		}
		if seen != "from-config" {
			t.Fatalf("subprocess token = %q, want from-config", seen)
		}
	})
	t.Run("falls back to env when no configured token", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "from-env")
		var seen string
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			seen = env["BWS_ACCESS_TOKEN"]
			return []byte(`{"key":"k","value":"v"}`), nil
		})
		if _, err := ResolveWith("bws:1a2b", Options{}); err != nil {
			t.Fatal(err)
		}
		if seen != "from-env" {
			t.Fatalf("subprocess token = %q, want from-env", seen)
		}
	})
	t.Run("neither source set errors naming both", func(t *testing.T) {
		t.Setenv("BWS_ACCESS_TOKEN", "")
		_, err := ResolveWith("bws:1a2b", Options{})
		if err == nil || !strings.Contains(err.Error(), "BWS_ACCESS_TOKEN") ||
			!strings.Contains(err.Error(), "bws.accessToken") {
			t.Fatalf("want error naming both sources, got %v", err)
		}
	})
}

func TestBwResolver(t *testing.T) {
	t.Run("requires session", func(t *testing.T) {
		t.Setenv("BW_SESSION", "")
		_, err := Resolve("bw:item/mydb")
		if err == nil || !strings.Contains(err.Error(), "BW_SESSION") {
			t.Fatalf("want session error, got %v", err)
		}
	})
	t.Run("parses item with fields into extra", func(t *testing.T) {
		t.Setenv("BW_SESSION", "sess")
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			return []byte(`{"login":{"username":"svc","password":"pw"},
				"fields":[{"name":"Host","value":"db.internal"},{"name":"port","value":"5433"}]}`), nil
		})
		cred, err := Resolve("bw:item/mydb")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Username != "svc" || cred.Password != "pw" {
			t.Fatalf("cred = %+v", cred)
		}
		if cred.Extra["host"] != "db.internal" || cred.Extra["port"] != "5433" {
			t.Fatalf("extra = %+v (field names should be lowercased)", cred.Extra)
		}
	})
	t.Run("missing password", func(t *testing.T) {
		t.Setenv("BW_SESSION", "sess")
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			return []byte(`{"login":{"username":"svc","password":""}}`), nil
		})
		if _, err := Resolve("bw:item/mydb"); err == nil {
			t.Fatal("want error for missing password")
		}
	})
	t.Run("empty selector", func(t *testing.T) {
		if _, err := Resolve("bw:item/"); err == nil {
			t.Fatal("want error for empty selector")
		}
	})
}

func TestKeychainResolver(t *testing.T) {
	t.Run("service and account", func(t *testing.T) {
		var gotArgs []string
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			if name != "security" {
				t.Fatalf("backend = %q, want security", name)
			}
			gotArgs = args
			return []byte("kc-pass\n"), nil
		})
		cred, err := Resolve("keychain:reporting-sql/svc_reports")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Username != "svc_reports" || cred.Password != "kc-pass" {
			t.Fatalf("cred = %+v", cred)
		}
		want := []string{"find-generic-password", "-s", "reporting-sql", "-a", "svc_reports", "-w"}
		if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
			t.Fatalf("args = %v, want %v", gotArgs, want)
		}
	})
	t.Run("service only", func(t *testing.T) {
		withBackend(t, func(env map[string]string, name string, args ...string) ([]byte, error) {
			return []byte("pw\n"), nil
		})
		cred, err := Resolve("keychain:mysvc")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Username != "" || cred.Password != "pw" {
			t.Fatalf("cred = %+v", cred)
		}
	})
	t.Run("empty rest", func(t *testing.T) {
		if _, err := Resolve("keychain:"); err == nil {
			t.Fatal("want error for empty service")
		}
	})
}
