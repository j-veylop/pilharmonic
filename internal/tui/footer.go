package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func hintsWidth(hints []string) int {
	width := 0
	for i, hint := range hints {
		if i > 0 {
			width += 5 // rendered separator "  ·  "
		}
		width += lipgloss.Width(hint)
	}
	return width
}

// boardFooter keeps selected-task actions first, then the operator essentials.
// Optional hints are dropped before the footer grows past two lines.
func (m model) boardFooter(w int) string {
	var hints []string
	add := func(hint string, optional bool) {
		candidate := append(append([]string{}, hints...), hint)
		if optional && hintsWidth(candidate) > 2*w {
			return
		}
		hints = candidate
	}
	for _, hint := range m.selectedTaskHints() {
		add(hint, false)
	}
	if m.attentionCount() > 0 {
		add("! action", true)
	}
	add("? help", true)
	add("q quit", false)
	add("/ request", true)
	add("n new", true)
	add("L login", true)
	add("m model", true)
	if w < 76 {
		add("j/k tasks", true)
		add("l details", true)
	}
	return strings.Join(hints, " · ")
}

func (m model) footer(w int) string {
	footerText := m.boardFooter(w)
	switch {
	case m.router:
		if m.routerFocus != routerFocusComposer {
			footerText = "j/k move · enter open · tab next · esc tasks"
		} else if _, ok := m.routerMentionQuery(); ok {
			footerText = "tab complete · enter submit · esc tasks · ctrl+c quit"
		} else {
			footerText = "enter submit · tab lists · esc tasks · ctrl+c quit"
		}
	case m.input.kind != "":
		footerText = "enter submit · ctrl+j/alt+enter newline · esc cancel"
	case m.activity:
		if m.activityExpanded {
			footerText = "esc collapse · t output · f filter · ? help · q quit"
		} else {
			footerText = "enter expand · t output · f filter · ? help · q quit"
		}
	case m.detailFocus || m.fullscreen:
		footerText = "j/k scroll · T thinking · h tasks · ? help · q quit"
	}
	var hints []string
	for _, line := range strings.Split(footerText, "\n") {
		var parts []string
		for _, hint := range strings.Split(line, " · ") {
			key, desc, _ := strings.Cut(hint, " ")
			parts = append(parts, keyStyle.Render(key)+mutedStyle.Render(" "+desc))
		}
		hints = append(hints, strings.Join(parts, faintStyle.Render("  ·  ")))
	}
	footer := strings.Join(hints, "\n")
	if m.notice != "" {
		footer = accentStyle.Render("› "+strings.ReplaceAll(terminalText(m.notice), "\n", " ")) + "\n" + footer
	}
	if m.cleanup.kind == deleteTask {
		footer = accentStyle.Render("DELETE TASK + HISTORY  › "+strings.ReplaceAll(terminalText(m.cleanup.title), "\n", " ")) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel") + "\n" + mutedStyle.Render("Removes its worktree; source workspace is untouched.")
	}
	if m.cleanup.kind == deleteCompleted {
		footer = accentStyle.Render(fmt.Sprintf("DELETE %d COMPLETED TASK(S) + HISTORY", len(m.cleanup.ids))) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel") + "\n" + mutedStyle.Render("Tasks with unfinished children are kept; worktrees are removed.")
	}
	if m.cleanup.kind == discardTask {
		footer = accentStyle.Render("DISCARD RETAINED WORKTREE  › "+strings.ReplaceAll(terminalText(m.cleanup.title), "\n", " ")) + "\n" + mutedStyle.Render("y discard  ·  n/esc cancel") + "\n" + mutedStyle.Render("Refuses uncommitted or unmerged work; task history is kept.")
	}
	if m.cleanup.kind == discardMerged {
		footer = accentStyle.Render(fmt.Sprintf("DISCARD %d MERGED WORKTREE(S)", len(m.cleanup.ids))) + "\n" + mutedStyle.Render("y discard  ·  n/esc cancel") + "\n" + mutedStyle.Render("Skips uncommitted or unmerged work; task history is kept.")
	}
	if m.agentPicker.target != "" {
		footer = mutedStyle.Render("↑↓/j/k choose agent · enter select · esc cancel")
	}
	if m.profilePicker.target != "" {
		footer = mutedStyle.Render("↑↓/j/k choose profile · enter select · esc cancel")
	}
	return ansi.Hardwrap(footer, w, true)
}
