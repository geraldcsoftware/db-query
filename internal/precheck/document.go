package precheck

import (
	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// SchemaVersion is the dry-run document's contract version.
//
// A consumer that does not recognise it must refuse rather than proceed
// (§13.14). The failure this guards is quiet: rename a field in a later
// release and a hook testing decision.action reads nothing and allows, so an
// ordinary upgrade would silently disable the control.
const SchemaVersion = 1

// Status distinguishes a verdict from the absence of one. §13.13's planner
// mechanism needs a reachable database, so a pre-check can fail for reasons
// unrelated to the SQL, and "could not classify" must never read as "clean".
const (
	StatusClassified = "classified"
	StatusIncomplete = "incomplete"
)

// Source names which of the five inputs the SQL arrived through. A hook's
// confidence differs by kind: stdin cannot be replayed, and a file can change
// between the check and the run.
type Source struct {
	Kind string `json:"kind"` // arg | file | stdin | saved
	Ref  string `json:"ref,omitempty"`
}

// ReadOnly reports the posture actually in force, separating what the config
// says from what was verified, so a hook can require more than a config key.
type ReadOnly struct {
	Configured     bool   `json:"configured"`
	Probe          string `json:"probe"` // passed | failed | skipped
	EngineEnforced bool   `json:"engine_enforced"`
}

// Document is the dry-run output. Field names and reason codes are an API.
type Document struct {
	SchemaVersion int      `json:"schema_version"`
	Status        string   `json:"status"`
	Tool          Tool     `json:"tool"`
	Target        Target   `json:"target"`
	Source        Source   `json:"source"`
	ReadOnly      ReadOnly `json:"readonly"`

	Classification sqlscan.Verdict `json:"classification"`
	Params         Params          `json:"params"`
	Digest         DigestField     `json:"digest"`

	// PrecheckToken is explicitly null rather than omitted when no token was
	// minted, because a missing key cannot be told from a truncated document.
	PrecheckToken *string `json:"precheck_token"`

	Decision sqlscan.Decision `json:"decision"`

	// SQL appears only when explicitly requested. §9 forbids logging
	// parameter values, and §13.14 extends that to literals written into the
	// SQL itself: an account number in a WHERE clause would otherwise reach
	// this document and the audit record.
	SQL string `json:"sql,omitempty"`
}

type Tool struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Target struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
	Database string `json:"database"`
}

// Params carries names and a count. The values are covered by the digest and
// reach nothing else.
type Params struct {
	Names []string `json:"names"`
	Count int      `json:"count"`
}

type DigestField struct {
	Alg   string `json:"alg"`
	Value string `json:"value"`
}
