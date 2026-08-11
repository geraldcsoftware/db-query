package tui

import (
	"bytes"
	"fmt"
	"io"

	"github.com/geraldcsoftware/db-query/internal/dblist"
	"github.com/geraldcsoftware/db-query/internal/render"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// bootstrap resolves the session's host/credential once, prompting for
// whichever of the two the invocation left open. A picker's choice behaves
// exactly as if the matching flag had been passed; nothing is written back to
// config or the environment (design §4). Quitting either picker returns a
// zero-value Resolved with exit code 0 — nothing to do, not a failure.
func bootstrap(c session.CommonFlags, stderr io.Writer) (session.Resolved, int) {
	hostPicked := false
	if c.Host == "" {
		chosen, code := pickHost(c, stderr)
		if code != 0 || chosen == "" {
			return session.Resolved{}, code
		}
		c.Host = chosen
		hostPicked = true
	}
	r, code := session.Setup(c, stderr)
	if code != 0 {
		return session.Resolved{}, code
	}
	if !needsDatabase(c, r, hostPicked) {
		return r, 0
	}
	names, code := databaseChoices(r, c, stderr)
	if code != 0 {
		return session.Resolved{}, code
	}
	p, err := runPicker(newDatabasePicker(names, r.Host.Database))
	if err != nil {
		fmt.Fprintln(stderr, "db-query: "+err.Error())
		return session.Resolved{}, 1
	}
	if p.chosen == "" {
		return session.Resolved{}, 0 // user quit the picker; not an error, just nothing to do
	}
	r.Host.Database = p.chosen
	return r, 0
}

// pickHost prompts for one of the configured host names. An empty name
// alongside exit code 0 means the user quit the picker.
func pickHost(c session.CommonFlags, stderr io.Writer) (string, int) {
	cfg, err := session.LoadConfig(c.Config)
	if err != nil {
		fmt.Fprintln(stderr, "db-query: "+err.Error())
		return "", 1
	}
	names := cfg.HostNames()
	if len(names) == 0 {
		fmt.Fprintln(stderr, "db-query: no hosts configured; add one under [hosts.<name>] first")
		return "", 1
	}
	p, err := runPicker(newHostPicker(names))
	if err != nil {
		fmt.Fprintln(stderr, "db-query: "+err.Error())
		return "", 1
	}
	return p.chosen, 0
}

// needsDatabase reports whether startup should prompt for a database. An
// explicit --database/DB_QUERY_DATABASE is always taken as given. Otherwise
// the prompt is for the two cases where the session has no database the user
// deliberately chose: one picked interactively, since that invocation is
// already a "choose my session" flow and the host's own default may not be
// what is wanted; and one that resolved to no database at all, which would
// otherwise open a TUI whose queries cannot run. A --host whose config block
// names a database launches straight into the TUI, unprompted.
func needsDatabase(c session.CommonFlags, r session.Resolved, hostPicked bool) bool {
	return c.Database == "" && (hostPicked || r.Host.Database == "")
}

// databaseChoices lists the databases to offer for a resolved host. The live
// listing leads — it is what `db-query databases` shows, so the picker offers
// what the host actually holds right now, and it refreshes the completion
// cache on the way past. A host that cannot be reached this minute still has a
// usable session if an earlier listing cached its names, so the cache stands
// in rather than aborting startup. Only when neither yields a name does the
// listing's own failure surface, with its exit code: a TUI with nothing to
// query is worse than a clear error.
func databaseChoices(r session.Resolved, c session.CommonFlags, stderr io.Writer) ([]string, int) {
	// The live error is held back rather than printed as it happens: it is
	// noise if the cache covers for it, and the picker is about to redraw.
	var listErr bytes.Buffer
	rows, code := session.ListDatabases(r, c, &listErr)
	if code == 0 {
		if names := session.DatabaseNames(rows); len(names) > 0 {
			return names, 0
		}
	}
	if cached, err := dblist.Read(dblist.CachePath(r.Host.Name)); err == nil && len(cached) > 0 {
		return cached, 0
	}
	if listErr.Len() > 0 {
		io.Copy(stderr, &listErr) // already rendered in the caller's output format
	} else {
		render.Error(stderr, c.Output, fmt.Sprintf("host %s reports no databases to choose from", r.Host.Name))
	}
	if code == 0 {
		code = 1
	}
	return nil, code
}
