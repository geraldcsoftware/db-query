package tui

import "github.com/charmbracelet/lipgloss"

// The palette is a dark-terminal scheme: a teal/green accent for structure
// (pane titles, the focused frame, keybinding keys), pink for emphasised
// values, and two neutrals for body and secondary text. Only foregrounds and
// border colours are set — nothing paints a full-screen background, so the
// TUI sits on whatever background the user's terminal already has instead of
// fighting it.
var (
	colorAccent = lipgloss.Color("#4ADE9B")
	colorEmph   = lipgloss.Color("#F472B6")
	colorText   = lipgloss.Color("#E5E7EB")
	colorMuted  = lipgloss.Color("#6B7280")
	colorError  = lipgloss.Color("#F87171")
	colorBusy   = lipgloss.Color("#FBBF24")
)

// Focus is signalled by two independent cues so it survives a terminal (or a
// test harness) that renders without colour: the focused pane is drawn with a
// heavy frame, every other pane with a light rounded one.
var (
	borderFocused   = lipgloss.ThickBorder()
	borderUnfocused = lipgloss.RoundedBorder()
)

var (
	// paneTitleStyle labels a pane inside its own top border rule.
	paneTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	// paneFrameFocusedStyle and paneFrameStyle draw a pane's frame. Neither
	// sets Width or Height: lipgloss sizes a border around the content it is
	// given, adding exactly one cell per side, so a content block that is
	// already exactly (w-2) x (h-2) renders as exactly w x h.
	paneFrameFocusedStyle = lipgloss.NewStyle().Border(borderFocused).BorderForeground(colorAccent)
	paneFrameStyle        = lipgloss.NewStyle().Border(borderUnfocused).BorderForeground(colorMuted)

	// borderRuleFocusedStyle and borderRuleStyle colour the border runes that
	// are composed by hand rather than by lipgloss — the top rule, which
	// carries the pane's title.
	borderRuleFocusedStyle = lipgloss.NewStyle().Foreground(colorAccent)
	borderRuleStyle        = lipgloss.NewStyle().Foreground(colorMuted)

	// appNameStyle and connectionStyle split the top bar: the build on the
	// left, the resolved connection on the right with the database name
	// emphasised, since that is the part a user re-reads before running
	// something destructive.
	appNameStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	appVersionStyle = lipgloss.NewStyle().Foreground(colorMuted)
	connectionStyle = lipgloss.NewStyle().Foreground(colorText)
	databaseStyle   = lipgloss.NewStyle().Foreground(colorEmph)

	// hintKeyStyle and hintDescStyle give the bottom bar its two-tone
	// reading: the keystroke stands out, its effect recedes.
	hintKeyStyle  = lipgloss.NewStyle().Foreground(colorAccent)
	hintDescStyle = lipgloss.NewStyle().Foreground(colorMuted)
	hintSepStyle  = lipgloss.NewStyle().Foreground(colorMuted)

	// errorStyle marks a failed run's text so it cannot be mistaken for a
	// result; runningStyle and statusStyle carry the bottom bar's transient
	// states.
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	runningStyle = lipgloss.NewStyle().Foreground(colorBusy)
	statusStyle  = lipgloss.NewStyle().Foreground(colorEmph)

	// pageIndicatorStyle dims the Results pane's page position so it reads as
	// chrome around the table rather than as another row of data.
	pageIndicatorStyle = lipgloss.NewStyle().Foreground(colorMuted)
)

// paneFrame returns the frame style and border rune set for a pane, given
// whether it currently holds focus.
func paneFrame(focused bool) (lipgloss.Style, lipgloss.Border, lipgloss.Style) {
	if focused {
		return paneFrameFocusedStyle, borderFocused, borderRuleFocusedStyle
	}
	return paneFrameStyle, borderUnfocused, borderRuleStyle
}
