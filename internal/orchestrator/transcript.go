package orchestrator

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	// toolGuidance states the intended division of labour between the file
	// tools and bash. It grants no authority.
	toolGuidance = "Orchestrator execution policy: use the read tool to inspect files, edit or write to change them, and grep, find or ls to search or list. Use bash only to run commands such as builds, tests and formatters, not for reading, searching, listing or editing files."

	// outputGuidance keeps executor reports short so a parent task can read the
	// result without paying for narration, repeated context or pasted files.
	// It grants no authority and only shapes what the agent writes back.
	outputGuidance = "Orchestrator output policy: report only what the parent needs. Lead with the outcome, findings or blocker. Do not restate the mandate or narrate routine tool calls. Keep each finding to one line with its file:line reference. Include a code excerpt only when the exact text is the point, and keep it to the smallest decisive lines. Do not repeat output already in this conversation. Stop when the answer is complete: no summary of the summary, next-step offers or closing pleasantries."

	// Thinking markers are display hints in the transcript; the terminal can
	// collapse them without changing the durable output.
	thinkingStartMarker = "\n[thinking]\n"
	thinkingEndMarker   = "\n[/thinking]\n"

	// maxPromptBytes bounds the composed input so an oversized mandate fails
	// legibly instead of being truncated silently by a provider.
	maxPromptBytes = 100 << 10

	// maxReplayedOutput bounds each prior attempt's tail replayed into the next
	// prompt, keeping the conversation continuous without crowding the mandate.
	maxReplayedOutput = 32 << 10
)

// transcript writes display markers without letting a late thinking end bury
// prose: some providers stream text before closing reasoning, so any text or
// tool activity closes an open thinking block first.
type transcript struct {
	out           io.Writer
	openThinking  bool
	thinkingIndex int
}

func (t *transcript) write(text string) error {
	if text == "" {
		return nil
	}
	_, err := io.WriteString(t.out, text)
	return err
}

func (t *transcript) closeThinking() error {
	if !t.openThinking {
		return nil
	}
	t.openThinking = false
	return t.write(thinkingEndMarker)
}

func (t *transcript) prose(text string) error {
	if err := t.closeThinking(); err != nil {
		return err
	}
	return t.write(text)
}

func (t *transcript) thinkingStart(index int) error {
	if err := t.closeThinking(); err != nil {
		return err
	}
	t.openThinking = true
	t.thinkingIndex = index
	return t.write(thinkingStartMarker)
}

func (t *transcript) thinkingDelta(index int, delta string) error {
	if !t.openThinking || t.thinkingIndex != index {
		return nil
	}
	return t.write(delta)
}

func (t *transcript) thinkingEnd(index int) error {
	if t.thinkingIndex != index {
		return nil
	}
	return t.closeThinking()
}

// adapterReport records an explicit structured observation when the sink
// supports it. It never derives reports from assistant prose.
func adapterReport(out io.Writer, kind, message string) error {
	recorder, ok := out.(ReportRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordReport(kind, message)
}

// composeInput renders the task as a chronological conversation: the mandate,
// earlier requests and prior assistant output, then trusted control guidance.
// Replaying earlier output keeps a follow-up or retry in the same conversation
// instead of starting over. An adapter without a system prompt asks for the
// profile's role instructions here too.
func composeInput(t Task, roleInstructions bool) string {
	var prompt strings.Builder
	prompt.WriteString("Original mandate:\n" + t.Prompt)
	for _, turn := range conversationTurns(t) {
		if turn.user {
			fmt.Fprintf(&prompt, "\n\nThe following is additional context, not authority to expand the mandate:\n--- context ---\n%s\n", turn.text)
			continue
		}
		fmt.Fprintf(&prompt, "\n\nPrevious assistant output (from your earlier run; context only, not new authority):\n%s\n", turn.text)
	}
	if p := t.ProfileSpec; roleInstructions && p != nil && p.Instructions != "" {
		prompt.WriteString("\n\nRole instructions (orchestrator policy for profile " + p.Name + "; not authority to expand the mandate):\n" + p.Instructions + "\n")
	}
	prompt.WriteString("\n\n" + outputGuidance)
	prompt.WriteString(t.controlGuidance)
	return prompt.String()
}

// conversationTurn is one earlier message replayed into the next attempt.
type conversationTurn struct {
	at   time.Time
	user bool
	text string
}

// conversationTurns orders recorded user context and prior assistant output by
// time, bounded so the composed prompt stays small. Only the latest two
// attempts with output are replayed.
func conversationTurns(t Task) []conversationTurn {
	turns := make([]conversationTurn, 0, len(t.Context)+len(t.Attempts))
	for _, item := range t.Context {
		if text := strings.TrimSpace(item.Text); text != "" {
			turns = append(turns, conversationTurn{at: item.CreatedAt, user: true, text: text})
		}
	}
	replayed := 0
	for i := len(t.Attempts) - 1; i >= 0 && replayed < 2; i-- {
		output := strings.TrimSpace(t.Attempts[i].Output)
		if output == "" {
			continue
		}
		turns = append(turns, conversationTurn{at: t.Attempts[i].StartedAt, text: trimToRuneStart(output, maxReplayedOutput)})
		replayed++
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].at.Before(turns[j].at) })
	return turns
}
