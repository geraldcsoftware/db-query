package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/render"
)

// queryOut runs a one-row query through the psql stub and returns stdout.
func queryOut(t *testing.T, extra ...string) string {
	t.Helper()
	seedSchemaCache(t)
	needsClassifier(t)
	fakePsql(t, `printf 'id,name\n1,Ada\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	args := append([]string{"query", "--host", "testpg", "--config", cfg}, extra...)
	args = append(args, "SELECT 1")
	code, out, errb := run(t, args...)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	return out
}

func TestResolveOutput(t *testing.T) {
	t.Run("explicit formats pass through", func(t *testing.T) {
		for _, f := range []string{"text", "json", "table"} {
			if got := resolveOutput(f, os.Stdout); got != f {
				t.Errorf("resolveOutput(%q) = %q, want unchanged", f, got)
			}
		}
	})

	t.Run("auto resolves to text for a non-file writer", func(t *testing.T) {
		var b strings.Builder
		if got := resolveOutput(render.AutoFormat, &b); got != "text" {
			t.Fatalf("auto on a buffer = %q, want text", got)
		}
	})

	t.Run("auto resolves to text for a pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		if got := resolveOutput(render.AutoFormat, w); got != "text" {
			t.Fatalf("auto on a pipe = %q, want text", got)
		}
	})

	t.Run("auto resolves to text for a regular file", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "out")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if got := resolveOutput(render.AutoFormat, f); got != "text" {
			t.Fatalf("auto on a redirect = %q, want text", got)
		}
	})

	t.Run("auto resolves to table on a character device", func(t *testing.T) {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Skipf("no %s available: %v", os.DevNull, err)
		}
		defer f.Close()
		// /dev/null is a character device, the same class as a tty — this is
		// the positive half of the ModeCharDevice probe, which no pipe or
		// regular file can exercise.
		if got := resolveOutput(render.AutoFormat, f); got != "table" {
			t.Fatalf("auto on a char device = %q, want table", got)
		}
	})
}

func TestIsTerminal(t *testing.T) {
	t.Run("false for a buffer", func(t *testing.T) {
		var b strings.Builder
		if isTerminal(&b) {
			t.Fatal("a strings.Builder must not report as a terminal")
		}
	})

	t.Run("false for a pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		if isTerminal(w) {
			t.Fatal("a pipe must not report as a terminal")
		}
	})

	t.Run("true for a character device", func(t *testing.T) {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Skipf("no %s available: %v", os.DevNull, err)
		}
		defer f.Close()
		if !isTerminal(f) {
			t.Fatal("a character device must report as a terminal")
		}
	})
}

// TestAutoIsTextWhenNotATTY is the regression guard that matters most: the
// default format must stay byte-identical to the pre-table behaviour whenever
// output is not a terminal, so pipes, redirects and agent callers are unaffected.
func TestAutoIsTextWhenNotATTY(t *testing.T) {
	if got := queryOut(t); got != "id\tname\n1\tAda\n" {
		t.Fatalf("default output = %q, want tab-separated text", got)
	}
}

func TestQueryTableOutput(t *testing.T) {
	got := queryOut(t, "--output", "table")
	want := "" +
		"+----+------+\n" +
		"| id | name |\n" +
		"+----+------+\n" +
		"| 1  | Ada  |\n" +
		"+----+------+\n" +
		"(1 row)\n"
	if got != want {
		t.Fatalf("table output =\n%s\nwant:\n%s", got, want)
	}
}

func TestQueryTableNoHeaders(t *testing.T) {
	got := queryOut(t, "--output", "table", "--no-headers")
	want := "" +
		"+---+-----+\n" +
		"| 1 | Ada |\n" +
		"+---+-----+\n"
	if got != want {
		t.Fatalf("no-headers table =\n%s\nwant:\n%s", got, want)
	}
}

func TestQueryMaxColWidth(t *testing.T) {
	seedSchemaCache(t)
	needsClassifier(t)
	fakePsql(t, `printf 'note\nLorem ipsum dolor sit amet consectetur\n'`)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)

	code, out, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--output", "table", "--max-col-width", "10", "SELECT 1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "…") || strings.Contains(out, "consectetur") {
		t.Fatalf("--max-col-width not applied:\n%s", out)
	}

	code, out, errb = run(t, "query", "--host", "testpg", "--config", cfg,
		"--output", "table", "--max-col-width", "0", "SELECT 1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "Lorem ipsum dolor sit amet consectetur") {
		t.Fatalf("--max-col-width 0 must not truncate:\n%s", out)
	}
}

// TestDBQueryOutputEnv pins the escape hatch an agent or CI job uses to opt out
// of the auto default once, and that an explicit flag still beats it.
func TestDBQueryOutputEnv(t *testing.T) {
	t.Run("env selects the format", func(t *testing.T) {
		t.Setenv("DB_QUERY_OUTPUT", "table")
		if got := queryOut(t); !strings.Contains(got, "+----+------+") {
			t.Fatalf("DB_QUERY_OUTPUT=table ignored:\n%s", got)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		t.Setenv("DB_QUERY_OUTPUT", "table")
		if got := queryOut(t, "--output", "text"); got != "id\tname\n1\tAda\n" {
			t.Fatalf("explicit --output did not win: %q", got)
		}
	})

	t.Run("invalid env value is reported", func(t *testing.T) {
		t.Setenv("DB_QUERY_OUTPUT", "yaml")
		seedSchemaCache(t)
		fakePsql(t, `printf 'id\n1\n'`)
		t.Setenv("DBQ_TEST_PW", "pw")
		cfg := testConfig(t)
		code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg, "SELECT 1")
		if code != 1 || !strings.Contains(errb, "unknown output format") {
			t.Fatalf("code=%d err=%q", code, errb)
		}
	})
}

// TestTableOnEveryRowCommand pins that the format applies across the whole
// command surface, not just query — every command that prints rows goes
// through the same render pivot.
func TestTableOnEveryRowCommand(t *testing.T) {
	cfg := testConfig(t)

	t.Run("hosts", func(t *testing.T) {
		code, out, errb := run(t, "hosts", "--config", cfg, "--output", "table")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "| testpg") || !strings.Contains(out, "(1 row)") {
			t.Fatalf("hosts not tabular:\n%s", out)
		}
	})

	t.Run("list", func(t *testing.T) {
		isolateStore(t)
		mustSave(t, "daily", "reports", "postgres", "select 1")
		code, out, errb := run(t, "list", "--output", "table")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "| category") || !strings.Contains(out, "| daily") {
			t.Fatalf("list not tabular:\n%s", out)
		}
	})

	t.Run("schema", func(t *testing.T) {
		seedCatalogueCache(t)
		// setup() resolves the host credential before the cache is consulted.
		t.Setenv("DBQ_TEST_PW", "pw")
		code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg, "--output", "table")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "| table_name") || !strings.Contains(out, "(4 rows)") {
			t.Fatalf("schema not tabular:\n%s", out)
		}
	})

	t.Run("introspect", func(t *testing.T) {
		isolateCache(t)
		splitPsql(t, `printf 'table_schema,table_name,column_name,data_type,is_nullable\npublic,people,id,integer,NO\n'`)
		callsFile(t)
		t.Setenv("DBQ_TEST_PW", "pw")
		code, out, errb := run(t, "introspect", "--host", "testpg", "--config", cfg, "--output", "table")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errb)
		}
		if !strings.Contains(out, "| table_schema") || !strings.Contains(out, "(1 row)") {
			t.Fatalf("introspect not tabular:\n%s", out)
		}
	})
}

// TestCompletionOffersEveryFormat stops the six hard-coded value lists in
// completion.zsh from drifting away from the renderer registry.
func TestCompletionOffersEveryFormat(t *testing.T) {
	code, script, errb := run(t, "completion", "zsh")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	want := "format:(" + strings.Join(render.Formats(), " ") + ")"
	n := strings.Count(script, want)
	if n == 0 {
		t.Fatalf("completion script does not offer %q", want)
	}
	if got := strings.Count(script, "format:("); got != n {
		t.Fatalf("%d format lists, but only %d match %q — a list has drifted", got, n, want)
	}
}

func TestQueryBorderStyles(t *testing.T) {
	cases := map[string]string{
		"ascii":    "+----+------+",
		"light":    "┌────┬──────┐",
		"markdown": "| --- | --- |",
		"none":     "id  name",
	}
	for border, marker := range cases {
		t.Run(border, func(t *testing.T) {
			got := queryOut(t, "--output", "table", "--border", border)
			if !strings.Contains(got, marker) {
				t.Fatalf("--border %s missing %q:\n%s", border, marker, got)
			}
		})
	}
}

// TestBorderRejectedEarly pins that a bad --border fails as a usage error,
// before any credential is resolved or the client is invoked.
func TestBorderRejectedEarly(t *testing.T) {
	cfg := testConfig(t)
	// No psql stub and no password on purpose: if validation happened late,
	// this would fail with a credential or exec error instead.
	code, _, errb := run(t, "query", "--host", "testpg", "--config", cfg,
		"--border", "double", "SELECT 1")
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb, "unknown border style") {
		t.Fatalf("stderr = %q", errb)
	}
	for _, b := range render.Borders() {
		if !strings.Contains(errb, b) {
			t.Errorf("error does not offer %q: %q", b, errb)
		}
	}
}

func TestSchemaBorder(t *testing.T) {
	seedCatalogueCache(t)
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := testConfig(t)
	code, out, errb := run(t, "schema", "--host", "testpg", "--config", cfg,
		"--output", "table", "--border", "markdown")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "| --- |") {
		t.Fatalf("schema --border markdown:\n%s", out)
	}
}

func TestCompletionOffersEveryBorder(t *testing.T) {
	code, script, errb := run(t, "completion", "zsh")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	want := "border:(" + strings.Join(render.Borders(), " ") + ")"
	if !strings.Contains(script, want) {
		t.Fatalf("completion script does not offer %q", want)
	}
}
