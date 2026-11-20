package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/geraldcsoftware/db-query/internal/remembered"
)

// markFlagged records whether fs was given --host/-H or --database/-d. It adds
// to what c already holds, because a subcommand's flags start from the root's:
// a host given before the command is as much a flag as one given after it.
func markFlagged(c *commonFlags, fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "host", "H":
			c.hostFlag = true
		case "database", "d":
			c.databaseFlag = true
		}
	})
}

// targetMemo is one run's dealings with the remembered target: what was
// remembered when the run started, where the run's own host and database came
// from, and whether a remembered value filled a gap. An inactive memo neither
// reads nor writes.
type targetMemo struct {
	active bool
	prev   remembered.Target
	ok     bool
	given  remembered.Given
	used   bool
}

// resolveTarget records where c's host and database came from and, when the
// run is active, fills their gaps from the remembered target. Callers make a
// run active only on a terminal, and a query only when it is neither a dry run
// nor carries a precheck token: the gate's dry run never sees a terminal, so
// the run it lets through must resolve the same target the dry run did.
func resolveTarget(c *commonFlags, active bool) targetMemo {
	m := targetMemo{
		active: active,
		given: remembered.Given{
			Host:         c.host,
			Database:     c.database,
			HostFlag:     c.hostFlag,
			DatabaseFlag: c.databaseFlag,
		},
	}
	if !active {
		return m
	}
	m.prev, m.ok = remembered.Read()
	c.host, c.database, m.used = remembered.Fill(m.prev, m.ok, c.host, c.database)
	return m
}

// note says on stderr that the run is going to a remembered target. It is
// plain text whatever --output asks for: it only appears on a terminal, where
// no program reads stderr.
func (m targetMemo) note(stderr io.Writer, host, database string) {
	if !m.used {
		return
	}
	if database == "" {
		fmt.Fprintf(stderr, "db-query: %s (remembered)\n", host)
		return
	}
	fmt.Fprintf(stderr, "db-query: %s / %s (remembered)\n", host, database)
}

// save remembers the run's flagged host and database once the run has reached
// its target: its connection opened, or it succeeded without one (a schema
// answered from its cache). A failed write is ignored; the run's own result
// stands.
func (m targetMemo) save(reached bool) {
	if !m.active || !reached {
		return
	}
	if next, changed := remembered.Next(m.prev, m.ok, m.given); changed {
		_ = remembered.Write(next)
	}
}
