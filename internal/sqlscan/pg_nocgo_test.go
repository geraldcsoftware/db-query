//go:build !cgo

package sqlscan

import "testing"

// A build without the grammar must refuse, not allow. This is the failure mode
// worth a test of its own: a safety check that disappears with a build flag is
// worse than one that was never there, because the tool still looks guarded.
func TestClassifyPostgresWithoutCgoRefuses(t *testing.T) {
	for _, sql := range []string{"SELECT 1", "DROP TABLE t"} {
		got := ClassifyPostgres(sql)
		if got.Class != ClassOpaque {
			t.Errorf("%q: got %s, want opaque", sql, got.Class)
		}
		if Decide(got, false).Action == ActionAllow {
			t.Errorf("%q: a build without the parser must never allow", sql)
		}
	}
}
