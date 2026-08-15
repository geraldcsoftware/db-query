package tui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/geraldcsoftware/db-query/internal/config"
	"github.com/geraldcsoftware/db-query/internal/schema"
	"github.com/geraldcsoftware/db-query/internal/session"
)

// noSchemaMark tags a database with no cached schema wherever one is offered
// for selection, so the cost of choosing it is visible before it is chosen
// rather than at the confirmation afterwards.
const noSchemaMark = "no schema"

// needsIntrospect reports whether a database on this host would open with an
// empty Schema pane. It is a stat, not a query, so it is cheap enough to run
// for every name in a list on every keystroke.
func needsIntrospect(host config.HostConfig, database string) bool {
	return !schema.Exists(schema.CachePath(host.Host, database))
}

// schemaMarks tags the names that have never been introspected. The map is
// keyed on the name because that is what the chooser has in hand when it draws
// a row.
func schemaMarks(host config.HostConfig, names []string) map[string]string {
	marks := make(map[string]string, len(names))
	for _, name := range names {
		if needsIntrospect(host, name) {
			marks[name] = noSchemaMark
		}
	}
	return marks
}

// introspect builds the schema cache for one database on an already-resolved
// host. The session is taken by value, so pointing it at another database to
// run the catalogue query cannot disturb the caller's own.
//
// session.BuildSchema renders its failures rather than returning them, so they
// are captured and handed back as an error: the caller decides whether that
// belongs on stderr or in a status strip, and neither wants it printed from in
// here.
func introspect(r session.Resolved, database string, c session.CommonFlags) error {
	r.Host.Database = database
	var out bytes.Buffer
	code := session.BuildSchema(r, schema.CachePath(r.Host.Host, database), c, &out)
	if code == 0 {
		return nil
	}
	if msg := strings.TrimSpace(out.String()); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("introspecting %s failed with exit code %d", database, code)
}
