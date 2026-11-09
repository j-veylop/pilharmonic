// Package tui provides the interactive terminal interface.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
)

const runningGlyphPlaceholder = "\uE000"

var defaultRunningGlyph = spinner.MiniDot.Frames[0]

type tickMsg time.Time

type isolationResultMsg struct {
	err     error
	enabled bool
}

type model struct {
	activity         bool
	thinking         bool
	activityFilter   int
	activityID       string
	activityAttempt  string
	activityExpanded bool
	vimG             bool
	vimCount         int
	agentPicker      pickerState[orchestrator.AgentInfo]
	profilePicker    pickerState[orchestrator.Profile]
	service          *orchestrator.Service
	workspace        string
	tasks            []orchestrator.Task
	selected         int
	router           bool
	routerDraft      string
	routerCursor     int
	routerScroll     int
	routerFocus      int
	routerSelected   int
	width, height    int
	input            composer
	historyOffset    int
	notice           string
	history          bool
	detailFocus      bool
	fullscreen       bool
	help             bool
	helpPage         int
	helpScroll       int
	isolationBusy    bool
	cleanup          cleanupRequest
	cleanupBusy      bool
	selection        textSelection
	clipboard        io.Writer
	spinner          spinner.Model
}

// Run starts the terminal interface. The service owns task execution.
func Run(service *orchestrator.Service, workspace string) error {
	initial := model{
		service:   service,
		workspace: workspace,
		tasks:     service.ListTasks(),
		router:    true,
		spinner:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	output := newTerminalOutput(os.Stdout)
	initial.clipboard = output
	_, err := tea.NewProgram(initial, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(output)).Run()
	return err
}

func tick() tea.Cmd           { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m model) Init() tea.Cmd { return tea.Batch(tick(), m.spinner.Tick) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case cleanupResultMsg:
		m.cleanupBusy = false
		m.notice = string(msg)
		selected := m.selectedID()
		m.tasks = m.service.ListTasks()
		for i, task := range m.tasks {
			if task.ID == selected {
				m.selected = i
				break
			}
		}
		m.clamp()
		m.clampScroll()
		m.selection = textSelection{}
		return m, nil
	case isolationResultMsg:
		m.isolationBusy = false
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else if msg.enabled {
			m.notice = "Isolation enabled: starts from committed HEAD; uncommitted files are excluded."
		} else {
			m.notice = "Isolation off: task uses the source workspace."
		}
		m.tasks = m.service.ListTasks()
		m.clamp()
		m.clampScroll()
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		m.cancelStaleSelection()
	case tickMsg:
		oldID := m.selectedID()
		oldLines := m.detailLineCount()
		m.tasks = m.service.ListTasks()
		m.clamp()
		m.clampRouter()
		if oldID != m.selectedID() {
			m.activityID = ""
			m.activityExpanded = false
			m.historyOffset = 0
			m.selection = textSelection{}
		} else if m.historyOffset > 0 {
			m.historyOffset += m.detailLineCount() - oldLines
		}
		m.clampScroll()
		m.cancelStaleSelection()
		return m, tick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case selectionCopiedMsg:
		if msg.err != nil {
			m.notice = "Could not copy selection: " + msg.err.Error()
		} else if msg.lines > 0 {
			m.notice = fmt.Sprintf("Copied %d line(s) to clipboard via OSC 52.", msg.lines)
		}
		return m, nil
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		m.selection = textSelection{}
		if m.help {
			return m.helpKey(msg.String())
		}
		if m.router {
			return m.routerKey(msg)
		}
		if m.cleanup.kind != "" {
			return m.cleanupKey(msg.String())
		}
		if m.profilePicker.target != "" {
			return m.profilePickerKey(msg.String())
		}
		if m.agentPicker.target != "" {
			return m.agentPickerKey(msg.String())
		}
		if m.input.kind != "" {
			return m.composerKey(msg)
		}
		if m.activityKey(msg.String()) {
			return m, nil
		}
		oldTaskID := m.selectedID()
		if m.vimNavigation(msg.String()) {
			if oldTaskID != m.selectedID() {
				m.activityID = ""
				m.activityExpanded = false
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.fullscreen {
				m.fullscreen = false
				m.detailFocus = false
			} else {
				m.detailFocus = !m.detailFocus
			}
			m.clampScroll()
		case "z":
			m.fullscreen = !m.fullscreen
			m.clampScroll()
		case "?":
			m.help = !m.help
			m.helpPage = 0
			m.helpScroll = 0
			m.clampScroll()
		case "esc":
			m.fullscreen = false
			m.detailFocus = false
			m.help = false
			m.clampScroll()
		case "t":
			m.activity = !m.activity
			m.activityID = ""
			m.activityExpanded = false
			m.historyOffset = 0
			m.detailFocus = true
			if m.activity {
				m.historyOffset = m.maxScroll()
			}
		case "T":
			m.thinking = !m.thinking
			m.clampScroll()
		case "tab":
			m.activity = false
			m.activityExpanded = false
			m.history = !m.history
			m.historyOffset = 0
		case "A":
			if len(m.tasks) > 0 {
				task := m.tasks[m.selected]
				if task.Status != orchestrator.Queued || task.ScheduleRequested {
					m.notice = "Choose an agent before starting."
				} else {
					m.agentPicker.target = task.ID
					m.agentPicker.choices = m.service.ListAgents()
					m.agentPicker.index = 0
					for i, agent := range m.agentPicker.choices {
						if agent.Name == task.Agent {
							m.agentPicker.index = i
						}
					}
					m.notice = ""
				}
			}
		case "P":
			if len(m.tasks) > 0 {
				task := m.tasks[m.selected]
				if task.Status != orchestrator.Queued || task.ScheduleRequested {
					m.notice = "Choose a profile before starting."
				} else {
					m.profilePicker.target = task.ID
					m.profilePicker.choices = m.service.ListProfiles()
					m.profilePicker.index = 0
					for i, profile := range m.profilePicker.choices {
						if profile.Name == task.ProfileName {
							m.profilePicker.index = i
						}
					}
					m.notice = ""
				}
			}
		case "/":
			m.router = true
			m.routerFocus = 0
			m.routerDraft = ""
			m.routerCursor = 0
			m.routerScroll = 0
			m.notice = ""
			m.clampRouter()
		case "n":
			m.input = composer{kind: newTaskInput}
			m.notice = ""
		case "a":
			if len(m.tasks) > 0 {
				m.input = composer{kind: relatedTaskInput, target: m.tasks[m.selected].ID}
				m.notice = ""
			}
		case "b":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if t.Status != orchestrator.Running {
					m.notice = "Spawn subagents only from a running task."
				} else {
					m.input = composer{kind: subagentInput, target: t.ID}
					m.notice = "Enter a self-contained mandate for the subagent."
				}
			}
		case "[":
			m.scrollDetail(1)
		case "]":
			m.scrollDetail(-1)
		case "home":
			m.historyOffset = m.maxScroll()
		case "end":
			m.historyOffset = 0
		case "f":
			if len(m.tasks) > 0 {
				m.input = composer{kind: threadInput, target: m.tasks[m.selected].ID}
				m.notice = ""
			}
		case "F":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				m.input = composer{kind: followUpInput, target: t.ID}
				m.notice = ""
				if t.Status == orchestrator.Queued || t.Status == orchestrator.Running {
					m.notice = "Context does not expand the mandate."
				} else {
					m.notice = "Follow-up requeues the task; press r to start."
				}
			}
		case "p":
			if len(m.tasks) > 0 {
				m.input = composer{kind: reportInput, target: m.tasks[m.selected].ID}
				m.notice = "Report: kind and message (progress, waiting, blocked, ready, failed)"
			}
		case "s":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if err := m.service.SetContextSubscription(t.ID, t.ContextPaused); err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Context subscription updated."
				}
			}
		case "w":
			if len(m.tasks) > 0 && !m.isolationBusy {
				t := m.tasks[m.selected]
				if t.Status != orchestrator.Queued || t.ScheduleRequested {
					m.notice = "Choose isolation before starting."
				} else {
					m.isolationBusy = true
					m.notice = "Checking workspace isolation…"
					return m, func() tea.Msg { return isolationResultMsg{m.service.SetTaskIsolation(t.ID, !t.Isolated), !t.Isolated} }
				}
			}
		case "r":
			if len(m.tasks) > 0 {
				err := m.service.Start(context.Background(), m.tasks[m.selected].ID)
				if err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Execution requested; queued tasks start when capacity frees."
				}
			}
		case "R":
			if len(m.tasks) > 0 {
				err := m.service.Retry(m.tasks[m.selected].ID)
				if err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Task queued for retry; press r to start."
				}
			}
		case "v":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if !t.AwaitingReview() {
					m.notice = "No worktree commit awaits review."
				} else if err := m.service.ApproveReview(t.ID, t.ReviewCommit, ""); err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Review approved for commit " + t.ReviewCommit + "."
				}
			}
		case "V":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if !t.AwaitingReview() {
					m.notice = "No worktree commit awaits review."
				} else {
					m.input = composer{kind: rejectReviewInput, target: t.ID, reviewCommit: t.ReviewCommit}
					m.notice = "Enter a reason to reject the review."
				}
			}
		case "d", "D", "x", "X":
			m.prepareCleanup(msg.String())
		case "c":
			if len(m.tasks) > 0 {
				err := m.service.Cancel(m.tasks[m.selected].ID)
				if err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Cancellation requested."
				}
			}
		}
		m.tasks = m.service.ListTasks()
		m.clamp()
		m.clampScroll()
	}
	return m, nil
}
func (m *model) clamp() {
	if m.selected >= len(m.tasks) {
		m.selected = len(m.tasks) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m model) selectedID() string {
	if m.selected < 0 || m.selected >= len(m.tasks) {
		return ""
	}
	return m.tasks[m.selected].ID
}
func (m *model) clampScroll() {
	if m.historyOffset < 0 {
		m.historyOffset = 0
	}
	if max := m.maxScroll(); m.historyOffset > max {
		m.historyOffset = max
	}
}
func (m *model) scrollDetail(delta int) { m.historyOffset += delta; m.clampScroll() }
