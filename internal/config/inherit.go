package config

import (
	"fmt"
	"sort"
	"strings"
)

// Inheritance operates on raw key maps, before any key is interpreted. Merging
// first and interpreting after means an inherited key goes through exactly the
// same validation as a literal one, and there is no need to tell an unset field
// from one explicitly set to its zero value.

// resolved is one host's fully merged key set together with the origin of each
// key — "host lionel" or "profile eus". Origins serve two callers: error
// messages, which must blame the file section that carries the mistake, and
// `db-query hosts <name>`, which shows where each effective value came from.
type resolved struct {
	keys    map[string]any
	origins map[string]string
}

// layer is one link in an inheritance chain: a labelled set of raw keys.
type layer struct {
	label string
	keys  map[string]any
}

// flatten walks the inherit chain from own up to its root and merges it into a
// single key map. Ancestors are applied first and the entity's own keys last,
// so the nearest definition of a key wins.
//
// Profiles are only validated through the hosts that inherit them; a profile no
// host reaches is inert, since a profile is partial by design and cannot be
// checked for completeness on its own.
func flatten(label string, own map[string]any, profiles map[string]map[string]any) (resolved, error) {
	chain := []layer{{label: label, keys: own}}
	seen := map[string]bool{}
	var path []string

	cur, curLabel := own, label
	for {
		raw, ok := cur["inherit"]
		if !ok {
			break
		}
		parent, ok := raw.(string)
		if !ok {
			return resolved{}, fmt.Errorf("%s: inherit must be a profile name (a string), got %T", curLabel, raw)
		}
		if parent == "" {
			return resolved{}, fmt.Errorf("%s: inherit is empty", curLabel)
		}
		if seen[parent] {
			return resolved{}, fmt.Errorf("%s: inherit cycle %s", curLabel, strings.Join(append(path, parent), " → "))
		}
		keys, ok := profiles[parent]
		if !ok {
			return resolved{}, fmt.Errorf("%s: unknown profile %q (configured: %v)", curLabel, parent, profileNames(profiles))
		}
		seen[parent] = true
		path = append(path, parent)
		curLabel = "profile " + parent
		cur = keys
		chain = append(chain, layer{label: curLabel, keys: keys})
	}

	r := resolved{keys: make(map[string]any), origins: make(map[string]string)}
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range chain[i].keys {
			if k == "inherit" {
				continue // consumed here; never interpreted, never passed to an adapter
			}
			r.keys[k] = v
			r.origins[k] = chain[i].label
		}
	}
	return r, nil
}

func profileNames(profiles map[string]map[string]any) []string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
