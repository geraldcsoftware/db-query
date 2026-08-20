package precheck

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestDigestBindsMoreThanTheSQL(t *testing.T) {
	base := Tuple{Provider: "postgres", Host: "dev", Database: "app", SQL: "SELECT 1",
		ParamValues: map[string]string{"id": "1"}}
	want := Digest(base, testKey)

	// Each of these is a replay the digest has to defeat.
	differs := map[string]Tuple{
		"a different host":     {Provider: "postgres", Host: "prod", Database: "app", SQL: "SELECT 1", ParamValues: map[string]string{"id": "1"}},
		"a different database": {Provider: "postgres", Host: "dev", Database: "other", SQL: "SELECT 1", ParamValues: map[string]string{"id": "1"}},
		"a different provider": {Provider: "sqlserver", Host: "dev", Database: "app", SQL: "SELECT 1", ParamValues: map[string]string{"id": "1"}},
		"different SQL":        {Provider: "postgres", Host: "dev", Database: "app", SQL: "SELECT 2", ParamValues: map[string]string{"id": "1"}},
		"a different param value": {Provider: "postgres", Host: "dev", Database: "app", SQL: "SELECT 1",
			ParamValues: map[string]string{"id": "1; DROP TABLE t"}},
	}
	for name, tp := range differs {
		if Digest(tp, testKey) == want {
			t.Errorf("%s produced the same digest, so that replay would pass", name)
		}
	}
	if Digest(base, []byte("a different key entirely aaaaaa")) == want {
		t.Error("the key does not affect the digest")
	}
}

func TestDigestIsStableAcrossMapOrder(t *testing.T) {
	a := Tuple{SQL: "x", ParamValues: map[string]string{"a": "1", "b": "2", "c": "3"}}
	b := Tuple{SQL: "x", ParamValues: map[string]string{"c": "3", "b": "2", "a": "1"}}
	if Digest(a, testKey) != Digest(b, testKey) {
		t.Error("map iteration order changed the digest")
	}
}

func TestCanonicalIsUnambiguous(t *testing.T) {
	// Without length prefixes these two would serialise identically.
	a := Tuple{Provider: "ab", Host: "c"}
	b := Tuple{Provider: "a", Host: "bc"}
	if Digest(a, testKey) == Digest(b, testKey) {
		t.Error("field boundaries are not encoded, so contents can be rearranged")
	}
}

func evalPostgres(t *testing.T, sql string, params map[string]string) Document {
	t.Helper()
	a, err := adapter.For("postgres")
	if err != nil {
		t.Fatal(err)
	}
	return Evaluate(Input{
		Adapter: a,
		Host:    config.HostConfig{Name: "h", Provider: "postgres", Database: "d", ReadOnly: true},
		SQL:     sql,
		Params:  params,
		Source:  Source{Kind: "arg"},
		Key:     testKey,
	})
}

func TestTokenIsMintedOnlyForCleanSQL(t *testing.T) {
	clean := evalPostgres(t, "SELECT 1", nil)
	if clean.Decision.Action != sqlscan.ActionAllow {
		t.Fatalf("a plain SELECT should be allowed, got %+v", clean.Decision)
	}
	if clean.PrecheckToken == nil {
		t.Error("a clean submission should carry a token")
	}

	dirty := evalPostgres(t, "DROP TABLE t", nil)
	if dirty.PrecheckToken != nil {
		// Otherwise the token would prove a pre-check happened rather than
		// that it passed, which a caller satisfies by running the pre-check
		// itself and ignoring the answer.
		t.Error("a destructive submission must not be handed a token")
	}
}

func TestClientDirectiveBlocksBeforeAnyMechanism(t *testing.T) {
	doc := evalPostgres(t, "SELECT 1;\n\\! rm -rf /", nil)
	if doc.Decision.Action != sqlscan.ActionBlock {
		t.Errorf("action: got %q, want block", doc.Decision.Action)
	}
	if doc.Decision.ReasonCode != sqlscan.ReasonClientDirective {
		t.Errorf("reason code: got %q, want %q", doc.Decision.ReasonCode, sqlscan.ReasonClientDirective)
	}
	if doc.PrecheckToken != nil {
		t.Error("a blocked submission must not carry a token")
	}
}

