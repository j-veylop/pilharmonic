package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func panel(content string, width, height int) string {
	return panelWith(content, width, height, false)
}

// surfacePanel draws a borderless content surface with the two-cell margin
// Crush uses for its main chat and sidebar panes.
func surfacePanel(content string, width, height int) string {
	if width < 4 || height < 1 {
		return fit(content, width, height)
	}
	style := lipgloss.NewStyle().
		Width(width).MaxWidth(width).
		Height(height).MaxHeight(height).
		Padding(0, 2)
	return style.Render(fit(content, width-4, height))
}

func focusedPanel(content string, width, height int, focused bool) string {
	return panelWith(content, width, height, focused)
}

func panelWith(content string, width, height int, focused bool) string {
	if width < 4 || height < 2 {
		return fit(content, width, height)
	}
	style := panelStyle
	if focused {
		style = panelStyle.BorderForeground(accent)
	}
	return style.Width(width - 2).Height(height - 2).MaxWidth(width).MaxHeight(height).Render(fit(content, width-4, height-2))
}

// windowRows returns the visible slice bounds [start, end) for a list of n rows
// scrolled so the selected row stays inside slots visible rows.
func windowRows(selected, n, slots int) (start, end int) {
	slots = max(1, slots)
	if selected >= slots {
		start = min(selected-slots+1, max(0, n-slots))
	}
	return start, min(n, start+slots)
}

func selectionRow(text string, width int) string {
	if width < 1 {
		return ""
	}
	if width == 1 {
		return selectionRailStyle.Render("▌")
	}
	if width == 2 {
		return selectionRailStyle.Render("▌ ")
	}
	body := selectionBodyStyle.Width(width - 2).Render(fit(text, width-2, 1))
	return selectionRailStyle.Render("▌ ") + body
}

func (m model) taskPanel(width, height int) string {
	inner := m.layout().panelInner(width)
	all := taskListRows(m.tasks)
	rows := m.mainRows()
	title := sectionStyle.Render("TASKS") + faintStyle.Render(fmt.Sprintf("  / %02d", len(all)))
	if models := m.modelSummary(inner - lipgloss.Width(title) - 1); models != "" {
		title += strings.Repeat(" ", inner-lipgloss.Width(title)-lipgloss.Width(models)) + models
	}
	lines := []string{fit(title, inner, 1), ""}
	if len(all) == 0 {
		return surfacePanel(m.landingContent(inner), width, height)
	}
	highlight := rootIndex(m.tasks, m.selected)
	selectedStart, selectedEnd := -1, -1
	showHeaders := m.visibleGroupCount(rows) > 1
	current := taskGroup(-1)
	for i, row := range rows {
		group := m.groupOf(row.task)
		if showHeaders && group != current {
			if current >= 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.groupHeader(group, rows))
			current = group
		}
		start := len(lines)
		lines = append(lines,
			m.taskTitleRow(row.task, i+1, len(rows), inner, row.index == highlight && !m.inputFocused()),
			m.mainTaskMetaLine(row.task, inner))
		if row.index == highlight {
			selectedStart, selectedEnd = start, len(lines)
		}
	}
	if m.doneCollapsed(all) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		done, merged := 0, 0
		for _, row := range all {
			if m.groupOf(row.task) == groupDone {
				done++
				if row.task.MergeStatus == orchestrator.MergeMerged {
					merged++
				}
			}
		}
		summary := fmt.Sprintf("DONE %d · merged %d", done, merged)
		lines = append(lines, faintStyle.Render(summary)+mutedStyle.Render("    E expand"))
	}
	available := max(1, height)
	start := 0
	if selectedEnd > available {
		start = selectedEnd - available
		if selectedStart >= 0 && selectedStart < start {
			start = selectedStart
		}
	}
	if maxStart := len(lines) - available; start > maxStart {
		start = max(0, maxStart)
	}
	end := min(len(lines), start+available)
	return surfacePanel(strings.Join(lines[start:end], "\n"), width, height)
}

