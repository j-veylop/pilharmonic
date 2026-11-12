package tui

import (
	"fmt"
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

// composerMaxLines bounds the draft rows so the input box never crowds out the
// panels above it on a short terminal.
const composerMaxLines = 6

// composerPrompt is the marker that starts the draft row inside the box.
const composerPrompt = "› "

// The target is captured when the composer opens. Task ordering can change
// while the operator is typing.
type composer struct {
	kind         inputKind
	target       string
	reviewCommit string
	text         string
	cursor       int
	scroll       int
}

func (c composer) editorState() editor {
	return editor{text: c.text, cursor: c.cursor, scroll: c.scroll}
}

func (c *composer) setEditorState(e editor) {
	c.text, c.cursor, c.scroll = e.text, e.cursor, e.scroll
}

// composerOpen reports whether View docks an input box at the bottom of the
// screen: the request form and every composer type share it.
func (m model) composerOpen() bool {
	return m.router || m.input.kind != ""
}

// composerFocused reports whether keys reach the draft. The request form hands
// focus to its action or recent list with Tab, which dims the box.
func (m model) composerFocused() bool {
	if m.router {
		return m.routerFocus == routerFocusComposer
	}
	return true
}

// composerEditor returns the draft the open box edits.
func (m model) composerEditor() editor {
	if m.router {
		return m.routerEditor()
	}
	return m.input.editorState()
}

func (m *model) setComposerEditor(e editor) {
	if m.router {
		m.setRouterEditor(e)
		return
	}
	m.input.setEditorState(e)
}

// composerBoxWidth is the outer width of the docked box: the full terminal
// width, so the input lines up with the panels above it.
func (m model) composerBoxWidth() int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	return max(4, width)
}

// composerInnerWidth is the box content width. The rounded border and the
// horizontal padding take four cells.
func (m model) composerInnerWidth() int {
	return max(1, m.composerBoxWidth()-4)
}

// composerWidth is the draft width used to wrap and move the cursor. One cell
// is reserved for the block cursor drawn at the end of a full line.
func (m model) composerWidth() int {
	return max(1, m.composerInnerWidth()-lipgloss.Width(composerPrompt)-1)
}

// composerHeight is how many draft rows the box shows.
func (m model) composerHeight() int {
	return max(1, m.layout().composerDraft)
}

// composerDraftRows picks the draft rows for a terminal whose footer and hint
// rows are already reserved. The box grows with the draft, keeps two rows when
// it can and always leaves a usable body above it.
func (m model) composerDraftRows(reserved int) int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	// The header, the reserved rows, the box border and a three-row body.
	available := height - 1 - reserved - 2 - 3
	limit := min(composerMaxLines, max(1, available))
	return max(min(2, limit), min(limit, m.composerNeededRows()))
}

// composerNeededRows is how many rows the draft occupies once wrapped.
func (m model) composerNeededRows() int {
	if m.router && m.loginProvider != "" {
		return 1
	}
	e := m.composerEditor()
	if e.text == "" {
		return 1
	}
	return max(1, len(wrapEditor(e.text, m.composerWidth())))
}

// composerExtraRows counts the hint rows rendered under the box.
func (m model) composerExtraRows() int {
	if !m.router {
		return 0
	}
	return len(m.routerMentionHintLines(m.composerBoxWidth()))
}

// composerTitle names the box on its top border and, when it fits, shows the
// policy that decides acceptance on the right.
func (m model) composerTitle() (title, note string) {
	if m.router {
		if m.loginProvider != "" {
			return "API KEY", terminalText(m.loginProvider)
		}
		return "NEW REQUEST", "policy " + orchestrator.LocalPolicyVersion
	}
	if m.input.kind == rejectReviewInput {
		return string(m.input.kind), "keeps worktree · F requeues"
	}
	return string(m.input.kind), ""
}

// composerPlaceholder describes the expected draft while the box is empty.
func (m model) composerPlaceholder() string {
	if m.router {
		return "Describe the work… type @ to choose an executor"
	}
	switch m.input.kind {
	case newTaskInput:
		return "Describe the task… type @ to choose an executor"
	case relatedTaskInput:
		return "Describe the related task…"
	case subagentInput:
		return "Enter a self-contained mandate for the subagent…"
	case threadInput:
		return "Context for thread subscribers…"
	case followUpInput:
		return "Follow-up context; the mandate is unchanged…"
	case reportInput:
		return "kind and message, e.g. blocked need database access…"
	case rejectReviewInput:
		return "Reason for rejection; the worktree is kept…"
	}
	return "Enter text…"
}

