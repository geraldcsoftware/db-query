package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

func TestSummaryString(t *testing.T) {
	set := func(n int) adapter.Rows {
		r := adapter.Rows{Columns: []string{"a"}}
		for range n {
			v := "x"
			r.Rows = append(r.Rows, []*string{&v})
		}
		return r
	}
	for name, tc := range map[string]struct {
		res  adapter.Result
		want string
	}{
		"select": {
			adapter.Result{Sets: []adapter.Rows{set(12)}, Stats: []adapter.Stat{{Rows: 12, Counted: true, Returned: true}}, Elapsed: 41 * time.Millisecond},
			"12 rows returned (41ms)",
		},
		"one row": {
			adapter.Result{Sets: []adapter.Rows{set(1)}, Elapsed: 1500 * time.Microsecond},
			"1 row returned (2ms)",
		},
		"several sets and dml": {
			adapter.Result{
				Sets: []adapter.Rows{set(2), set(3)},
				Stats: []adapter.Stat{
					{Rows: 2, Counted: true, Returned: true},
					{Rows: 4, Counted: true},
					{Rows: 1, Counted: true},
					{Rows: 3, Counted: true, Returned: true},
				},
				Elapsed: 1234 * time.Millisecond,
			},
			"5 rows returned in 2 result sets, 5 rows affected (1.23s)",
		},
		"update matching nothing": {
			adapter.Result{Stats: []adapter.Stat{{Tag: "UPDATE 0", Counted: true}}, Elapsed: 300 * time.Microsecond},
			"0 rows affected (<1ms)",
		},
		"ddl has no count": {
			adapter.Result{Stats: []adapter.Stat{{Tag: "CREATE TABLE"}}, Elapsed: 5 * time.Millisecond},
			"ok (5ms)",
		},
		"empty set": {
			adapter.Result{Sets: []adapter.Rows{set(0)}, Elapsed: 5 * time.Millisecond},
			"0 rows returned (5ms)",
		},
	} {
		if got := Summarize(tc.res).String(); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestStatsJSON(t *testing.T) {
	var b strings.Builder
	res := adapter.Result{
		Stats:   []adapter.Stat{{Tag: "CREATE TABLE"}, {Tag: "INSERT 0 2", Rows: 2, Counted: true}},
		Elapsed: 2500 * time.Microsecond,
	}
	if err := Stats(&b, "json", res); err != nil {
		t.Fatal(err)
	}
	want := `{"stats":{"result_sets":0,"rows_returned":0,"rows_affected":2,"elapsed_ms":2.5,` +
		`"statements":[{"tag":"CREATE TABLE","rows":null,"returned":false},{"tag":"INSERT 0 2","rows":2,"returned":false}]}}` + "\n"
	if b.String() != want {
		t.Fatalf("got  %s\nwant %s", b.String(), want)
	}
	if !json.Valid([]byte(b.String())) {
		t.Fatal("not valid JSON")
	}
}

func TestStatsJSONWithNoCountsHasNullAffected(t *testing.T) {
	var b strings.Builder
	if err := Stats(&b, "json", adapter.Result{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"rows_affected":null`) || !strings.Contains(b.String(), `"statements":[]`) {
		t.Fatalf("got %s", b.String())
	}
}

func TestStatsText(t *testing.T) {
	var b strings.Builder
	if err := Stats(&b, "table", adapter.Result{Elapsed: 3 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if b.String() != "db-query: ok (3ms)\n" {
		t.Fatalf("got %q", b.String())
	}
}