// groupHeader names one board group. The header already carries the counts, so
// the heading stays one word.
func (m model) groupHeader(group taskGroup, _ []taskListRow) string {
	style := mutedStyle
	switch group {
	case groupAttention:
		style = amberStyle()
	case groupRunning:
		style = blueStyle()
	}
	return style.Render(group.label())
}

// visibleGroupCount reports how many groups the current rows occupy. One group
// needs no heading; the rows already say what they are.
func (m model) visibleGroupCount(rows []taskListRow) int {
	seen := map[taskGroup]bool{}
	for _, row := range rows {
		seen[m.groupOf(row.task)] = true
	}
	return len(seen)
}

// subtaskPanel is the detailed view's sidebar: every descendant of the selected
// main task, with the focused subtask marked.
func (m model) subtaskPanel(width, height int, inner int, root orchestrator.Task, rows []taskListRow) string {
	lines := []string{
		sectionStyle.Render("SUBTASKS") + mutedStyle.Render(fmt.Sprintf("  / %02d", len(rows))),
		mutedStyle.Render(fit(singleLine(root.Title), inner, 1)),
		"",
	}
	selected := 0
	for i, row := range rows {
		if row.index == m.selected {
			selected = i
			break
		}
	}
	start, end := windowRows(selected, len(rows), (height-3)/2)
	for i := start; i < end; i++ {
		row := rows[i]
		lines = append(lines,
			m.taskTitleRow(row.task, 0, 0, inner, row.index == m.selected && !m.inputFocused()),
			taskMetaLine(row.task, inner))
	}
	return surfacePanel(strings.Join(lines, "\n"), width, height)
}

// selectedPosition returns the selected task's one-based position in grouped
// display order, which is the order the sidebar and navigation use.
func (m model) selectedPosition() (position, total int, ok bool) {
	rows := m.mainRows()
	highlight := rootIndex(m.tasks, m.selected)
	for i, row := range rows {
		if row.index == highlight {
			return i + 1, len(rows), true
		}
	}
	return 0, 0, false
}

// mainTaskMetaLine is the task list's secondary row: workspace or review state,
// elapsed time, model, line churn and the keys that resolve an attention state.
// The executor is left to task details because routing usually picks general. A
// running task also shows the last observed activity, so a silent agent looks
// different from a busy one.
func (m model) mainTaskMetaLine(task orchestrator.Task, width int) string {
	now := time.Now()
	sep := mutedStyle.Render("  ·  ")
	parts := []string{workspaceBadgeFor(task).render()}
	if count := len(subtaskRows(m.tasks, task.ID)); count > 0 {
		parts = append(parts, mutedStyle.Render(fmt.Sprintf("%d subtask(s)", count)))
	}
	elapsed, hasElapsed := taskElapsed(task, now)
	stale := false
	if task.Status == orchestrator.Running {
		if at, _ := lastActivity(task); !at.IsZero() && now.Sub(at) > staleAfter {
			stale = true
		}
	}
	if hasElapsed {
		text := shortDuration(elapsed)
		if stale {
			parts = append(parts, warningStyle.Render("⚠ "+text))
		} else {
			parts = append(parts, mutedStyle.Render(text))
		}
	}
	if model := taskModel(task); model != "" {
		parts = append(parts, mutedStyle.Render(model))
	}
	if usage := task.Usage(); !usage.IsZero() {
		available := width - 2 - lipgloss.Width(strings.Join(parts, sep)) - lipgloss.Width(sep)
		if segment := taskListUsage(usage, available); segment != "" {
			parts = append(parts, mutedStyle.Render(segment))
		}
	}
	if added, removed, ok := m.diffStat(task); ok && added+removed > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(green).Render(fmt.Sprintf("+%d", added))+
			mutedStyle.Render(" ")+lipgloss.NewStyle().Foreground(red).Render(fmt.Sprintf("−%d", removed)))
	}
	if hint := actionHint(task); hint != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(statusWaiting).Render(hint))
	}
	if task.Status == orchestrator.Running {
		if at, summary := lastActivity(task); summary != "" && !at.IsZero() {
			line := "last " + shortDuration(now.Sub(at)) + ": " + singleLine(summary)
			parts = append(parts, faintStyle.Render(line))
		} else if stale {
			parts = append(parts, faintStyle.Render("no activity "+shortDuration(now.Sub(task.UpdatedAt))))
		}
	}
	return fit("  "+strings.Join(parts, sep), width, 1)
}

