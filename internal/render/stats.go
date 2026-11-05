package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/geraldcsoftware/db-query/internal/adapter"
)

// Summary is what a run did, in the terms a person reading it wants: how many
// rows came back, how many were changed, and how long it took. It is built
// from a Result, so it reads the same for either provider.
type Summary struct {
	// ResultSets and RowsReturned count the result sets the run printed and
	// the rows in them.
	ResultSets   int `json:"result_sets"`
	RowsReturned int `json:"rows_returned"`

	// RowsAffected is the sum of every count the server gave for rows a
	// statement changed. It is nil when no statement gave one: a run of only
	// SELECTs, a CREATE TABLE, or anything under SQL Server's NOCOUNT. That is
	// not the same as a count of zero, which an UPDATE matching nothing gives.
	RowsAffected *int64 `json:"rows_affected"`

	// ElapsedMS is the run's duration in milliseconds.
	ElapsedMS float64 `json:"elapsed_ms"`

	// Statements is the server's report on each statement, as it gave it.
	Statements []StatementSummary `json:"statements"`

	elapsed time.Duration
}

// StatementSummary is one statement's entry in a Summary. Tag is the postgres
// command tag and is left out for SQL Server, which has none. Rows is nil for a
// statement the server gave no count for.
type StatementSummary struct {
	Tag      string `json:"tag,omitempty"`
	Rows     *int64 `json:"rows"`
	Returned bool   `json:"returned"`
}

// Summarize reads a Result into a Summary.
func Summarize(res adapter.Result) Summary {
	s := Summary{
		ResultSets: len(res.Sets),
		ElapsedMS:  float64(res.Elapsed.Microseconds()) / 1000,
		Statements: make([]StatementSummary, 0, len(res.Stats)),
		elapsed:    res.Elapsed,
	}
	for _, set := range res.Sets {
		s.RowsReturned += len(set.Rows)
	}
	for _, st := range res.Stats {
		entry := StatementSummary{Tag: st.Tag, Returned: st.Returned}
		if st.Counted {
			n := st.Rows
			entry.Rows = &n
			if !st.Returned {
				if s.RowsAffected == nil {
					s.RowsAffected = new(int64)
				}
				*s.RowsAffected += n
			}
		}
		s.Statements = append(s.Statements, entry)
	}
	return s
}

// Stats writes a run's summary. In json it is one line, {"stats": {...}},
// shaped like the {"error": ...} line a failure writes, so a consumer reading
// stderr can tell the two apart by their key. Every other format writes one
// line of prose: "db-query: 12 rows returned, 3 rows affected (41ms)".
func Stats(w io.Writer, format string, res adapter.Result) error {
	s := Summarize(res)
	if format == "json" {
		doc, err := json.Marshal(map[string]Summary{"stats": s})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(doc))
		return err
	}
	_, err := fmt.Fprintln(w, "db-query: "+s.String())
	return err
}

// String is the summary as prose, without the "db-query: " prefix.
func (s Summary) String() string {
	var parts []string
	if s.ResultSets > 0 {
		part := rowCount(int64(s.RowsReturned)) + " returned"
		if s.ResultSets > 1 {
			part += fmt.Sprintf(" in %d result sets", s.ResultSets)
		}
		parts = append(parts, part)
	}
	if s.RowsAffected != nil {
		parts = append(parts, rowCount(*s.RowsAffected)+" affected")
	}
	if len(parts) == 0 {
		parts = append(parts, "ok")
	}
	return strings.Join(parts, ", ") + " (" + elapsedText(s.elapsed) + ")"
}

func rowCount(n int64) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

// elapsedText rounds a duration to what is worth reading: whole milliseconds
// under a second, hundredths of a second above it.
func elapsedText(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}
