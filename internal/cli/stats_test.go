package cli

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/pgfake"
)

// statsScript runs a script of a SELECT and an UPDATE against the fake server
// and returns what the command printed.
func statsScript(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	seedSchemaCache(t)
	needsClassifier(t)
	fakePG(t, func(q pgfake.Query) pgfake.Reply {
		if q.SQL == "SELECT 1 AS a" {
			return pgfake.Rows([]string{"a"}, []string{"1"})
		}
		return pgfake.Reply{Tag: "UPDATE 4"}
	})
	t.Setenv("DBQ_TEST_PW", "pw")
	args := append([]string{"query", "--host", "testpg", "--config", writableConfig(t)}, extra...)
	args = append(args, "SELECT 1 AS a; UPDATE people SET name = name")
	return run(t, args...)
}

// TestQueryStatsOffWhenStderrIsNotATerminal pins the default for a caller
// capturing stderr: a successful run adds nothing to it.
func TestQueryStatsOffWhenStderrIsNotATerminal(t *testing.T) {
	code, out, errb := statsScript(t)
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q, want a silent stderr", code, errb)
	}
	if out != "a\n1\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestQueryStatsText(t *testing.T) {
	code, out, errb := statsScript(t, "--stats")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "a\n1\n" {
		t.Fatalf("stdout = %q: the summary must stay off stdout", out)
	}
	want := regexp.MustCompile(`^db-query: 1 row returned, 4 rows affected \((<1ms|\d+ms|[\d.]+s)\)\n$`)
	if !want.MatchString(errb) {
		t.Fatalf("stderr = %q", errb)
	}
}

func TestQueryStatsJSON(t *testing.T) {
	code, out, errb := statsScript(t, "--stats", "--output", "json")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("stdout is not the rows alone: %v\n%s", err, out)
	}
	var doc struct {
		Stats struct {
			ResultSets   int    `json:"result_sets"`
			RowsReturned int    `json:"rows_returned"`
			RowsAffected *int64 `json:"rows_affected"`
			Statements   []struct {
				Tag      string `json:"tag"`
				Rows     *int64 `json:"rows"`
				Returned bool   `json:"returned"`
			} `json:"statements"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(errb), &doc); err != nil {
		t.Fatalf("stderr is not one JSON document: %v\n%s", err, errb)
	}
	s := doc.Stats
	if s.ResultSets != 1 || s.RowsReturned != 1 || s.RowsAffected == nil || *s.RowsAffected != 4 {
		t.Fatalf("stats = %+v", s)
	}
	if len(s.Statements) != 2 || s.Statements[0].Tag != "SELECT 1" || !s.Statements[0].Returned ||
		s.Statements[1].Tag != "UPDATE 4" || s.Statements[1].Returned {
		t.Fatalf("statements = %+v", s.Statements)
	}
}

func TestQueryStatsOnByDefaultOnATerminal(t *testing.T) {
	seedSchemaCache(t)
	needsClassifier(t)
	fakePG(t, replyAll(pgfake.Reply{Tag: "UPDATE 4"}))
	t.Setenv("DBQ_TEST_PW", "pw")
	cfg := writableConfig(t)

	var out, errb strings.Builder
	real := isTerminal
	t.Cleanup(func() { isTerminal = real })
	// Only stderr is a terminal, so stdout still renders as text.
	isTerminal = func(w io.Writer) bool { return w == io.Writer(&errb) }

	for _, tc := range []struct {
		flag string
		want bool
	}{{"", true}, {"--stats=false", false}} {
		out.Reset()
		errb.Reset()
		args := []string{"query", "--host", "testpg", "--config", cfg}
		if tc.flag != "" {
			args = append(args, tc.flag)
		}
		args = append(args, "UPDATE people SET name = name")
		if code := Run(args, &out, &errb); code != 0 {
			t.Fatalf("%q: code=%d err=%q", tc.flag, code, errb.String())
		}
		got := strings.HasPrefix(errb.String(), "db-query: 4 rows affected (")
		if got != tc.want || (!tc.want && errb.Len() != 0) {
			t.Fatalf("%q: stderr = %q, want summary %v", tc.flag, errb.String(), tc.want)
		}
	}
}