func TestPlannerProviderWithoutAConnectionIsIncompleteNotClean(t *testing.T) {
	a, err := adapter.For("sqlserver")
	if err != nil {
		t.Fatal(err)
	}
	doc := Evaluate(Input{
		Adapter: a,
		Host:    config.HostConfig{Name: "h", Provider: "sqlserver", Database: "d", ReadOnly: true},
		SQL:     "SELECT 1",
		Source:  Source{Kind: "arg"},
		Key:     testKey,
		RunPlan: nil, // no connection
	})
	if doc.Status != StatusIncomplete {
		t.Errorf("status: got %q, want %q", doc.Status, StatusIncomplete)
	}
	if doc.Decision.Action == sqlscan.ActionAllow || doc.PrecheckToken != nil {
		t.Error("an unclassifiable submission must never be allowed or tokenised")
	}
}

func TestDocumentOmitsSQLAndParamValues(t *testing.T) {
	// §13.14: a literal in the SQL can be an account number, and the audit
	// record must not carry it.
	doc := evalPostgres(t, "SELECT * FROM cards WHERE pan = '4111111111111111'",
		map[string]string{"secret": "hunter2"})
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("4111111111111111")) {
		t.Error("the document leaks a SQL literal by default")
	}
	if bytes.Contains(b, []byte("hunter2")) {
		t.Error("the document leaks a parameter value")
	}
	if !bytes.Contains(b, []byte(`"secret"`)) {
		t.Error("the document should still carry parameter names")
	}
}

func TestDocumentIsByteReproducible(t *testing.T) {
	a, _ := json.Marshal(evalPostgres(t, "SELECT 1", map[string]string{"b": "2", "a": "1"}))
	b, _ := json.Marshal(evalPostgres(t, "SELECT 1", map[string]string{"a": "1", "b": "2"}))
	if !bytes.Equal(a, b) {
		t.Errorf("two runs over identical input differ:\n%s\n%s", a, b)
	}
}

func TestGateRejectsAChangedTuple(t *testing.T) {
	checked := Tuple{Provider: "postgres", Host: "h", Database: "d", SQL: "SELECT 1"}
	token := Digest(checked, testKey)
	ran := checked
	ran.SQL = "DROP TABLE t" // the file changed between check and use

	doc := evalPostgres(t, "SELECT 1", nil)
	got, _ := Gate(doc, token, ran, testKey, nil)
	if got != Mismatch {
		t.Errorf("got %v, want Mismatch", got)
	}
	if got.ExitCode() != 6 {
		t.Errorf("exit code: got %d, want 6", got.ExitCode())
	}
}

func TestGateDoesNotLetATokenAnswerAChallenge(t *testing.T) {
	doc := evalPostgres(t, "DROP TABLE t", nil)
	tuple := Tuple{Provider: "postgres", Host: "h", Database: "d", SQL: "DROP TABLE t"}
	// A well-formed token for exactly this tuple, which is what a caller that
	// ran its own dry-run would hold.
	token := Digest(tuple, testKey)
	got, _ := Gate(doc, token, tuple, testKey, nil)
	if got != Refused {
		t.Errorf("got %v, want Refused: a token must not stand in for a human", got)
	}
	if got.ExitCode() != 5 {
		t.Errorf("exit code: got %d, want 5", got.ExitCode())
	}
}

func TestGateAllowsACleanRead(t *testing.T) {
	doc := evalPostgres(t, "SELECT 1", nil)
	tuple := Tuple{Provider: "postgres", Host: "h", Database: "d", SQL: "SELECT 1"}
	if got, why := Gate(doc, Digest(tuple, testKey), tuple, testKey, nil); got != Proceed {
		t.Errorf("got %v (%s), want Proceed", got, why)
	}
	// A missing token is not itself a failure: otherwise every scheduled
	// SELECT would need an operator.
	if got, why := Gate(doc, "", tuple, testKey, nil); got != Proceed {
		t.Errorf("no token: got %v (%s), want Proceed", got, why)
	}
}

func TestChallengeRequiresTheNonceTypedBack(t *testing.T) {
	doc := evalPostgres(t, "DROP TABLE t", nil)
	var out bytes.Buffer
	// Anything that is not the nonce declines, and the nonce is not knowable
	// before it is printed.
	ok, err := challengeOn(&out, strings.NewReader("yes\n"), doc)
	if err != nil || ok {
		t.Errorf("typing 'yes' should not authorise: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out.String(), "type") {
		t.Errorf("the prompt does not say what to type: %q", out.String())
	}

	out.Reset()
	ok, _ = challengeOn(&out, strings.NewReader("\n"), doc)
	if ok {
		t.Error("an empty answer should not authorise")
	}
}
