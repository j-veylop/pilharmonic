package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The interface uses one compact, adaptive palette. Keep colour decisions here
// so status, selection, badges and transcript styling stay in the same visual
// language.
const (
	colorInk        = "#E8EDF9"
	colorMuted      = "#8D9AB5"
	colorFaint      = "#5F6D88"
	colorAccent     = "#B7A6FF"
	colorAccentDeep = "#7C6AE8"
	colorCyan       = "#7BE0CF"
	colorBlue       = "#7BA7FF"
	colorGreen      = "#8FE3B0"
	colorAmber      = "#F4C97A"
	colorRed        = "#FF96A5"
	colorPink       = "#FF9ED8"
	colorViolet     = "#C4B0FF"
	colorBorder     = "#35425D"
	colorSurface    = "#232B3D"
)

// The palette adapts to the terminal background: the light variants read as
// the same hierarchy on a white surface without changing any call site.
var (
	ink    = lipgloss.AdaptiveColor{Light: "#1B2232", Dark: colorInk}
	muted  = lipgloss.AdaptiveColor{Light: "#5B667C", Dark: colorMuted}
	accent = lipgloss.AdaptiveColor{Light: "#6450C8", Dark: colorAccent}
	cyan   = lipgloss.AdaptiveColor{Light: "#0E8C7C", Dark: colorCyan}
	border = lipgloss.AdaptiveColor{Light: "#B9C2D4", Dark: colorBorder}
	blue   = lipgloss.AdaptiveColor{Light: "#2F6BD8", Dark: colorBlue}
	green  = lipgloss.AdaptiveColor{Light: "#2E8A57", Dark: colorGreen}
	amber  = lipgloss.AdaptiveColor{Light: "#A66A0A", Dark: colorAmber}
	red    = lipgloss.AdaptiveColor{Light: "#C24055", Dark: colorRed}
	faint  = lipgloss.AdaptiveColor{Light: "#8A94A8", Dark: colorFaint}
	pink   = lipgloss.AdaptiveColor{Light: "#B93B8E", Dark: colorPink}
	violet = lipgloss.AdaptiveColor{Light: "#6B4FD8", Dark: colorViolet}

	surface          = lipgloss.AdaptiveColor{Light: "#E9EDF7", Dark: colorSurface}
	pillBlueBG       = lipgloss.AdaptiveColor{Light: "#DCE8FF", Dark: "#243A5C"}
	pillTealBG       = lipgloss.AdaptiveColor{Light: "#D9F5EF", Dark: "#1E3F3B"}
	pillAmberBG      = lipgloss.AdaptiveColor{Light: "#FCEFD2", Dark: "#453719"}
	pillGreenBG      = lipgloss.AdaptiveColor{Light: "#DDF5E5", Dark: "#1E3B2A"}
	pillOnSurface    = lipgloss.AdaptiveColor{Light: "#1B2232", Dark: colorInk}
	statusPillBlue   = lipgloss.AdaptiveColor{Light: "#2F6BD8", Dark: "#2D4E8F"}
	statusPillTeal   = lipgloss.AdaptiveColor{Light: "#0E8C7C", Dark: "#216B61"}
	statusPillViolet = lipgloss.AdaptiveColor{Light: "#6450C8", Dark: colorAccentDeep}
)

var accentDeep = lipgloss.AdaptiveColor{Light: "#6450C8", Dark: colorAccentDeep}

func greenStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(green) }
func redStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(red) }
func blueStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(blue) }
func amberStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(amber) }
func pinkStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(pink) }
func cyanStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(cyan) }
func violetStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(violet) }

// Diff lines carry a tinted background so additions and removals stay distinct
// even in a monochrome terminal.
var (
	diffAddedStyle = lipgloss.NewStyle().
			Foreground(green).
			Background(lipgloss.AdaptiveColor{Light: "#E4F7EC", Dark: "#183526"})
	diffRemovedStyle = lipgloss.NewStyle().
				Foreground(red).
				Background(lipgloss.AdaptiveColor{Light: "#FCE4E8", Dark: "#3E2028"})
	diffHunkStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)
)

// Semantic status colours keep process state, merge state and tool activity
// consistent. They are intentionally separate from the accent used for focus.
var (
	statusRunning   = blue
	statusCompleted = green
	statusWaiting   = amber
	statusFailed    = red
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(ink)
	mutedStyle   = lipgloss.NewStyle().Foreground(muted)
	faintStyle   = lipgloss.NewStyle().Foreground(faint)
	accentStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	warningStyle = lipgloss.NewStyle().Foreground(statusWaiting).Bold(true)
	panelStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)

	activeTabStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true).
			Underline(true)
	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(muted)
	selectionRailStyle = lipgloss.NewStyle().
				Foreground(accent).
				Bold(true)
	selectionBodyStyle = lipgloss.NewStyle().
				Foreground(pillOnSurface).
				Background(surface).
				Bold(true)
	selectionHighlightStyle = lipgloss.NewStyle().
				Background(accentDeep).
				Foreground(lipgloss.Color(colorInk))
	keyStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)
)

