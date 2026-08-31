//go:build integration

// Package integration runs the built db-query binary against real
// databases started by docker-compose.yml (make integration). Two
// suites live here: TestPostgres* (psql) and TestSQLServer* (sqlcmd).
package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	pgPassword    = "pg-integration-pw"
	mssqlPassword = "DbQ-Integration-Pw1"
)

var (
	binPath    string
	configPath string
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "dbq-integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	binPath = filepath.Join(tmp, "db-query")
	build := exec.Command("go", "build", "-o", binPath, "../cmd/db-query")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building db-query:", err)
		os.Exit(1)
	}

	configPath = filepath.Join(tmp, "config.toml")
	cfg := fmt.Sprintf(`
[hosts.pg]
provider   = "postgres"
host       = "127.0.0.1"
port       = %s
database   = "dbqtest"
username   = "dbq"
credential = "env:DBQ_PG_PASSWORD"

[hosts.mssql-master]
provider   = "sqlserver"
host       = "127.0.0.1"
port       = %s
database   = "master"
username   = "sa"
credential = "env:DBQ_MSSQL_PASSWORD"

[hosts.mssql]
provider   = "sqlserver"
host       = "127.0.0.1"
port       = %s
database   = "dbqtest"
username   = "sa"
credential = "env:DBQ_MSSQL_PASSWORD"
`,
		envOr("DBQ_PG_PORT", "15432"),
		envOr("DBQ_MSSQL_PORT", "11433"),
		envOr("DBQ_MSSQL_PORT", "11433"))
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// The tests that exercise credential provision set these per-case;
	// the defaults make the happy paths work.
	os.Setenv("DBQ_PG_PASSWORD", pgPassword)
	os.Setenv("DBQ_MSSQL_PASSWORD", mssqlPassword)

	os.Exit(m.Run())
}

type result struct {
	code   int
	stdout string
	stderr string
}

// envWithout returns os.Environ() minus the named variable, so tests
// can simulate a missing or overridden credential deterministically.
func envWithout(key string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if len(kv) > len(key) && kv[:len(key)+1] == key+"=" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runTool executes the built binary. env == nil inherits the test
// process environment; otherwise the given environment is used as-is.
func runTool(t testing.TB, env []string, args ...string) result {
	t.Helper()
	full := append([]string{args[0], "--config", configPath}, args[1:]...)
	cmd := exec.Command(binPath, full...)
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running db-query: %v", err)
	}
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// seedMSSQL runs one batch straight through sqlcmd, connecting the way the
// adapter does: every detail through the SQLCMD* overlay, nothing on argv, so
// the fixture and the subject under test reach the same server the same way.
// -b matches the adapter too, making a batch error exit nonzero.
//
// It bypasses db-query deliberately. See the note in mssqlReady for why the
// fixture cannot be built with the tool itself.
func seedMSSQL(t testing.TB, database, sql string) {
	t.Helper()
	cmd := exec.Command("sqlcmd", "-b", "-Q", sql)
	cmd.Env = append(os.Environ(),
		"SQLCMDSERVER=tcp:127.0.0.1,"+envOr("DBQ_MSSQL_PORT", "11433"),
		"SQLCMDUSER=sa",
		"SQLCMDPASSWORD="+mssqlPassword,
		"SQLCMDDBNAME="+database,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seeding %s failed: %v\n%s", database, err, out)
	}
}

// waitReady polls a trivial query until the database accepts it.
func waitReady(t testing.TB, host string, deadline time.Duration) {
	t.Helper()
	start := time.Now()
	var last result
	for time.Since(start) < deadline {
		last = runTool(t, nil, "query", "--host", host, "--timeout", "10s", "SELECT 1 AS ready")
		if last.code == 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s not ready after %s.\nIs the stack up? Run: make integration-up\nlast stderr: %s",
		host, deadline, last.stderr)
}
