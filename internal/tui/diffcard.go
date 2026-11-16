package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// diffCard renders a unified diff as a bordered block with coloured added and
// removed lines. It reports nil for results that are not diffs.
func (m model) diffCard(text string, width int) []string {
	if !looksLikeDiff(text) || width < 12 {
		return nil
	}
	inner := width - 4
	var body []string
	for _, line := range strings.Split(terminalText(text), "\n") {
		rendered := faintStyle.Render(line)
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			rendered = mutedStyle.Render(line)
		case strings.HasPrefix(line, "@@"):
			rendered = accentStyle.Render(line)
		case strings.HasPrefix(line, "+"):
			rendered = greenStyle().Render(line)
		case strings.HasPrefix(line, "-"):
			rendered = redStyle().Render(line)
		}
		body = append(body, strings.Split(ansi.Hardwrap(rendered, inner, true), "\n")...)
	}
	edge := faintStyle.Render("│ ")
	top := faintStyle.Render("╭─") + accentStyle.Render(" diff ") + faintStyle.Render(strings.Repeat("─", max(0, width-8))+"╮")
	out := []string{top}
	for _, line := range body {
		padded := fit(line, inner, 1)
		out = append(out, edge+padded+faintStyle.Render(" │"))
	}
	out = append(out, faintStyle.Render("╰"+strings.Repeat("─", max(0, width-2))+"╯"))
	return out
}

// looksLikeDiff requires at least one changed line and a hunk or file header,
// so prose that happens to start with + or - keeps its normal rendering.
func looksLikeDiff(text string) bool {
	changed, header := false, false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			header = true
		case strings.HasPrefix(line, "diff --git "), strings.HasPrefix(line, "--- "):
			header = true
		case (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) ||
			(strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")):
			changed = true
		}
	}
	return changed && header
}
