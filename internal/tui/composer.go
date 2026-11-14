package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pilharmonic/internal/orchestrator"
)

type inputKind string

const (
	newTaskInput      inputKind = "NEW TASK"
	relatedTaskInput  inputKind = "RELATED TASK"
	subagentInput     inputKind = "SUBAGENT TASK"
	threadInput       inputKind = "THREAD CONTEXT"
	followUpInput     inputKind = "FOLLOW UP"
	reportInput       inputKind = "REPORT"
	rejectReviewInput inputKind = "REJECT REVIEW"
)

// composerMaxLines bounds the draft rows so the box never crowds out the
// panels above it.
const composerMaxLines = 6

const composerPrompt = "› "

// composer is a task-scoped input. target is captured when it opens, because
// task ordering can change while the operator types.
type composer struct {
	editor
	kind         inputKind
	target       string
	reviewCommit string
}

var composerPlaceholders = map[inputKind]string{
	newTaskInput:      "Describe the task… type @ to choose an executor",
	relatedTaskInput:  "Describe the related task…",
	subagentInput:     "Enter a self-contained mandate for the subagent…",
	threadInput:       "Context for thread subscribers…",
	followUpInput:     "Follow-up context; the mandate is unchanged…",
	reportInput:       "kind and message, e.g. blocked need database access…",
	rejectReviewInput: "Reason for rejection; the worktree is kept…",
}

// composerOpen reports whether View docks the input box shared by the request
// form and every composer.
func (m model) composerOpen() bool { return m.router.open || m.input.kind != "" }

// composerFocused reports whether keys reach the draft. Tab in the request
// form moves focus to a list, which dims the box.
func (m model) composerFocused() bool { return !m.router.open || m.router.focus == routerFocusComposer }

func (m *model) composerEditor() *editor {
	if m.router.open {
		return &m.router.editor
	}
	return &m.input.editor
}

// composerBoxWidth spans the terminal so the box lines up with the panels.
func (m model) composerBoxWidth() int {
	w, _ := m.size()
	return max(4, w)
}

// composerInnerWidth leaves four cells for the rounded border and padding.
func (m model) composerInnerWidth() int { return max(1, m.composerBoxWidth()-4) }

// composerWidth wraps the draft, reserving a cell for the cursor at line end.
func (m model) composerWidth() int {
	return max(1, m.composerInnerWidth()-lipgloss.Width(composerPrompt)-1)
}

func (m model) composerHeight() int { return max(1, m.layout().composerDraft) }

// composerDraftRows grows the box with the draft, keeping two rows when it can
// and always leaving the header, the reserved rows, the border and a three-row
// body.
func (m model) composerDraftRows(reserved int) int {
	_, height := m.size()
	limit := min(composerMaxLines, max(1, height-1-reserved-2-3))
	needed := 1
	if e := m.composerEditor(); e.text != "" && !(m.router.open && m.login.provider != "") {
		needed = len(wrapEditor(e.text, m.composerWidth()))
	}
	return max(min(2, limit), min(limit, needed))
}

// composerExtraRows counts the hint rows rendered under the box.
func (m model) composerExtraRows() int {
	if !m.router.open {
		return 0
	}
	return len(m.routerMentionHintLines(m.composerBoxWidth()))
}

// composerTitle names the box on its top border, with an optional note on the
// right such as the authorization step that records its decision.
func (m model) composerTitle() (title, note string) {
	switch {
	case m.router.open && m.login.provider != "":
		return "API KEY", terminalText(m.login.provider)
	case m.router.open:
		return "NEW REQUEST", "authorization " + orchestrator.LocalPolicyVersion
	case m.input.kind == rejectReviewInput:
		return string(m.input.kind), "keeps worktree · F requeues"
	}
	return string(m.input.kind), ""
}

