package tui

import (
	"testing"

	"github.com/geraldcsoftware/db-query/internal/adapter"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// TestShouldLaunchFalseOnZeroResolved confirms the exact contract Run's
// startup check relies on: bootstrap's picker-quit sentinel (a zero-value
// session.Resolved with code 0) must not be treated as launchable, or Run
// would start a second, blank interactive program after the user already
// quit the host picker.
func TestShouldLaunchFalseOnZeroResolved(t *testing.T) {
	if shouldLaunch(session.Resolved{}) {
		t.Fatal("shouldLaunch(session.Resolved{}) = true, want false")
	}
}

// TestShouldLaunchTrueWithAdapter confirms a genuinely resolved session
// (the shape session.Setup returns on success) is launchable.
func TestShouldLaunchTrueWithAdapter(t *testing.T) {
	a, err := adapter.For("postgres")
	if err != nil {
		t.Fatalf("adapter.For(postgres) = %v", err)
	}
	if !shouldLaunch(session.Resolved{Adapter: a}) {
		t.Fatal("shouldLaunch with a non-nil Adapter = false, want true")
	}
}
