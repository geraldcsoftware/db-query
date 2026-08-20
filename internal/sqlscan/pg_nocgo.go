//go:build !cgo

package sqlscan

// ClassifyPostgres without cgo has no grammar to classify against, because the
// vendored PostgreSQL parser needs it. It refuses rather than allowing.
//
// A safety component must never be the thing that quietly vanishes from a
// build. This stand-in exists so that a CGO_ENABLED=0 build still compiles and
// still runs, but reports every postgres submission as opaque with a reason
// naming the absent parser, which §13.12 turns into a refusal.
func ClassifyPostgres(string) Verdict {
	return Opaque(MechanismParser, "unavailable",
		"built without cgo: the PostgreSQL grammar is not compiled into this binary")
}

// ParserAvailable reports whether this build carries the PostgreSQL grammar.
func ParserAvailable() bool { return false }
