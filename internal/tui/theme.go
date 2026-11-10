package tui

import (
	"fmt"
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

var (
	ink    = lipgloss.Color(colorInk)
	muted  = lipgloss.Color(colorMuted)
	accent = lipgloss.Color(colorAccent)
	border = lipgloss.Color(colorBorder)
	cyan   = lipgloss.Color(colorCyan)
	blue   = lipgloss.Color(colorBlue)
	green  = lipgloss.Color(colorGreen)
	amber  = lipgloss.Color(colorAmber)
	red    = lipgloss.Color(colorRed)
	faint  = lipgloss.Color(colorFaint)
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
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(ink)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	faintStyle  = lipgloss.NewStyle().Foreground(faint)
	accentStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)

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
				Background(lipgloss.Color(colorAccentDeep)).
				Foreground(lipgloss.Color(colorInk))
	keyStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)
)

// gradientText paints a wordmark from violet to teal without changing its
// visible text or width. ANSI styling is applied per rune, so callers can
// continue to use normal cell-width helpers for layout.
func gradientText(text string) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	start := [3]float64{183, 166, 255}
	end := [3]float64{123, 224, 207}
	var b strings.Builder
	for i, r := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(i) / float64(len(runes)-1)
		}
		color := fmt.Sprintf("#%02X%02X%02X",
			int(start[0]+(end[0]-start[0])*t),
			int(start[1]+(end[1]-start[1])*t),
			int(start[2]+(end[2]-start[2])*t),
		)
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(string(r)))
	}
	return b.String()
}
