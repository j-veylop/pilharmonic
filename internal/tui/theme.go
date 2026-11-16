package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The interface uses one small, dark palette. Keep colour decisions here so
// status, selection and transcript styling stay in the same visual language.
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
	colorBorder     = "#35425D"
)

// The palette adapts to the terminal background: the light variants read as
// the same hierarchy on a white surface without changing any call site.
var (
	ink    = lipgloss.AdaptiveColor{Light: "#1B2232", Dark: colorInk}
	muted  = lipgloss.AdaptiveColor{Light: "#5B667C", Dark: colorMuted}
	accent = lipgloss.AdaptiveColor{Light: "#6450C8", Dark: colorAccent}
	border = lipgloss.AdaptiveColor{Light: "#B9C2D4", Dark: colorBorder}
	cyan   = lipgloss.AdaptiveColor{Light: "#0E8C7C", Dark: colorCyan}
	blue   = lipgloss.AdaptiveColor{Light: "#2F6BD8", Dark: colorBlue}
	green  = lipgloss.AdaptiveColor{Light: "#2E8A57", Dark: colorGreen}
	amber  = lipgloss.AdaptiveColor{Light: "#A66A0A", Dark: colorAmber}
	red    = lipgloss.AdaptiveColor{Light: "#C24055", Dark: colorRed}
	faint  = lipgloss.AdaptiveColor{Light: "#8A94A8", Dark: colorFaint}
)

var accentDeep = lipgloss.AdaptiveColor{Light: "#6450C8", Dark: colorAccentDeep}

func greenStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(green) }
func redStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(red) }

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
				Foreground(ink).
				Bold(true)
	selectionHighlightStyle = lipgloss.NewStyle().
				Background(accentDeep).
				Foreground(lipgloss.Color(colorInk))
	keyStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)
)

// pillStyle and cardStyle carry the Crush-like chrome: compact badges in the
// status bar and bordered blocks in the transcript.
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

// The help overlay is prose plus a two-column shortcut table, so it needs a
// heading, a description and a scrollbar style of its own. They reuse the
// palette above and never set a background colour.
var (
	helpHeadingStyle = lipgloss.NewStyle().
				Foreground(cyan).
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

// gradientText paints a wordmark from violet to teal without changing its
// visible text or width. ANSI styling is applied per rune, so callers can
// continue to use normal cell-width helpers for layout.
func gradientText(text string) string { return gradientPhase(text, 0) }

// gradientPhase shifts the wordmark gradient for the running animation. Phase 0
// is the static brand mark.
func gradientPhase(text string, phase int) string {
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

// gradientColor interpolates the wordmark from violet to teal, shifting with
// the animation phase while staying inside the two brand hues.
func gradientColor(i, n, phase int) string {
	start := [3]float64{183, 166, 255}
	end := [3]float64{123, 224, 207}
	span := n + 8
	t := 0.0
	if span > 1 {
		t = float64((i+phase)%span) / float64(span-1)
		if t > 1 {
			t = 2 - t
		}
	}
	return fmt.Sprintf("#%02X%02X%02X",
		int(start[0]+(end[0]-start[0])*t),
		int(start[1]+(end[1]-start[1])*t),
		int(start[2]+(end[2]-start[2])*t),
	)
}
