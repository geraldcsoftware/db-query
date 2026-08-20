package precheck

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/sqlscan"
)

// Outcome is what the gate decided about a real (non dry-run) invocation.
type Outcome int

const (
	// Proceed: the submission may run.
	Proceed Outcome = iota
	// Refused: it may not, and no operator may wave it through.
	Refused
	// Mismatch: a token was presented for a different tuple than the one
	// about to run, which means the input changed between check and use.
	Mismatch
)

// ExitCode maps an outcome onto the process exit code, extending §13.3.
// A refusal and a mismatch are distinct so a caller can tell "policy says no"
// from "the input changed since you checked it".
func (o Outcome) ExitCode() int {
	switch o {
	case Refused:
		return 5
	case Mismatch:
		return 6
	default:
		return 0
	}
}

// Gate decides whether a real invocation runs.
//
// The order matters. A presented token is verified first: if it names a
// different tuple, the input changed between the check and the run, and that
// is a distinct failure from any verdict about the current input. Only then
// does the classification decide.
func Gate(doc Document, presentedToken string, tuple Tuple, key []byte, challenge Challenger) (Outcome, string) {
	if presentedToken != "" && !Matches(tuple, key, presentedToken) {
		return Mismatch, "the query, parameters or target changed since it was checked"
	}

	switch doc.Decision.Action {
	case sqlscan.ActionAllow:
		return Proceed, ""
	case sqlscan.ActionBlock:
		return Refused, doc.Decision.Reason
	}

	// A challenge. A valid token does not answer it: §13.12 mints tokens only
	// for clean SQL, so a token presented alongside a non-clean verdict says
	// nothing about this submission.
	if challenge == nil {
		return Refused, doc.Decision.Reason + " (no operator terminal available)"
	}
	ok, err := challenge(doc)
	if err != nil {
		return Refused, doc.Decision.Reason + " (" + err.Error() + ")"
	}
	if !ok {
		return Refused, "declined at the operator challenge"
	}
	return Proceed, ""
}

// Challenger asks an operator to authorise a submission.
type Challenger func(doc Document) (bool, error)

// TTYChallenge asks on /dev/tty, printing the target and a nonce the operator
// must type back.
//
// It opens /dev/tty rather than testing whether a standard descriptor is a
// terminal. A descriptor test fails in the wrong direction: any caller wrapped
// in script(1) gets a full terminal on all three descriptors, while an
// operator piping output to jq does not, so the test admits the automated case
// and refuses the human one. Opening /dev/tty also works when stdin carries
// the SQL and stdout is a pipe, which are both documented usages.
//
// An automated caller can still answer this, but only by reading a nonce and
// echoing it, which is a deliberate act visible in a transcript rather than a
// passive wrapper.
func TTYChallenge(doc Document) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("no operator terminal: %w", err)
	}
	defer tty.Close()
	return challengeOn(tty, tty, doc)
}

// challengeOn is the body, taking its streams so a test can drive it.
func challengeOn(out io.Writer, in io.Reader, doc Document) (bool, error) {
	nonce, err := newNonce()
	if err != nil {
		return false, err
	}
	fmt.Fprintf(out, "\ndb-query: %s\n", doc.Decision.Reason)
	fmt.Fprintf(out, "  target   %s on %s (%s)\n", doc.Target.Database, doc.Target.Host, doc.Target.Provider)
	fmt.Fprintf(out, "  class    %s\n", doc.Classification.ClassName)
	fmt.Fprintf(out, "\nTo run it, type %s and press Enter. Anything else cancels.\n> ", nonce)

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false, nil // a closed terminal is a decline, never an approval
	}
	return strings.TrimSpace(line) == nonce, nil
}

// newNonce returns a short challenge string. It is short enough to retype and
// long enough that it cannot be guessed ahead of being printed, which is what
// makes answering it an act rather than a reflex.
func newNonce() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating a challenge: %w", err)
	}
	return hex.EncodeToString(b), nil
}
