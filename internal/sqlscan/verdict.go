// Package sqlscan classifies SQL before it reaches an engine, and holds the
// vocabulary both providers report in. It knows nothing about connections or
// clients: an adapter either decides from the text (postgres, via the vendored
// PostgreSQL grammar) or reports ErrNeedsPlan and is driven through a planner
// probe (sqlserver). See docs/design.md §13.12–§13.14.
package sqlscan

import (
	"errors"
	"fmt"
)

// ErrNeedsPlan is returned by an adapter that cannot decide from the text and
// needs the engine's planner. Callers switch on this error rather than on a
// provider name, so a third provider picks its own mechanism without touching
// any call site.
var ErrNeedsPlan = errors.New("sqlscan: this provider classifies through the engine planner")

// Class is what one statement would do, ordered by how restrictively it must
// be treated. ClassOpaque sorts highest deliberately: a statement the
// mechanism could not classify is not thereby safe, and reducing a submission
// to its maximum class must never let an unclassified statement hide behind a
// classified one.
type Class int

const (
	ClassRead Class = iota
	ClassWrite
	ClassDestructive
	ClassAdmin
	ClassOpaque
)

var classNames = [...]string{"read", "write", "destructive", "admin", "opaque"}

func (c Class) String() string {
	if c < 0 || int(c) >= len(classNames) {
		return "opaque" // an out-of-range class denies, like an unknown one
	}
	return classNames[c]
}

// Permitted reports whether a class may run on a host without an operator
// challenge. The threshold is the host's posture, not a global constant.
//
// A read-only host permits reads and nothing else. A host explicitly declared
// writable permits writes too, because that declaration is the operator
// already saying so; requiring a challenge for every INSERT on a development
// host would make the gate intolerable, and a gate people turn off protects
// nothing. Destructive and administrative statements still meet a human on
// either kind of host, and so does anything unclassifiable.
func (c Class) Permitted(readOnly bool) bool {
	if readOnly {
		return c == ClassRead
	}
	return c == ClassRead || c == ClassWrite
}

// Statement is one statement's verdict, carrying what decided it so a refusal
// can name the statement and the reason rather than the whole submission.
type Statement struct {
	Index     int    `json:"index"`
	Class     Class  `json:"-"`
	ClassName string `json:"class"`
	DecidedBy string `json:"decided_by"`
}

// Verdict is a whole submission's classification.
type Verdict struct {
	Class      Class       `json:"-"`
	ClassName  string      `json:"class"`
	Mechanism  string      `json:"mechanism"`          // "parser" or "planner"
	Version    string      `json:"decided_by_version"` // the grammar or server that decided
	Statements []Statement `json:"statements"`
}

// Mechanism values, fixed because they reach the dry-run document and the
// audit record, where they distinguish a claim about the text from a claim
// about what one server at one version would do.
const (
	MechanismParser  = "parser"
	MechanismPlanner = "planner"
)

// Reduce fills the submission-level class from the statements, taking the
// maximum. A submission is exactly as safe as its least safe statement.
func (v *Verdict) Reduce() {
	v.Class = ClassRead
	for i := range v.Statements {
		v.Statements[i].ClassName = v.Statements[i].Class.String()
		if v.Statements[i].Class > v.Class {
			v.Class = v.Statements[i].Class
		}
	}
	if len(v.Statements) == 0 {
		// No statements means nothing was understood, not that nothing
		// happens. An empty submission never reaches here (the CLI rejects
		// it), so this is a mechanism that returned nothing.
		v.Class = ClassOpaque
	}
	v.ClassName = v.Class.String()
}

// Opaque builds a one-statement verdict for a submission that could not be
// classified at all, so every failure path produces a well-formed verdict
// instead of a nil one a caller might read as permission.
func Opaque(mechanism, version, reason string) Verdict {
	v := Verdict{
		Mechanism: mechanism,
		Version:   version,
		Statements: []Statement{{
			Index:     1,
			Class:     ClassOpaque,
			DecidedBy: reason,
		}},
	}
	v.Reduce()
	return v
}

// ReasonCode is the stable, machine-readable half of a decision. Hooks branch
// on these; the human prose beside them is free to change. Codes are
// append-only and never repurposed, and a consumer meeting an unknown one
// treats it as a refusal (§13.14).
type ReasonCode string

const (
	ReasonOKRead              ReasonCode = "OK_READ"
	ReasonClassWrite          ReasonCode = "CLASS_WRITE"
	ReasonClassDestructive    ReasonCode = "CLASS_DESTRUCTIVE"
	ReasonClassAdmin          ReasonCode = "CLASS_ADMIN"
	ReasonClassOpaque         ReasonCode = "CLASS_OPAQUE"
	ReasonClientDirective     ReasonCode = "CLIENT_DIRECTIVE"
	ReasonParamUnsafe         ReasonCode = "PARAM_UNSAFE"
	ReasonReadonlyProbeFailed ReasonCode = "READONLY_PROBE_FAILED"
	ReasonParserUnavailable   ReasonCode = "PARSER_UNAVAILABLE"
	ReasonPrecheckIncomplete  ReasonCode = "PRECHECK_INCOMPLETE"
	ReasonDigestMismatch      ReasonCode = "DIGEST_MISMATCH"
)

// CodeFor maps a class to the reason code that reports it.
func CodeFor(c Class) ReasonCode {
	switch c {
	case ClassRead:
		return ReasonOKRead
	case ClassWrite:
		return ReasonClassWrite
	case ClassDestructive:
		return ReasonClassDestructive
	case ClassAdmin:
		return ReasonClassAdmin
	default:
		return ReasonClassOpaque
	}
}

// Action is what a caller should do with a verdict.
type Action string

const (
	// ActionAllow runs the query.
	ActionAllow Action = "allow"
	// ActionChallenge routes to the operator challenge: the submission may be
	// legitimate, but nothing short of a human may authorise it.
	ActionChallenge Action = "challenge"
	// ActionBlock refuses outright. Reserved for submissions no operator
	// should be asked to wave through, such as a client directive.
	ActionBlock Action = "block"
)

// Decision pairs an action with the codes that explain it.
type Decision struct {
	Action     Action     `json:"action"`
	ReasonCode ReasonCode `json:"reason_code"`
	Reason     string     `json:"reason"`
}

// Decide turns a verdict into a decision for a host of the given posture.
// Refusals that are never an operator's call to make (directives, unsafe
// params, a failed privilege probe) are raised by their own call sites with
// ActionBlock.
func Decide(v Verdict, readOnly bool) Decision {
	if v.Class.Permitted(readOnly) {
		return Decision{
			Action:     ActionAllow,
			ReasonCode: CodeFor(v.Class),
			Reason:     "permitted on this host: " + v.Class.String(),
		}
	}
	// Name the first statement at the deciding class: a refusal that cannot
	// say which statement of twelve caused it is not actionable.
	for _, s := range v.Statements {
		if s.Class == v.Class {
			return Decision{
				Action:     ActionChallenge,
				ReasonCode: CodeFor(v.Class),
				Reason:     fmt.Sprintf("statement %d is %s (%s)", s.Index, s.Class, s.DecidedBy),
			}
		}
	}
	return Decision{Action: ActionChallenge, ReasonCode: CodeFor(v.Class), Reason: v.Class.String()}
}
