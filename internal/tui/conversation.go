package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// chatTurn is one message in the task's conversation timeline.
type chatTurn struct {
	at      time.Time
	user    bool
	text    string
	attempt orchestrator.Attempt
}

// conversationContent renders the mandate and every recorded follow-up and
// attempt output as one chronological chat, styled like Crush: user turns carry
// a left rail, assistant turns sit indented under a muted attempt line, and a
// live turn ends with the animated spinner.
func (m model) conversationContent(t orchestrator.Task, width int) []string {
	var lines []string
	add := func(rows ...string) {
		for _, row := range rows {
			lines = append(lines, strings.Split(ansi.Hardwrap(row, width, true), "\n")...)
		}
	}
	lines = append(lines, chatUserBlock(t.Prompt, width)...)
	turns := taskTurns(t)
	if len(turns) == 0 && strings.TrimSpace(t.Output) != "" {
		turns = append(turns, chatTurn{at: t.UpdatedAt, text: t.Output})
	}
	for _, turn := range turns {
		add("")
		if turn.user {
			lines = append(lines, chatUserBlock(turn.text, width)...)
			continue
		}
		if meta := attemptMeta(turn.attempt); meta != "" {
			add(mutedStyle.Render(meta))
		}
		lines = append(lines, chatAssistantBlock(m.renderedOutputFor(turn.text, width-2, m.runningGlyph(), m.thinking), width)...)
		if turn.attempt.Error != "" {
			add(warningStyle.Render("error  ") + terminalText(turn.attempt.Error))
		}
	}
	if t.Status == orchestrator.Running && m.chatAnim != nil {
		add("")
		lines = append(lines, chatAssistantBlock([]string{m.chatAnim.Render()}, width)...)
	}
	return lines
}

// chatUserBlock is the Crush user message shape: an accent rail and one cell of
// padding, wrapping inside the remaining width.
func chatUserBlock(text string, width int) []string {
	body := strings.Split(terminalText(text), "\n")
	var lines []string
	for _, row := range body {
		wrapped := strings.Split(ansi.Hardwrap(row, max(1, width-2), true), "\n")
		for _, line := range wrapped {
			lines = append(lines, gradientFill("▌", len(lines))+" "+line)
		}
	}
	return lines
}

// chatAssistantBlock is Crush's assistant message shape: indented two cells,
// with no rail unless focused.
func chatAssistantBlock(rows []string, width int) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		for _, line := range strings.Split(ansi.Hardwrap(row, max(1, width-2), true), "\n") {
			out = append(out, "  "+line)
		}
	}
	return out
}

// attemptMeta is the muted line above an assistant turn.
func attemptMeta(attempt orchestrator.Attempt) string {
	if attempt.ID == "" && attempt.StartedAt.IsZero() {
		return ""
	}
	meta := "attempt " + attempt.ID
	if attempt.Status != "" {
		meta += "  ·  " + statusLabel(attempt.Status)
	}
	if !attempt.StartedAt.IsZero() {
		meta += "  ·  " + attempt.StartedAt.Local().Format("15:04")
	}
	if attempt.OutputTruncated {
		meta += "  ·  output trimmed"
	}
	return terminalText(strings.TrimSpace(meta))
}

// taskTurns interleaves recorded context and attempt output by time.
func taskTurns(t orchestrator.Task) []chatTurn {
	turns := make([]chatTurn, 0, len(t.Context)+len(t.Attempts))
	for _, item := range t.Context {
		if text := strings.TrimSpace(item.Text); text != "" {
			turns = append(turns, chatTurn{at: item.CreatedAt, user: true, text: text})
		}
	}
	for _, attempt := range t.Attempts {
		if strings.TrimSpace(attempt.Output) == "" && attempt.Error == "" {
			continue
		}
		turns = append(turns, chatTurn{at: attempt.StartedAt, text: attempt.Output, attempt: attempt})
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].at.Before(turns[j].at) })
	return turns
}
