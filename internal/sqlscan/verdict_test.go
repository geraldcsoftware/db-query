package sqlscan

import "testing"

func TestReduceTakesTheLeastSafeStatement(t *testing.T) {
	tests := []struct {
		name    string
		classes []Class
		want    Class
	}{
		{"all reads", []Class{ClassRead, ClassRead}, ClassRead},
		{"one write among reads", []Class{ClassRead, ClassWrite, ClassRead}, ClassWrite},
		{"destructive beats write", []Class{ClassWrite, ClassDestructive}, ClassDestructive},
		{"admin beats destructive", []Class{ClassDestructive, ClassAdmin}, ClassAdmin},
		{
			// The point of ordering opaque highest: an unclassified statement
			// must not hide behind classified ones.
			"opaque beats everything", []Class{ClassRead, ClassAdmin, ClassOpaque}, ClassOpaque,
		},
		{"no statements is opaque, not clean", nil, ClassOpaque},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v Verdict
			for i, c := range tt.classes {
				v.Statements = append(v.Statements, Statement{Index: i + 1, Class: c})
			}
			v.Reduce()
			if v.Class != tt.want {
				t.Errorf("got %s, want %s", v.Class, tt.want)
			}
			if v.ClassName != tt.want.String() {
				t.Errorf("ClassName %q does not match class %s", v.ClassName, tt.want)
			}
		})
	}
}

func TestPermittedFollowsTheHostPosture(t *testing.T) {
	// A read-only host permits reads and nothing else.
	for _, c := range []Class{ClassWrite, ClassDestructive, ClassAdmin, ClassOpaque} {
		if c.Permitted(true) {
			t.Errorf("%s must not be permitted on a read-only host", c)
		}
	}
	if !ClassRead.Permitted(true) {
		t.Error("a read must be permitted on a read-only host")
	}

	// A host declared writable permits writes too, but never loses the
	// protection that matters: dropping and privilege changes still meet a
	// human, and so does anything unclassifiable.
	if !ClassWrite.Permitted(false) {
		t.Error("a write must be permitted on a writable host")
	}
	for _, c := range []Class{ClassDestructive, ClassAdmin, ClassOpaque} {
		if c.Permitted(false) {
			t.Errorf("%s must meet a human even on a writable host", c)
		}
	}
}

func TestDecideNamesTheDecidingStatement(t *testing.T) {
	v := Verdict{Statements: []Statement{
		{Index: 1, Class: ClassRead, DecidedBy: "SelectStmt"},
		{Index: 2, Class: ClassDestructive, DecidedBy: "DeleteStmt"},
	}}
	v.Reduce()
	d := Decide(v, true)
	if d.Action != ActionChallenge {
		t.Errorf("action: got %q, want challenge", d.Action)
	}
	if d.ReasonCode != ReasonClassDestructive {
		t.Errorf("reason code: got %q, want %q", d.ReasonCode, ReasonClassDestructive)
	}
	// A refusal that cannot say which statement caused it is not actionable.
	if want := "statement 2"; !contains(d.Reason, want) {
		t.Errorf("reason %q does not name the deciding statement", d.Reason)
	}
}

func TestDecideAllowsReads(t *testing.T) {
	v := Verdict{Statements: []Statement{{Index: 1, Class: ClassRead, DecidedBy: "SelectStmt"}}}
	v.Reduce()
	if d := Decide(v, true); d.Action != ActionAllow || d.ReasonCode != ReasonOKRead {
		t.Errorf("got %+v, want allow/OK_READ", d)
	}
}

func TestOpaqueVerdictIsWellFormed(t *testing.T) {
	// Every failure path must yield a verdict a caller cannot mistake for
	// permission, rather than a zero value.
	v := Opaque(MechanismParser, "x", "parser unavailable")
	if v.Class != ClassOpaque || v.ClassName != "opaque" || len(v.Statements) != 1 {
		t.Errorf("got %+v, want a one-statement opaque verdict", v)
	}
	if Decide(v, true).Action == ActionAllow {
		t.Error("an opaque verdict must never allow")
	}
}

func TestUnknownClassDenies(t *testing.T) {
	if Class(99).String() != "opaque" || Class(99).Permitted(true) || Class(99).Permitted(false) {
		t.Error("an out-of-range class must read as opaque and never be permitted")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