// composerBoxLines renders the draft with the prompt on the first row and a
// hanging indent on wrapped rows.
func (m model) composerBoxLines(rows int) []string {
	if m.router.open && m.login.provider != "" {
		return m.loginComposerLines(m.composerInnerWidth())
	}
	focused := m.composerFocused()
	prefix := accentStyle.Render(composerPrompt)
	if !focused {
		prefix = mutedStyle.Render(composerPrompt)
	}
	e := m.composerEditor()
	var visible []string
	if e.text == "" {
		placeholder := "Describe the work… type @ to choose an executor"
		if !m.router.open {
			placeholder = composerPlaceholders[m.input.kind]
		}
		cursor := ""
		if focused {
			cursor = "▏"
		}
		visible = []string{fit(mutedStyle.Render(placeholder), m.composerWidth(), 1) + cursor}
	} else {
		visible = e.viewLinesCursor(m.composerWidth(), rows, focused)
	}
	for i := range visible {
		if i == 0 {
			visible[i] = prefix + visible[i]
		} else {
			visible[i] = strings.Repeat(" ", lipgloss.Width(composerPrompt)) + visible[i]
		}
	}
	return visible
}

// composerBlock is the docked box, labelled on its top border and brightened
// while focused, plus the request form's executor hints.
func (m model) composerBlock(rows int) []string {
	title, note := m.composerTitle()
	color := border
	if m.composerFocused() {
		color = accent
	}
	width := m.composerBoxWidth()
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1).
		Width(width - 2).Height(rows).MaxWidth(width).MaxHeight(rows + 2)
	content := fit(strings.Join(m.composerBoxLines(rows), "\n"), m.composerInnerWidth(), rows)
	box := strings.Split(style.Render(content), "\n")
	box[0] = composerTopBorder(title, note, width, color, m.composerFocused())
	if m.router.open {
		box = append(box, m.routerMentionHintLines(width)...)
	}
	return box
}

// composerTopBorder writes the box edge with its label, keeping the note on the
// right when the terminal is wide enough for it.
func composerTopBorder(title, note string, width int, color lipgloss.Color, focused bool) string {
	edge := lipgloss.NewStyle().Foreground(color)
	label := " " + title + " "
	labelWidth := lipgloss.Width(label)
	if labelWidth+4 > width {
		return edge.Render("╭" + strings.Repeat("─", max(0, width-2)) + "╮")
	}
	if note != "" {
		note = " " + note + " "
	}
	fill := width - 4 - labelWidth - lipgloss.Width(note)
	if fill < 3 {
		note, fill = "", width-4-labelWidth
	}
	labelStyle := edge
	if focused {
		labelStyle = accentStyle
	}
	return edge.Render("╭─") + labelStyle.Render(label) + edge.Render(strings.Repeat("─", fill)) + faintStyle.Render(note) + edge.Render("─╮")
}

func (m model) composerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc":
		m.input = composer{}
	case "enter":
		prompt := strings.TrimSpace(m.input.text)
		if prompt == "" {
			m.notice.text = "Enter text first."
			return m, nil
		}
		return m, func() tea.Msg { return m.submitInput(prompt) }
	default:
		m.input.handleKey(msg, m.composerWidth(), m.composerHeight())
	}
	return m, nil
}

func (m model) submitInput(prompt string) composerResultMsg {
	id, s := m.input.target, m.service
	done := func(notice string, err error) composerResultMsg { return composerResultMsg(resultFor(notice, err)) }
	switch m.input.kind {
	case rejectReviewInput:
		return done("Review rejected; worktree retained; press F to follow up.", s.RejectReview(id, m.input.reviewCommit, prompt))
	case reportInput:
		kind, message, _ := strings.Cut(prompt, " ")
		return done("Report recorded.", s.RecordReport(id, kind, message))
	case threadInput:
		return done("Context recorded; delivery status is in history.", s.AddManualThreadContext(id, prompt))
	case followUpInput:
		requeued, err := s.FollowUp(id, prompt)
		if requeued {
			return composerResultMsg{notice: "Follow-up recorded; requeued. Press r to start.", err: err}
		}
		return done("Follow-up recorded; delivery status is in history.", err)
	case subagentInput:
		task, err := s.SpawnSubagent(id, "", prompt, "")
		return done("Subagent started: "+task.ID, err)
	}
	title := prompt
	if _, request, explicit := orchestrator.SplitAgentMention(prompt); explicit {
		title = request
	}
	if runes := []rune(title); len(runes) > 60 {
		title = string(runes[:57]) + "..."
	}
	var task orchestrator.Task
	var err error
	if m.input.kind == relatedTaskInput {
		task, err = s.CreateRelatedTask(id, title, prompt, m.workspace)
	} else {
		task, err = s.CreateTask(title, prompt, m.workspace)
	}
	return done("Task queued: "+task.ID, err)
}