// pillStyle and cardStyle carry the Crush-like chrome: compact badges and
// bordered blocks in the transcript.
var (
	pillStyle = lipgloss.NewStyle().
			Background(accentDeep).
			Foreground(lipgloss.Color(colorInk)).
			Bold(true).
			Padding(0, 1)
	pillMutedStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#E2E6EF", Dark: "#2A3346"}).
			Foreground(muted).
			Padding(0, 1)
	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1)
)

// pillFor paints a badge with a tinted background. The title colour stays
// readable in both the light and dark palettes.
func pillFor(background lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().
		Background(background).
		Foreground(pillOnSurface).
		Bold(true).
		Padding(0, 1)
}

func modePillStyle(mode string) lipgloss.Style {
	switch mode {
	case "REQUEST", "INSERT":
		return pillFor(statusPillViolet)
	case "DETAILS":
		return pillFor(statusPillTeal)
	default:
		return pillFor(statusPillBlue)
	}
}

func modelPillStyle(class string) lipgloss.Style {
	if class == "SMALL" {
		return pillFor(pillTealBG)
	}
	return pillFor(pillBlueBG)
}

// tabStyle gives each detail tab its own tint so the active view is visible at
// a glance without dropping the underline convention.
func tabStyle(tab detailTab) lipgloss.Style {
	background := pillGreenBG
	switch tab {
	case infoTab:
		background = pillAmberBG
	case activityTab:
		background = pillTealBG
	}
	return lipgloss.NewStyle().
		Foreground(pillOnSurface).
		Background(background).
		Bold(true)
}

// The help overlay is prose plus a two-column shortcut table, so it needs a
// heading, a description and a scrollbar style of its own. They reuse the
// palette above and never set a background colour.
var (
	helpHeadingStyle = lipgloss.NewStyle().
				Foreground(accent).
				Bold(true)
	helpDescStyle = lipgloss.NewStyle().
			Foreground(ink)
	helpThumbStyle = lipgloss.NewStyle().
			Foreground(accent)
	helpTrackStyle = lipgloss.NewStyle().
			Foreground(faint)
)

// ApplyTheme pins the background the adaptive palette resolves against. Auto
// follows the terminal; the mode is returned for display and persistence.
func ApplyTheme(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "dark":
		lipgloss.SetHasDarkBackground(true)
		return "dark"
	case "light":
		lipgloss.SetHasDarkBackground(false)
		return "light"
	}
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stdout))
	return "auto"
}

// wordmark is the product identity as one flat heading. It is deliberately
// single-coloured: a per-rune gradient shifted colour every frame without
// carrying any meaning.
func wordmark() string { return titleStyle.Render("◈ PILHARMONIC") }

// gradientText paints a wordmark through violet, pink and teal without
// changing its visible text or width. ANSI styling is applied per rune, so
// callers can continue to use normal cell-width helpers for layout.
func gradientText(text string) string { return gradientPhase(text, 0) }

// gradientFill paints repeated chrome, such as the header's diagonal filler,
// with the same moving ramp as the wordmark.
func gradientFill(text string, phase int) string { return gradientRunes(text, phase) }

// gradientPhase shifts the wordmark gradient for the running animation. Phase 0
// is the static brand mark.
func gradientPhase(text string, phase int) string { return gradientRunes(text, phase) }

func gradientRunes(text string, phase int) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range runes {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(gradientColor(i, len(runes), phase))).Bold(true).Render(string(r)))
	}
	return b.String()
}

// gradientStops is the brand ramp: violet, pink, teal and blue. More than two
// stops keep the wordmark from reading as a single dim hue.
var gradientStops = [][3]float64{
	{183, 166, 255},
	{255, 158, 216},
	{123, 224, 207},
	{123, 167, 255},
}

// gradientColor interpolates the wordmark through the brand stops, shifting
// with the animation phase.
func gradientColor(i, n, phase int) string {
	t := 0.0
	if span := n + 8; span > 1 {
		t = float64((i+phase)%span) / float64(span-1)
		if t > 1 {
			t = 2 - t
		}
	}
	pos := t * float64(len(gradientStops)-1)
	index := min(int(pos), len(gradientStops)-2)
	local := pos - float64(index)
	start, end := gradientStops[index], gradientStops[index+1]
	return fmt.Sprintf("#%02X%02X%02X",
		int(start[0]+(end[0]-start[0])*local),
		int(start[1]+(end[1]-start[1])*local),
		int(start[2]+(end[2]-start[2])*local),
	)
}
