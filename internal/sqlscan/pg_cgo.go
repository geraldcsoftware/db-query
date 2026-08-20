//go:build cgo

package sqlscan

import (
	"fmt"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// pgGrammar names what decided a postgres verdict. It reaches the dry-run
// document and the audit record, where a verdict has to be reproducible: it is
// a claim about the text under one specific grammar, not a timeless one.
const pgGrammar = "pg_query_go/v6 (PostgreSQL 17 grammar)"

// readNodes is an allowlist, and that direction is the point. A denylist of
// dangerous statement types would admit anything a future PostgreSQL release
// adds, because a type nobody has enumerated yet is absent from it. Listing
// what is known to be read-only means an unrecognised statement classifies
// opaque, which §13.12 treats as destructive.
//
// ExplainStmt is conditional and handled separately: EXPLAIN plans, but
// EXPLAIN ANALYZE executes.
var readNodes = map[string]bool{
	"SelectStmt":       true,
	"VariableShowStmt": true, // SHOW
}

// adminFuncs are functions whose call is itself the dangerous act, so the
// statement around them reads as ordinary. set_config is the sharpest: it is
// the one confirmed way to turn default_transaction_read_only off from inside
// a submission, and it is reachable from a plain SELECT.
//
// Matching by name holds better than it looks. A parse tree carries a function
// name as an identifier, never as an expression, so building the *argument* by
// concatenation does not hide the *call*: set_config('default_' || 'x', …) is
// still a FuncCall named set_config. Renaming reach is the limit, not
// obfuscation, and grants remain the control (§13.12).
var adminFuncs = map[string]bool{
	"set_config":              true, // turns the read-only GUC off
	"pg_terminate_backend":    true,
	"pg_cancel_backend":       true,
	"pg_reload_conf":          true,
	"pg_rotate_logfile":       true,
	"pg_read_file":            true,
	"pg_read_binary_file":     true,
	"pg_ls_dir":               true,
	"pg_stat_file":            true,
	"lo_import":               true,
	"lo_export":               true,
	"dblink":                  true, // runs SQL on another server
	"dblink_exec":             true,
	"query_to_xml":            true, // executes a query passed as text
	"pg_logical_emit_message": true,
}

// destructiveNodes classify above write: they lose data or schema outright.
var destructiveNodes = map[string]bool{
	"DropStmt": true, "TruncateStmt": true, "DeleteStmt": true,
	"DropdbStmt": true, "DropRoleStmt": true, "DropTableSpaceStmt": true,
}

// adminNodes change privileges or server state.
var adminNodes = map[string]bool{
	"GrantStmt": true, "GrantRoleStmt": true, "AlterSystemStmt": true,
	"CreateRoleStmt": true, "AlterRoleStmt": true, "AlterOwnerStmt": true,
	// SET and the transaction statements are here because two of the three
	// confirmed ways to escape default_transaction_read_only are
	// `SET default_transaction_read_only = off` and
	// `BEGIN; SET TRANSACTION READ WRITE`. Neither has a use through this
	// tool that is worth the hole.
	"VariableSetStmt": true, "TransactionStmt": true,
}

// writeNodes modify rows or schema without losing either outright.
var writeNodes = map[string]bool{
	"InsertStmt": true, "UpdateStmt": true, "MergeStmt": true,
	"CreateStmt": true, "AlterTableStmt": true, "IndexStmt": true,
	"ViewStmt": true, "CreateFunctionStmt": true, "CopyStmt": true,
	"RenameStmt": true, "CreateTableAsStmt": true, "RefreshMatViewStmt": true,
}

// ClassifyPostgres decides from the text alone, using PostgreSQL's own
// grammar. It never connects, so it holds when the database is unreachable.
func ClassifyPostgres(sql string) Verdict {
	tree, err := pg.Parse(sql)
	if err != nil {
		// Unparseable is not thereby safe. A client directive reaches here as
		// a syntax error too, though the pre-pass has already refused it.
		return Opaque(MechanismParser, pgGrammar, "not parseable as PostgreSQL")
	}
	v := Verdict{Mechanism: MechanismParser, Version: pgGrammar}
	for i, st := range tree.Stmts {
		class, why := classifyStmt(st.Stmt)
		v.Statements = append(v.Statements, Statement{Index: i + 1, Class: class, DecidedBy: why})
	}
	v.Reduce()
	return v
}

// classifyStmt walks one statement whole. Walking rather than reading the top
// node is what catches DML carried in a CTE and the statement inside an
// EXPLAIN: both present a harmless outermost node.
func classifyStmt(n *pg.Node) (Class, string) {
	if n == nil {
		return ClassOpaque, "empty statement node"
	}
	top := nodeName(n)

	worst, why := ClassRead, top
	note := func(c Class, reason string) {
		if c > worst {
			worst, why = c, reason
		}
	}

	seenAllowed := false
	walkNodes(n.ProtoReflect(), func(name string, m protoreflect.Message) {
		switch {
		case destructiveNodes[name]:
			note(ClassDestructive, name)
		case adminNodes[name]:
			note(ClassAdmin, name)
		case writeNodes[name]:
			note(ClassWrite, name)
		case name == "ExplainStmt":
			if explainAnalyzes(m) {
				// ANALYZE runs the plan, including any volatile function in it.
				note(ClassWrite, "EXPLAIN ANALYZE executes the statement")
			} else {
				seenAllowed = true
			}
		case name == "FuncCall":
			if fn := funcName(m); adminFuncs[fn] {
				note(ClassAdmin, "calls "+fn)
			}
		case readNodes[name]:
			seenAllowed = true
		}
	})

	if worst == ClassRead && !seenAllowed {
		// Nothing in the statement matched anything known. Under an allowlist
		// that is the case to refuse, not the case to wave through.
		return ClassOpaque, "unrecognised statement type " + top
	}
	if worst == ClassRead {
		return ClassRead, top
	}
	return worst, why
}

// walkNodes visits every message in the tree, naming each.
func walkNodes(m protoreflect.Message, visit func(string, protoreflect.Message)) {
	visit(string(m.Descriptor().Name()), m)
	m.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := val.List()
			for i := 0; i < l.Len(); i++ {
				walkNodes(l.Get(i).Message(), visit)
			}
		case fd.Kind() == protoreflect.MessageKind:
			walkNodes(val.Message(), visit)
		}
		return true
	})
}