// executorLabel shows the executor and, when chosen, its profile.
func executorLabel(t orchestrator.Task) string {
	if t.ParentID != "" {
		return t.Agent + " · subagent"
	}
	if t.ProfileName == "" || (t.ProfileSpec != nil && t.ProfileSpec.UsesExecutorDefaults()) {
		return t.Agent
	}
	return t.Agent + " · profile " + t.ProfileName
}

func profileLabel(p orchestrator.Profile) string {
	if p.UsesExecutorDefaults() {
		return "Executor defaults"
	}
	return p.Name
}

func taskProfileLabel(t orchestrator.Task) string {
	if t.ProfileSpec != nil {
		return profileLabel(*t.ProfileSpec)
	}
	return t.ProfileName
}

func describeProfile(profile orchestrator.Profile) (label, detail string) {
	details := []string{profile.Description}
	if profile.Model != "" {
		details = append(details, "model "+profile.Model)
	}
	if profile.NoIntegrate {
		details = append(details, "no auto-integration")
	}
	if profile.NoSubagents {
		details = append(details, "no delegation")
	}
	if len(profile.Tools) > 0 {
		details = append(details, "tools "+strings.Join(profile.Tools, ","))
	}
	if details[0] == "" {
		details = details[1:]
	}
	return terminalText(profileLabel(profile)), terminalText(strings.Join(details, " · "))
}

func describeAgent(agent orchestrator.AgentInfo) (label, detail string) {
	detail = "One-shot · no live context"
	if agent.Capabilities.LiveContext {
		detail = "Live follow-up context"
	}
	if agent.Capabilities.ToolActivity {
		detail += " · tool activity"
	}
	if agent.Capabilities.StructuredReports {
		detail += " · structured reports"
	}
	label = terminalText(agent.Name)
	if agent.Description != "" {
		label += " · " + terminalText(agent.Description)
	}
	return label, detail
}

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

// menuRow is one selectable line in a modal list menu. tag is pre-styled and
// right-aligned, or empty for a plain row.
type menuRow struct {
	label string
	tag   string
}

// menuView is the single renderer for the login and model menus.
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
		faintStyle.Render(strings.Repeat("─", inner)),
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
			tag := ""
			if row.tag != "" {
				tag = row.tag
			}
			plain := 2 + lipgloss.Width(row.label) + lipgloss.Width(ansi.Strip(tag))
			line := marker + name
			if tag != "" && plain < inner {
				line += strings.Repeat(" ", inner-plain) + tag
			}
			content = append(content, selectionHighlightStyle.Width(inner).Render(fit(ansi.Strip(line), inner, 1)))
			continue
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

// listMenuKey handles the keys every list menu and picker shares: ctrl+c asks
// to quit and j/k/up/down move index within count. It reports whether it
// handled key.
func (m *model) listMenuKey(key string, index *int, count int) bool {
	switch key {
	case "ctrl+c":
		*m = m.requestQuit()
	case "up", "k":
		*index = max(0, min(count-1, *index-1))
	case "down", "j":
		*index = max(0, min(count-1, *index+1))
	default:
		return false
	}
	return true
}

// setupState is the setup popup for a held task, captured when it opens. Its
// Executor and Profile rows open the agent and profile pickers in place.
type setupState struct {
	target string
	index  int
}

var setupRows = []string{"Executor", "Profile", "Isolation"}

// setupTask is the captured task, or a zero task once it is gone; the service
// then refuses changes to it.
func (m model) setupTask() orchestrator.Task {
	for _, t := range m.tasks {
		if t.ID == m.setup.target {
			return t
		}
	}
	return orchestrator.Task{}
}

