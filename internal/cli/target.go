package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/geraldcsoftware/db-query/internal/config"
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

// configInUse is the config file a run reads (--config, else DB_QUERY_CONFIG,
// else the default location), made absolute so the same file compares equal
// from any working directory.
func configInUse(path string) string {
	if path == "" {
		path = config.DefaultPath()
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// targetMemo is one run's dealings with the remembered target: the config file
// it is remembered under, what was remembered when the run started, and where
// the run's own host and database came from. An inactive memo neither reads
// nor writes.
type targetMemo struct {
	active bool
	config string
	prev   remembered.Target
	ok     bool
	given  remembered.Given
}

// resolveTarget records where c's host and database came from and, when the
// run is active, fills their gaps from the target remembered under the run's
// config file. When it fills one, it says so on stderr before the run sets up
// its target, so a remembered host that is no longer configured is named
// before the error it causes. The line is plain text whatever --output asks
// for: it only appears on a terminal, where no program reads stderr.
//
// Callers make a run active only on a terminal, and a query only when it is
// neither a dry run nor carries a precheck token: the gate's dry run never
// sees a terminal, so the run it lets through must resolve the same target the
// dry run did.
func resolveTarget(c *commonFlags, active bool, stderr io.Writer) targetMemo {
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
	m.config = configInUse(c.config)
	m.prev, m.ok = remembered.Read(m.config)
	var used bool
	c.host, c.database, used = remembered.Fill(m.prev, m.ok, c.host, c.database)
	switch {
	case !used:
	case c.database == "":
		fmt.Fprintf(stderr, "db-query: %s (remembered)\n", c.host)
	default:
		fmt.Fprintf(stderr, "db-query: %s / %s (remembered)\n", c.host, c.database)
	}
	return m
}

// save remembers the run's flagged host and database, under the run's config
// file, once the run has reached its target: its connection opened, or it
// succeeded without one (a schema answered from its cache). A failed write is
// ignored; the run's own result stands.
func (m targetMemo) save(reached bool) {
	if !m.active || !reached {
		return
	}
	if next, changed := remembered.Next(m.prev, m.ok, m.given); changed {
		next.Config = m.config
		_ = remembered.Write(next)
	}
}
