//go:build integration

package integration

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// statsDoc is the --stats --output json line on stderr.
type statsDoc struct {
	Stats struct {
		ResultSets   int     `json:"result_sets"`
		RowsReturned int     `json:"rows_returned"`
		RowsAffected *int64  `json:"rows_affected"`
		ElapsedMS    float64 `json:"elapsed_ms"`
		Statements   []struct {
			Tag      string `json:"tag"`
			Rows     *int64 `json:"rows"`
			Returned bool   `json:"returned"`
		} `json:"statements"`
	} `json:"stats"`
}

func parseStats(t *testing.T, r result) statsDoc {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("code=%d stderr=%s", r.code, r.stderr)
	}
	var doc statsDoc
	if err := json.Unmarshal([]byte(r.stderr), &doc); err != nil {
		t.Fatalf("stderr is not one stats document: %v\n%s", err, r.stderr)
	}
	return doc
}

func affected(d statsDoc) int64 {
	if d.Stats.RowsAffected == nil {
		return -1
	}
	return *d.Stats.RowsAffected
}

// The safety gate refuses DDL on every host, so these runs write to the
// fixture's people table, at ids from 90 up, and clean it through the driver.

func TestPostgresStats(t *testing.T) {
	pgReady(t)
	t.Cleanup(func() { seedPG(t, "DELETE FROM people WHERE id >= 90") })
	t.Run("a script reports each statement's tag", func(t *testing.T) {
		d := parseStats(t, runTool(t, nil, "query", "--host", "pgrw", "--stats", "--output", "json",
			"INSERT INTO people (id, name) VALUES (90, 'a'), (91, 'b'), (92, 'c'); "+
				"UPDATE people SET name = name WHERE id > 90 RETURNING id; "+
				"UPDATE people SET name = name WHERE id = 0; "+
				"SELECT id FROM people WHERE id >= 90"))
		var tags []string
		for _, s := range d.Stats.Statements {
			tags = append(tags, s.Tag)
		}
		if want := "INSERT 0 3|UPDATE 2|UPDATE 0|SELECT 3"; strings.Join(tags, "|") != want {
			t.Fatalf("tags = %q, want %q", tags, want)
		}
		// RETURNING's two rows are both returned and changed.
		if affected(d) != 5 || d.Stats.RowsReturned != 5 || d.Stats.ResultSets != 2 {
			t.Fatalf("stats = %+v", d.Stats)
		}
	})
	t.Run("text summary", func(t *testing.T) {
		r := runTool(t, nil, "query", "--host", "pg", "--stats", "SELECT 1 AS a")
		if r.code != 0 || r.stdout != "a\n1\n" ||
			!regexp.MustCompile(`^db-query: 1 row returned \(.+\)\n$`).MatchString(r.stderr) {
			t.Fatalf("code=%d stdout=%q stderr=%q", r.code, r.stdout, r.stderr)
		}
	})
}

func TestSQLServerStats(t *testing.T) {
	mssqlReady(t)
	t.Cleanup(func() { seedMSSQL(t, "dbqtest", "DELETE FROM people WHERE id >= 90") })
	t.Run("counts for dml and selects", func(t *testing.T) {
		d := parseStats(t, runTool(t, nil, "query", "--host", "mssqlrw", "--stats", "--output", "json",
			"INSERT INTO people (id, name) VALUES (90, 'a'), (91, 'b'), (92, 'c');\n"+
				"UPDATE people SET name = name WHERE id > 90;\n"+
				"SELECT id FROM people WHERE id >= 90;"))
		if affected(d) != 5 || d.Stats.RowsReturned != 3 || d.Stats.ResultSets != 1 {
			t.Fatalf("stats = %+v", d.Stats)
		}
		var returned []bool
		for _, s := range d.Stats.Statements {
			returned = append(returned, s.Returned)
		}
		if len(returned) != 3 || returned[0] || returned[1] || !returned[2] {
			t.Fatalf("statements = %+v", d.Stats.Statements)
		}
	})
	t.Run("GO n repeats are each counted", func(t *testing.T) {
		d := parseStats(t, runTool(t, nil, "query", "--host", "mssqlrw", "--stats", "--output", "json",
			"UPDATE people SET name = name WHERE id = 1\nGO 3"))
		if affected(d) != 3 || len(d.Stats.Statements) != 3 {
			t.Fatalf("stats = %+v", d.Stats)
		}
	})
	t.Run("an error after a count still fails the run", func(t *testing.T) {
		r := runTool(t, nil, "query", "--host", "mssqlrw", "--stats",
			"UPDATE people SET name = name WHERE id = 1;\nSELECT 1/0 AS boom;")
		if r.code != 4 || !strings.Contains(r.stderr, "Msg 8134") || strings.Contains(r.stderr, "affected") {
			t.Fatalf("code=%d stderr=%q", r.code, r.stderr)
		}
	})
	t.Run("a constraint violation reports the server's error", func(t *testing.T) {
		r := runTool(t, nil, "query", "--host", "mssqlrw",
			"INSERT INTO people (id, name) VALUES (1, 'duplicate');")
		if r.code != 4 || !strings.Contains(r.stderr, "Msg 2627") {
			t.Fatalf("code=%d stderr=%q", r.code, r.stderr)
		}
	})
}
