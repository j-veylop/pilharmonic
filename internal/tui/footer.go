package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// boardFooter keeps selected-task actions first, then the operator essentials.
// Optional hints are dropped before the footer grows past two lines.
func (m model) boardFooter(w int) string {
	var hints []string
	add := func(hint string, optional bool) {
		candidate := append(append([]string{}, hints...), hint)
		if optional && hintRows(strings.Join(candidate, " · "), w) > 2 {
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

// hintRows counts the rows text occupies once the separators are rendered and
// the footer is hard-wrapped at width. A hint is only worth adding when it costs
// no extra row.
func hintRows(text string, w int) int {
	return len(strings.Split(ansi.Hardwrap(strings.ReplaceAll(text, " · ", "  ·  "), w, true), "\n"))
}

// withHelpHint advertises the help overlay where there is room for it. F1 is
// the one help key that works inside a text field, so the entry surfaces
// advertise that key rather than ?.
func withHelpHint(text string, w int) string {
	withHelp := text + " · f1 help"
	if hintRows(withHelp, w) <= hintRows(text, w) {
		return withHelp
	}
	return text
}

func (m model) footer(w int) string {
	footerText := m.boardFooter(w)
	switch {
	case m.router.open:
		if m.router.focus != routerFocusComposer {
			footerText = withHelpHint("j/k move · enter open · tab next · esc tasks", w)
		} else if _, ok := m.routerMentionQuery(); ok {
			footerText = withHelpHint("tab complete · enter submit · esc tasks · ctrl+c quit", w)
		} else {
			footerText = withHelpHint("enter submit · tab lists · esc tasks · ctrl+c quit", w)
		}
	case m.input.kind != "":
		footerText = withHelpHint("enter submit · ctrl+j/alt+enter newline · esc cancel", w)
	case m.activity.open:
		if m.activity.expanded {
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
	if m.notice.text != "" {
		footer = accentStyle.Render("› "+singleLine(m.notice.text)) + "\n" + footer
	}
	if m.cleanup.kind == deleteTask {
		footer = accentStyle.Render("DELETE TASK + HISTORY  › "+singleLine(m.cleanup.title)) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel  ·  f1 help") + "\n" + mutedStyle.Render("Removes its worktree; source workspace is untouched.")
	}
	if m.cleanup.kind == deleteCompleted {
		footer = accentStyle.Render(fmt.Sprintf("DELETE %d COMPLETED TASK(S) + HISTORY", len(m.cleanup.ids))) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel  ·  f1 help") + "\n" + mutedStyle.Render("Tasks with unfinished children are kept; worktrees are removed.")
	}
	if m.cleanup.kind == discardTask {
		footer = accentStyle.Render("DISCARD RETAINED WORKTREE  › "+singleLine(m.cleanup.title)) + "\n" + mutedStyle.Render("y discard  ·  n/esc cancel  ·  f1 help") + "\n" + mutedStyle.Render("Refuses uncommitted or unmerged work; task history is kept.")
	}
	if m.cleanup.kind == discardMerged {
		footer = accentStyle.Render(fmt.Sprintf("DISCARD %d MERGED WORKTREE(S)", len(m.cleanup.ids))) + "\n" + mutedStyle.Render("y discard  ·  n/esc cancel  ·  f1 help") + "\n" + mutedStyle.Render("Skips uncommitted or unmerged work; task history is kept.")
	}
	if m.agentPicker.target != "" {
		footer = mutedStyle.Render("↑↓/j/k choose agent · enter select · f1 help · esc cancel")
	}
	if m.profilePicker.target != "" {
		footer = mutedStyle.Render("↑↓/j/k choose profile · enter select · f1 help · esc cancel")
	}
	return ansi.Hardwrap(footer, w, true)
}