// composerBoxLines renders the draft inside the border: the prompt marker on
// the first row and a hanging indent on wrapped rows.
func (m model) composerBoxLines(width, rows int) []string {
	inner := max(1, width-4)
	contentWidth := max(1, inner-lipgloss.Width(composerPrompt)-1)
	prefix := accentStyle.Render(composerPrompt)
	if !m.composerFocused() {
		prefix = mutedStyle.Render(composerPrompt)
	}
	pad := strings.Repeat(" ", lipgloss.Width(composerPrompt))
	if m.router && m.loginProvider != "" {
		return m.loginComposerLines(inner)
	}
	e := m.composerEditor()
	focused := m.composerFocused()
	cursor := ""
	if focused {
		cursor = "▏"
	}
	var visible []string
	if e.text == "" {
		visible = []string{fit(mutedStyle.Render(m.composerPlaceholder()), contentWidth, 1) + cursor}
	} else {
		visible = e.viewLinesCursor(contentWidth, rows, focused)
	}
	lines := make([]string, 0, len(visible))
	for i, line := range visible {
		if i == 0 {
			lines = append(lines, prefix+line)
		} else {
			lines = append(lines, pad+line)
		}
	}
	return lines
}

// composerBox draws the docked input box. Its label sits on the top border and
// the border brightens while the draft has focus.
func (m model) composerBox(width, rows int) []string {
	title, note := m.composerTitle()
	color := border
	if m.composerFocused() {
		color = accent
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color).
		Padding(0, 1).
		Width(width - 2).
		Height(rows).
		MaxWidth(width).
		MaxHeight(rows + 2)
	content := fit(strings.Join(m.composerBoxLines(width, rows), "\n"), width-4, rows)
	box := strings.Split(style.Render(content), "\n")
	if len(box) == 0 {
		return nil
	}
	box[0] = composerTopBorder(title, note, width, color, m.composerFocused())
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
	noteLabel := ""
	if note != "" {
		noteLabel = " " + note + " "
	}
	fill := width - 4 - labelWidth - lipgloss.Width(noteLabel)
	if fill < 3 {
		noteLabel, fill = "", width-4-labelWidth
	}
	labelStyle := lipgloss.NewStyle().Foreground(color)
	if focused {
		labelStyle = accentStyle
	}
	row := edge.Render("╭─") + labelStyle.Render(label)
	if noteLabel != "" {
		row += edge.Render(strings.Repeat("─", fill)) + faintStyle.Render(noteLabel) + edge.Render("─╮")
		return row
	}
	return row + edge.Render(strings.Repeat("─", fill)+"─╮")
}

// composerBlock is the whole docked block: the box plus the request form's
// executor hints.
func (m model) composerBlock(width, rows int) []string {
	lines := m.composerBox(width, rows)
	if m.router {
		lines = append(lines, m.routerMentionHintLines(width)...)
	}
	return lines
}

func (m model) composerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.input = composer{}
	case "enter":
		prompt := strings.TrimSpace(m.input.text)
		if prompt == "" {
			m.notice = "Enter text first."
			return m, nil
		}
		return m, m.submitInputCmd(prompt)
	default:
		e := m.composerEditor()
		e.handleKey(msg, m.composerWidth(), m.composerHeight())
		m.setComposerEditor(e)
	}
	return m, nil
}

func (m model) submitInputCmd(prompt string) tea.Cmd {
	return func() tea.Msg {
		notice, err := m.submitInput(prompt)
		return composerResultMsg{notice: notice, err: err}
	}
}

func (m model) submitInput(prompt string) (string, error) {
	id := m.input.target
	switch m.input.kind {
	case rejectReviewInput:
		if err := m.service.RejectReview(id, m.input.reviewCommit, prompt); err != nil {
			return "", err
		}
		return "Review rejected; worktree retained; press F to follow up.", nil
	case reportInput:
		kind, message, _ := strings.Cut(prompt, " ")
		if err := m.service.RecordReport(id, kind, message); err != nil {
			return "", err
		}
		return "Report recorded.", nil
	case threadInput:
		if err := m.service.AddManualThreadContext(id, prompt); err != nil {
			return "", err
		}
		return "Context recorded; delivery status is in history.", nil
	case followUpInput:
		requeued, err := m.service.FollowUp(id, prompt)
		if requeued {
			return "Follow-up recorded; requeued. Press r to start.", err
		}
		if err != nil {
			return "", err
		}
		return "Follow-up recorded; delivery status is in history.", nil
	case subagentInput:
		task, err := m.service.SpawnSubagent(id, "", prompt, "")
		if err != nil {
			return "", err
		}
		return "Subagent started: " + task.ID, nil
	case newTaskInput, relatedTaskInput:
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
			task, err = m.service.CreateRelatedTask(id, title, prompt, m.workspace)
		} else {
			task, err = m.service.CreateTask(title, prompt, m.workspace)
		}
		if err != nil {
			return "", err
		}
		return "Task queued: " + task.ID, nil
	default:
		return "", fmt.Errorf("unknown input kind %q", m.input.kind)
	}
}
