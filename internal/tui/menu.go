package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// menuRow is one selectable line in a modal list menu. tag is pre-styled and
// right-aligned, or empty for a plain row.
type menuRow struct {
	label string
	tag   string
}

// menuView is the single renderer for the login, model and agent-class menus.
// It draws a titled modal over a faint background, windows the rows around the
// selection, right-aligns each row's tag, and pads the body so the footer hint
// stays on the last row.
func menuView(background string, w, h int, title, subtitle string, body []string, rows []menuRow, selected int, empty, footer string, mw, mh int) string {
	inner := mw - 4
	if inner < 12 || mh < 6 {
		return fit(background, w, h)
	}
	selected = max(0, min(len(rows)-1, selected))
	content := []string{
		accentStyle.Render(title) + mutedStyle.Render("  "+subtitle),
		"",
	}
	content = append(content, body...)
	if len(rows) == 0 && empty != "" {
		content = append(content, mutedStyle.Render(empty))
	}
	start, end := windowRows(selected, len(rows), mh-5)
	for i := start; i < end; i++ {
		row := rows[i]
		marker, name := "  ", mutedStyle.Render(row.label)
		if i == selected {
			marker, name = accentStyle.Render("› "), titleStyle.Render(row.label)
		}
		if row.tag == "" {
			content = append(content, marker+name)
			continue
		}
		plain := 2 + lipgloss.Width(row.label) + lipgloss.Width(ansi.Strip(row.tag))
		if plain >= inner {
			content = append(content, marker+name)
			continue
		}
		content = append(content, marker+name+strings.Repeat(" ", inner-plain)+row.tag)
	}
	for len(content) < mh-3 {
		content = append(content, "")
	}
	content = append(content, accentStyle.Render(footer))
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	return overlay(background, modal, w, h, mw, mh)
}

// moveMenuIndex applies the shared j/k/up/down movement to a menu selection and
// reports whether the key was consumed.
func moveMenuIndex(key string, index *int, count int) bool {
	switch key {
	case "up", "k":
		*index = max(0, min(count-1, *index-1))
		return true
	case "down", "j":
		*index = max(0, min(count-1, *index+1))
		return true
	}
	return false
}
