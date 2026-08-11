package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T) string {
	t.Helper()
	return writeFile(t, t.TempDir(), "config.toml", `
[hosts.testpg]
provider   = "postgres"
host       = "localhost"
port       = 5432
database   = "testdb"
username   = "app"
credential = "env:DBQ_SESSION_TEST_PW"
`)
}

func TestSetupHappyPath(t *testing.T) {
	t.Setenv("DBQ_SESSION_TEST_PW", "pw")
	cfg := testConfig(t)
	var errb strings.Builder
	r, code := Setup(CommonFlags{Host: "testpg", Config: cfg, Output: "text", Timeout: time.Second}, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if r.Adapter.Name() != "postgres" {
		t.Fatalf("adapter = %q, want postgres", r.Adapter.Name())
	}
	if r.Host.Database != "testdb" {
		t.Fatalf("database = %q, want testdb", r.Host.Database)
	}
	if r.Cred.Password != "pw" {
		t.Fatalf("password = %q, want pw", r.Cred.Password)
	}
}

func TestSetupMissingHost(t *testing.T) {
	var errb strings.Builder
	_, code := Setup(CommonFlags{Output: "text", Timeout: time.Second}, &errb)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errb.String(), "--host is required") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestSetupUnknownHost(t *testing.T) {
	cfg := testConfig(t)
	var errb strings.Builder
	_, code := Setup(CommonFlags{Host: "nope", Config: cfg, Output: "text", Timeout: time.Second}, &errb)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errb.String(), `unknown host "nope"`) {
		t.Fatalf("stderr = %q", errb.String())
	}
}
