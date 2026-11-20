// Package remembered keeps the host and database a manual terminal run last
// pointed at, so a later manual run that leaves them out points there again.
// It owns the file and the two rules: which gaps the remembered target fills,
// and which pair a run leaves remembered. Callers decide when either applies.
package remembered

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/geraldcsoftware/db-query/internal/cache"
)

// Target is the host and database a manual run last pointed at, and the
// config file whose host it names: the same label in another config file can
// be another server.
type Target struct {
	Host     string `json:"host"`
	Database string `json:"database,omitempty"`
	Config   string `json:"config,omitempty"`
}

// Given is where one run's host and database came from, before the remembered
// target filled any gap.
type Given struct {
	Host, Database         string // from a flag or the environment; empty when neither gave one
	HostFlag, DatabaseFlag bool   // given as a flag, before or after the command
}

// Path is the file the remembered target is kept in.
func Path() string { return filepath.Join(cache.Dir("target"), "last.json") }

// Read returns the target remembered under the config file config. ok is
// false when nothing usable is remembered for it: the file is missing,
// unreadable or not JSON, names no host, or was written under another config
// file.
func Read(config string) (Target, bool) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return Target{}, false
	}
	var t Target
	if json.Unmarshal(b, &t) != nil || t.Host == "" || t.Config != config {
		return Target{}, false
	}
	return t, true
}

// Write replaces the remembered target. The JSON goes to a temporary file in
// the same directory, which is then renamed over the old file, so a reader or
// a second terminal writing at the same moment never sees a partial file; the
// last writer wins. The directory is private to the user (0700), and so is
// the file (0600, os.CreateTemp's mode).
func Write(t Target) error {
	dir := cache.Dir("target")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "last-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // fails harmlessly once the rename has moved it
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "last.json"))
}

// Fill fills the gaps in a run's host and database from the remembered target
// t, where ok false means nothing is remembered. An empty host takes the
// remembered host. An empty database takes the remembered database only when
// the host in use is the remembered host: a database belongs to the host it
// was remembered with. used reports whether either gap was filled.
func Fill(t Target, ok bool, host, database string) (string, string, bool) {
	if !ok {
		return host, database, false
	}
	used := false
	if host == "" {
		host, used = t.Host, true
	}
	if database == "" && host == t.Host && t.Database != "" {
		database, used = t.Database, true
	}
	return host, database, used
}

// Next returns the pair to remember after a run that reached its target, and
// whether it differs from prev (ok false: nothing was remembered). Only values
// given as flags are remembered, and nothing is when the host came from
// DB_QUERY_HOST: an exported host is a standing choice, not a pointer to move.
// A flagged host replaces the remembered one and, when it differs, drops the
// remembered database; a flagged database replaces the remembered database.
func Next(prev Target, ok bool, g Given) (Target, bool) {
	if !g.HostFlag && !g.DatabaseFlag {
		return prev, false
	}
	if g.Host != "" && !g.HostFlag {
		return prev, false
	}
	var next Target
	if ok {
		next = prev
	}
	if g.HostFlag && g.Host != next.Host {
		next = Target{Host: g.Host}
	}
	if next.Host == "" {
		return prev, false
	}
	if g.DatabaseFlag {
		next.Database = g.Database
	}
	return next, !ok || next != prev
}
