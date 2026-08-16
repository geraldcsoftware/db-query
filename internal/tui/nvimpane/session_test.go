package nvimpane

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestTheVersionGateComparesPlainIntegers: the floor is 0.12.0 because
// 'autocomplete' and the function-source flags of 'complete' do not exist
// before it, so a build below it cannot run this pane at all.
func TestTheVersionGateComparesPlainIntegers(t *testing.T) {
	for _, tc := range []struct {
		got    [3]int
		tooOld bool
	}{
		{[3]int{0, 12, 0}, false}, // the floor itself is usable
		{[3]int{0, 12, 4}, false},
		{[3]int{0, 13, 0}, false},
		{[3]int{1, 0, 0}, false},
		{[3]int{0, 11, 9}, true},
		{[3]int{0, 9, 5}, true},
		{[3]int{0, 0, 0}, true},
	} {
		if got := less(tc.got, minVersion); got != tc.tooOld {
			t.Errorf("%v below the floor = %v, want %v", tc.got, got, tc.tooOld)
		}
	}
}

func TestErrTooOldNamesBothVersions(t *testing.T) {
	err := ErrTooOld{Got: [3]int{0, 11, 3}}
	if msg := err.Error(); msg != "nvim 0.11.3 is below the 0.12.0 floor" {
		t.Errorf("Error() = %q", msg)
	}
}

// TestVersionOfRejectsEveryShapeItDoesNotUnderstand. A reply this cannot read
// has to be an error, not a zero version: zero would be read as an ancient
// Neovim and rejected for the wrong reason, and the fallback would be right by
// accident rather than by decision.
func TestVersionOfRejectsEveryShapeItDoesNotUnderstand(t *testing.T) {
	good := []any{int64(1), map[string]any{
		"version": map[string]any{"major": int64(0), "minor": int64(12), "patch": int64(4)},
	}}
	got, err := versionOf(good)
	if err != nil {
		t.Fatalf("a well-formed reply was rejected: %v", err)
	}
	if got != [3]int{0, 12, 4} {
		t.Fatalf("version = %v, want 0.12.4", got)
	}

	for _, tc := range []struct {
		name string
		info []any
	}{
		{"empty", nil},
		{"channel id alone", []any{int64(1)}},
		{"metadata is not a map", []any{int64(1), "nope"}},
		{"no version key", []any{int64(1), map[string]any{"functions": []any{}}}},
		{"version is not a map", []any{int64(1), map[string]any{"version": "0.12.4"}}},
		{"a field is missing", []any{int64(1), map[string]any{
			"version": map[string]any{"major": int64(0), "minor": int64(12)},
		}}},
		{"a field is not an integer", []any{int64(1), map[string]any{
			"version": map[string]any{"major": int64(0), "minor": "twelve", "patch": int64(4)},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := versionOf(tc.info); err == nil {
				t.Errorf("versionOf(%v) was accepted", tc.info)
			}
		})
	}
}

// TestToIntAcceptsEveryIntegerMessagePackMayDecodeTo: the wire format picks the
// narrowest type that fits, so the same field arrives as different Go types
// depending on the value.
func TestToIntAcceptsEveryIntegerMessagePackMayDecodeTo(t *testing.T) {
	for _, v := range []any{int64(12), uint64(12), int(12), float64(12)} {
		if n, ok := toInt(v); !ok || n != 12 {
			t.Errorf("toInt(%T) = %d, %v", v, n, ok)
		}
	}
	for _, v := range []any{"12", nil, true, []any{12}} {
		if _, ok := toInt(v); ok {
			t.Errorf("toInt(%T) was accepted", v)
		}
	}
}

// TestStartWithoutABinaryFails is the detection half of the gate: no nvim on
// PATH at all is the commonest reason this pane cannot run, and the caller
// reads it as "use the textarea for the rest of the session".
func TestStartWithoutABinaryFails(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory, so the lookup finds nothing

	if _, err := Start(Options{Cols: 80, Rows: 24}); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("err = %v, want ErrNoBinary", err)
	}
}

// TestStartWithAnUnusableBinaryFails covers everything between finding a
// binary and having a usable session: one that is not Neovim, or is too broken
// to answer, must fail rather than leave a half-built pane behind.
func TestStartWithAnUnusableBinaryFails(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "nvim")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	sess, err := Start(Options{Cols: 80, Rows: 24})
	if err == nil {
		_ = sess.Stop()
		t.Fatal("a binary that answers nothing was accepted")
	}
	if errors.Is(err, ErrNoBinary) {
		t.Fatalf("err = %v, want the failure to name the round trip that failed", err)
	}
}

func TestConfigPathFollowsTheAppName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := filepath.Join(dir, "dbquery", "init.lua")
	if got := ConfigPath(); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
	if HasConfig() {
		t.Error("HasConfig() is true with no file there")
	}

	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("-- user preference\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasConfig() {
		t.Error("HasConfig() is false with the file in place")
	}
}
