package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// overlay centres modal over a faint, unstyled copy of background in a w×h
// frame. Stripping the background's own styles keeps the modal the only thing
// that reads as active. The frame is always exactly w×h cells.
func overlay(background, modal string, w, h, mw, mh int) string {
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, max(0, h))
	for row := range result {
		line := ""
		if row < len(bg) {
			line = ansi.Strip(bg[row])
		}
		line = lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
		if row >= y && row < y+len(box) {
			result[row] = mutedStyle.Faint(true).Render(ansi.Cut(line, 0, x)) + box[row-y] + mutedStyle.Faint(true).Render(ansi.Cut(line, x+mw, w))
		} else {
			result[row] = mutedStyle.Faint(true).Render(line)
		}
	}
	return fit(strings.Join(result, "\n"), w, h)
}

// modalView draws content in a focused frame over background, or just the
// background when the terminal is too small for the frame.
func modalView(background string, w, h, mw, mh int, content []string) string {
	if content == nil {
		return fit(background, w, h)
	}
	return overlay(background, focusedPanel(strings.Join(content, "\n"), mw, mh, true), w, h, mw, mh)
}
