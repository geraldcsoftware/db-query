package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/geraldcsoftware/db-query/internal/savedquery"
)

// savedPane browses the saved-query store — a flat category/name list, no
// search/filter in v1 (design §2).
type savedPane struct {
	queries []savedquery.SavedQuery
	cursor  int
}

func newSavedPane() savedPane {
	list, _ := savedquery.List("") // a missing store is an empty list, not an error
	return savedPane{queries: list}
}

func (p savedPane) update(msg tea.Msg) (savedPane, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok || len(p.queries) == 0 {
		return p, nil
	}
	switch keyMsg.String() {
	case "up":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down":
		if p.cursor < len(p.queries)-1 {
			p.cursor++
		}
	}
	return p, nil
}

func (p savedPane) selected() (savedquery.SavedQuery, bool) {
	if p.cursor < 0 || p.cursor >= len(p.queries) {
		return savedquery.SavedQuery{}, false
	}
	return p.queries[p.cursor], true
}

func (p savedPane) view() string {
	var out string
	for i, q := range p.queries {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		out += cursor + q.Category + "/" + q.Name + "\n"
	}
	return out
}
