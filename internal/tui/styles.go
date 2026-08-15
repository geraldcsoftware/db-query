package tui

import "charm.land/lipgloss/v2"

// The palette is a dark-terminal scheme: a mint accent for the things a user
// navigates by (the app name, section labels, the selection bar, keystrokes),
// pink for numeric values, and three neutrals separating body text, secondary
// text and the separator rules. Foregrounds only, except the selection bar —
// nothing paints a full-screen background, so the TUI sits on whatever
// background the user's terminal already has instead of fighting it.
var (
	colorAccent = lipgloss.Color("#4ADE9B")
	colorText   = lipgloss.Color("#E5E7EB")
	colorMuted  = lipgloss.Color("#6B7280")
	colorRule   = lipgloss.Color("#374151")
	colorNumber = lipgloss.Color("#F472B6")
	colorSelFg  = lipgloss.Color("#0B1220")
	colorError  = lipgloss.Color("#F87171")
	colorBusy   = lipgloss.Color("#FBBF24")

	// colorPopupBg is the only filled background in the TUI. Everything else
	// sits on whatever background the terminal already has, but a box floating
	// over the panes has to be opaque or the text underneath shows through it.
	colorPopupBg = lipgloss.Color("#111827")
)

// focusMarker sits in the first cell of the focused pane's label row. Focus is
// signalled by two independent cues — this glyph and the accent colour — so it
// survives a terminal that renders without colour, where the colour is
// stripped and the marker is not.
const focusMarker = "▌"

var (
	// ruleStyle colours the horizontal and vertical separators. They are
	// structure, not content, so they sit a step below even muted text.
	ruleStyle = lipgloss.NewStyle().Foreground(colorRule)

	// paneLabelFocusedStyle and paneLabelStyle draw a pane's uppercase section
	// label; paneMetaStyle draws the summary right-aligned on the same row.
	paneLabelFocusedStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	paneLabelStyle        = lipgloss.NewStyle().Foreground(colorMuted)
	paneMetaStyle         = lipgloss.NewStyle().Foreground(colorMuted)

	// selectionStyle is the full-width bar on the row under a list pane's
	// cursor. It is the one place the TUI paints a background, because a bar
	// is what makes the current row findable in a long list at a glance.
	selectionStyle = lipgloss.NewStyle().Foreground(colorSelFg).Background(colorAccent)

	// listMarkerStyle, listNameStyle and listMetaStyle give a sidebar row its
	// three-tone reading: the disclosure marker and the trailing detail
	// recede, the name does not.
	listMarkerStyle = lipgloss.NewStyle().Foreground(colorMuted)
	listNameStyle   = lipgloss.NewStyle().Foreground(colorText)
	listMetaStyle   = lipgloss.NewStyle().Foreground(colorMuted)

	// hintStyle carries the Schema pane's stand-in text when there is no
	// cached schema to browse.
	hintStyle = lipgloss.NewStyle().Foreground(colorMuted)

	// appNameStyle and the connection styles split the top bar: the build on
	// the left, the resolved connection on the right with the database name in
	// the accent colour, since that is the part a user re-reads before running
	// something destructive.
	appNameStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	appVersionStyle = lipgloss.NewStyle().Foreground(colorMuted)
	databaseStyle   = lipgloss.NewStyle().Foreground(colorAccent)
	connectionStyle = lipgloss.NewStyle().Foreground(colorMuted)
	connectionSep   = lipgloss.NewStyle().Foreground(colorRule)

	// The startup picker's own scale, from quietest to loudest: introStyle
	// carries the explanation nobody needs to read twice, introLabelStyle and
	// introValueStyle record a choice already made, and pickerHeadingStyle
	// names the list that wants attention now. The explanation is deliberately
	// the dimmest thing on screen — it is context, not an instruction to
	// follow.
	introStyle         = lipgloss.NewStyle().Foreground(colorMuted)
	introLabelStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	introValueStyle    = lipgloss.NewStyle().Foreground(colorText)
	pickerHeadingStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	// filterPromptStyle inverts its label so the filter reads as an input the
	// keystrokes are going into, rather than as one more line of text.
	filterPromptStyle = lipgloss.NewStyle().Foreground(colorSelFg).Background(colorMuted)
	filterTextStyle   = lipgloss.NewStyle().Foreground(colorText)

	// hintKeyStyle and hintDescStyle give the bottom bar its two-tone reading:
	// the keystroke stands out in the accent colour, its effect stays in body
	// text rather than dimming — the bar is meant to be read at a glance.
	hintKeyStyle  = lipgloss.NewStyle().Foreground(colorAccent)
	hintDescStyle = lipgloss.NewStyle().Foreground(colorText)
	hintSepStyle  = lipgloss.NewStyle().Foreground(colorRule)

	// switcherBoxStyle frames the database-switch popup. It is the one thing
	// that paints a filled background over the panes: a floating box has to
	// hide what it covers, or the text behind it reads through as noise.
	switcherBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Background(colorPopupBg).
				Padding(0, 1)

	// errorStyle marks a failed run's text so it cannot be mistaken for a
	// result; runningStyle and statusStyle carry the bottom bar's transient
	// states.
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	runningStyle = lipgloss.NewStyle().Foreground(colorBusy)
	statusStyle  = lipgloss.NewStyle().Foreground(colorBusy)

	// resultHeaderStyle, resultGutterStyle, resultTextStyle and
	// resultNumberStyle style the Results table by role: chrome recedes, text
	// cells are body text, and numeric cells are pink so a column of amounts
	// reads as a column of amounts.
	resultHeaderStyle = lipgloss.NewStyle().Foreground(colorMuted)
	resultGutterStyle = lipgloss.NewStyle().Foreground(colorMuted)
	resultTextStyle   = lipgloss.NewStyle().Foreground(colorText)
	resultNumberStyle = lipgloss.NewStyle().Foreground(colorNumber)
)
