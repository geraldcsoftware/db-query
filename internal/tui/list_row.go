package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// listRow lays out one row of a sidebar list: a one-cell indent, an optional
// muted marker, the name, and an optional detail right-aligned against the
// pane's far edge. The row is always exactly w cells, so the row under the
// cursor renders as a bar spanning the pane's full width — padding included —
// rather than as a highlight that stops at the end of the text.
//
// The detail is dropped rather than squeezed when the pane is too narrow to
// hold it with a gap after the name; the name itself is clipped last.
func listRow(w int, selected bool, marker, name, detail string) string {
	if w <= 0 {
		return ""
	}
	lead := ansi.Truncate(" "+marker, w, "")
	room := w - ansi.StringWidth(lead)

	// The detail carries its own trailing pad cell, mirroring the indent.
	detailW := 0
	if detail != "" {
		detailW = ansi.StringWidth(detail) + 1
		if detailW+ansi.StringWidth(name)+1 > room {
			detail, detailW = "", 0
		}
	}
	if ansi.StringWidth(name) > room-detailW {
		name = ansi.Truncate(name, room-detailW, "")
	}
	gap := strings.Repeat(" ", room-detailW-ansi.StringWidth(name))
	trail := ""
	if detail != "" {
		trail = " "
	}

	if selected {
		return selectionStyle.Render(lead + name + gap + detail + trail)
	}
	// Empty segments are left unstyled: rendering one would emit an escape
	// sequence pair around nothing, for no visible gain.
	out := listMarkerStyle.Render(lead) + listNameStyle.Render(name) + gap
	if detail != "" {
		out += listMetaStyle.Render(detail) + trail
	}
	return out
}