// nodeName reports the concrete statement type behind the oneof wrapper.
func nodeName(n *pg.Node) string {
	if n == nil || n.Node == nil {
		return "nil"
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", n.Node), "*pg_query.Node_")
}

// explainAnalyzes reports whether an ExplainStmt carries ANALYZE.
func explainAnalyzes(m protoreflect.Message) bool {
	fd := m.Descriptor().Fields().ByName("options")
	if fd == nil {
		return false
	}
	found := false
	l := m.Get(fd).List()
	for i := 0; i < l.Len(); i++ {
		walkNodes(l.Get(i).Message(), func(name string, dm protoreflect.Message) {
			if name != "DefElem" {
				return
			}
			nf := dm.Descriptor().Fields().ByName("defname")
			if nf != nil && strings.EqualFold(dm.Get(nf).String(), "analyze") {
				found = true
			}
		})
	}
	return found
}

// funcName joins a FuncCall's dotted name, lowercased for comparison.
func funcName(m protoreflect.Message) string {
	fd := m.Descriptor().Fields().ByName("funcname")
	if fd == nil {
		return ""
	}
	var parts []string
	l := m.Get(fd).List()
	for i := 0; i < l.Len(); i++ {
		sf := l.Get(i).Message().Descriptor().Fields().ByName("string")
		if sf == nil {
			continue
		}
		sv := l.Get(i).Message().Get(sf).Message()
		vf := sv.Descriptor().Fields().ByName("sval")
		if vf == nil {
			continue
		}
		parts = append(parts, strings.ToLower(sv.Get(vf).String()))
	}
	// A schema-qualified call is matched on its final element, so
	// pg_catalog.set_config is caught alongside set_config.
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