func (m model) setupKey(key string) (model, tea.Cmd) {
	switch {
	case m.agentPicker.target != "":
		return m.agentPickerKey(key), nil
	case m.profilePicker.target != "":
		return m.profilePickerKey(key), nil
	case m.listMenuKey(key, &m.setup.index, len(setupRows)):
		return m, nil
	}
	t := m.setupTask()
	switch {
	case key == "esc":
		m.setup = setupState{}
	case key != "enter":
	case m.setup.index == 0:
		openPicker(&m.agentPicker, m.setup.target, m.service.ListAgents(), func(a orchestrator.AgentInfo) bool { return a.Name == t.Agent })
	case m.setup.index == 1:
		openPicker(&m.profilePicker, m.setup.target, m.service.ListProfiles(), func(p orchestrator.Profile) bool { return p.Name == t.ProfileName })
	case !m.isolationBusy:
		m.isolationBusy = true
		m.notice.text = "Checking workspace isolation…"
		service, id, enabled := m.service, m.setup.target, !t.Isolated
		return m, func() tea.Msg {
			notice := "Isolation off: task uses the source workspace."
			if enabled {
				notice = "Isolation enabled: starts from committed HEAD; uncommitted files are excluded."
			}
			return isolationResultMsg(resultFor(notice, service.SetTaskIsolation(id, enabled)))
		}
	}
	return m, nil
}

func (m model) setupView(background string, w, h int) string {
	t := m.setupTask()
	subtitle, index, footer := singleLine(t.Title), m.setup.index, "j/k select · enter change · esc close"
	var rows []menuRow
	choice := func(label, detail string) { rows = append(rows, menuRow{label: label, tag: mutedStyle.Render(detail)}) }
	switch {
	case m.agentPicker.target != "":
		for _, a := range m.agentPicker.choices {
			choice(describeAgent(a))
		}
		subtitle, index, footer = "Executor", m.agentPicker.index, "j/k select · enter choose · esc back"
	case m.profilePicker.target != "":
		for _, p := range m.profilePicker.choices {
			choice(describeProfile(p))
		}
		subtitle, index, footer = "Profile", m.profilePicker.index, "j/k select · enter choose · esc back"
	default:
		isolation := "off"
		if t.Isolated {
			isolation = "on"
		}
		for i, value := range []string{t.Agent, taskProfileLabel(t), isolation} {
			rows = append(rows, menuRow{label: setupRows[i], tag: accentStyle.Render(terminalText(value))})
		}
	}
	return menuView(background, w, h, "SETUP", subtitle, nil, rows, index, "Nothing to choose.", footer, min(72, w-4), min(len(rows)+8, h-2))
}

// pickerState is a held task's agent or profile chooser. target is captured
// when the picker opens.
type pickerState[T any] struct {
	target  string
	choices []T
	index   int
}

func openPicker[T any](p *pickerState[T], target string, choices []T, current func(T) bool) {
	*p = pickerState[T]{target: target, choices: choices}
	for i, choice := range choices {
		if current(choice) {
			p.index = i
		}
	}
}

// pickerKey drives an open picker. Enter hands the choice to apply, which
// returns the success notice and the service error.
func pickerKey[T any](m *model, p *pickerState[T], key string, apply func(target string, choice T) (string, error)) {
	if m.listMenuKey(key, &p.index, len(p.choices)) {
		return
	}
	switch key {
	case "enter":
		if len(p.choices) > 0 {
			notice, err := apply(p.target, p.choices[p.index])
			m.notice.text = resultFor(notice, err).text()
			m.refreshTasks()
		}
	case "esc":
	default:
		return
	}
	*p = pickerState[T]{}
}

func (m model) profilePickerKey(key string) model {
	pickerKey(&m, &m.profilePicker, key, func(id string, p orchestrator.Profile) (string, error) {
		return "Profile selected: " + profileLabel(p), m.service.SetTaskProfile(id, p.Name)
	})
	return m
}

func (m model) agentPickerKey(key string) model {
	pickerKey(&m, &m.agentPicker, key, func(id string, a orchestrator.AgentInfo) (string, error) {
		return "Agent selected: " + a.Name, m.service.SetTaskAgent(id, a.Name)
	})
	return m
}
