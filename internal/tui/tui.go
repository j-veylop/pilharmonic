// Package tui provides the interactive terminal interface.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	agentTask        string
	agentChoices     []orchestrator.AgentInfo
	agentIndex       int
	service          *orchestrator.Service
	workspace        string
	tasks            []orchestrator.Task
	selected         int
	router           bool
	routerDraft      string
	routerFocus      int
	routerSelected   int
	width, height    int
	input            bool
	draft            string
	contextTask      string
	relatedTask      string
	subagentTask     string
	reportTask       string
	followUpTask     string
	historyOffset    int
	notice           string
	history          bool
	detailFocus      bool
	fullscreen       bool
	help             bool
	helpPage         int
	helpScroll       int
	isolationBusy    bool
	deleteTaskID     string
	deleteTaskTitle  string
	deleteCompleted  bool
	deleteCount      int
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
	_, err := tea.NewProgram(initial, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func tick() tea.Cmd           { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m model) Init() tea.Cmd { return tea.Batch(tick(), m.spinner.Tick) }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case isolationResultMsg:
		m.isolationBusy = false
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else if msg.enabled {
			m.notice = "Isolation enabled: committed Git HEAD only; local uncommitted files are not copied."
		} else {
			m.notice = "Isolation disabled; task will run in its source workspace."
		}
		m.tasks = m.service.ListTasks()
		m.clamp()
		m.clampScroll()
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
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
		} else if m.historyOffset > 0 {
			m.historyOffset += m.detailLineCount() - oldLines
		}
		m.clampScroll()
		return m, tick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.MouseMsg:
		if m.help {
			if msg.Button == tea.MouseButtonWheelUp {
				m.scrollHelpBody(-3)
			}
			if msg.Button == tea.MouseButtonWheelDown {
				m.scrollHelpBody(3)
			}
			return m, nil
		}
		if !m.input && !m.router && m.deleteTaskID == "" && m.agentTask == "" && !m.deleteCompleted {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.scrollDetail(3)
			case tea.MouseButtonWheelDown:
				m.scrollDetail(-3)
			}
		}
		return m, nil
	case tea.KeyMsg:
		if m.help {
			switch msg.String() {
			case "?", "esc", "q":
				m.help = false
			case "ctrl+c":
				return m, tea.Quit
			case "l", "right", " ", "tab":
				m.scrollHelp(1)
			case "h", "left", "shift+tab":
				m.scrollHelp(-1)
			case "j", "down", "ctrl+e":
				m.scrollHelpBody(1)
			case "k", "up", "ctrl+y":
				m.scrollHelpBody(-1)
			case "pgdown", "ctrl+f", "ctrl+d":
				m.scrollHelpBody(8)
			case "pgup", "ctrl+b", "ctrl+u":
				m.scrollHelpBody(-8)
			case "home", "g":
				m.helpPage = 0
				m.helpScroll = 0
			case "end", "G":
				m.helpPage = 10000
				m.scrollHelp(0)
			}
			return m, nil
		}
		if m.router {
			var cmd tea.Cmd
			m, cmd = m.routerKey(msg)
			return m, cmd
		}
		if m.deleteTaskID != "" {
			switch msg.String() {
			case "y":
				err := m.service.DeleteTask(m.deleteTaskID)
				if err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Task and its history deleted."
				}
				m.deleteTaskID, m.deleteTaskTitle = "", ""
				m.tasks = m.service.ListTasks()
				m.clamp()
			case "n", "esc":
				m.deleteTaskID, m.deleteTaskTitle = "", ""
				m.notice = "Deletion canceled."
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		if m.deleteCompleted {
			switch msg.String() {
			case "y":
				count, err := m.service.DeleteCompletedTasks()
				if err != nil {
					m.notice = err.Error()
				} else {
					m.notice = fmt.Sprintf("Deleted %d completed task(s) and their history.", count)
				}
				m.deleteCompleted, m.deleteCount = false, 0
				m.tasks = m.service.ListTasks()
				m.clamp()
			case "n", "esc":
				m.deleteCompleted, m.deleteCount = false, 0
				m.notice = "Deletion canceled."
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		if m.agentTask != "" {
			switch msg.String() {
			case "esc":
				m.agentTask = ""
				m.agentChoices = nil
			case "ctrl+c":
				return m, tea.Quit
			case "up", "k":
				if m.agentIndex > 0 {
					m.agentIndex--
				}
			case "down", "j":
				if m.agentIndex < len(m.agentChoices)-1 {
					m.agentIndex++
				}
			case "enter":
				if len(m.agentChoices) > 0 {
					name := m.agentChoices[m.agentIndex].Name
					if err := m.service.SetTaskAgent(m.agentTask, name); err != nil {
						m.notice = err.Error()
					} else {
						m.notice = "Agent selected: " + name
					}
					m.tasks = m.service.ListTasks()
					m.clamp()
				}
				m.agentTask = ""
				m.agentChoices = nil
			}
			return m, nil
		}
		if m.input {
			switch msg.Type {
			case tea.KeyEsc:
				m.input = false
				m.contextTask = ""
				m.relatedTask = ""
				m.subagentTask = ""
				m.reportTask = ""
				m.followUpTask = ""
				m.draft = ""
			case tea.KeyEnter:
				prompt := strings.TrimSpace(m.draft)
				if prompt == "" {
					m.notice = "Enter a task before submitting."
					break
				}
				if m.reportTask != "" {
					kind, message, _ := strings.Cut(prompt, " ")
					err := m.service.RecordReport(m.reportTask, kind, message)
					if err != nil {
						m.notice = err.Error()
					} else {
						m.notice = "Report recorded."
					}
					m.reportTask = ""
				} else if m.contextTask != "" {
					err := m.service.AddManualThreadContext(m.contextTask, prompt)
					if err != nil {
						m.notice = err.Error()
					} else {
						m.notice = "Thread context recorded; see history for delivery status."
					}
					m.contextTask = ""
				} else if m.followUpTask != "" {
					requeued, err := m.service.FollowUp(m.followUpTask, prompt)
					if err != nil {
						m.notice = err.Error()
					} else if requeued {
						m.notice = "Follow-up recorded; task requeued. Press r to start it."
					} else {
						m.notice = "Follow-up recorded; see history for delivery status."
					}
					m.followUpTask = ""
				} else if m.subagentTask != "" {
					task, err := m.service.SpawnSubagent(m.subagentTask, "", prompt, "")
					if err != nil {
						m.notice = err.Error()
					} else {
						m.notice = "Subagent started: " + task.ID
					}
					m.subagentTask = ""
				} else {
					title := prompt
					if len([]rune(title)) > 60 {
						title = string([]rune(title)[:57]) + "..."
					}
					var task orchestrator.Task
					var err error
					if m.relatedTask != "" {
						task, err = m.service.CreateRelatedTask(m.relatedTask, title, prompt, m.workspace)
					} else {
						task, err = m.service.CreateTask(title, prompt, m.workspace)
					}
					m.relatedTask = ""
					if err != nil {
						m.notice = err.Error()
					} else {
						m.notice = "Task queued: " + task.ID
					}
				}
				m.tasks = m.service.ListTasks()
				m.input = false
				m.draft = ""
				m.clamp()
			case tea.KeyBackspace, tea.KeyCtrlH:
				if len(m.draft) > 0 {
					_, n := utf8.DecodeLastRuneInString(m.draft)
					m.draft = m.draft[:len(m.draft)-n]
				}
			case tea.KeyRunes:
				m.draft += string(msg.Runes)
			case tea.KeySpace:
				m.draft += " "
			}
			return m, nil
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
					m.notice = "Choose an agent before requesting a start."
				} else {
					m.agentTask = task.ID
					m.agentChoices = m.service.ListAgents()
					m.agentIndex = 0
					for i, agent := range m.agentChoices {
						if agent.Name == task.Agent {
							m.agentIndex = i
						}
					}
					m.notice = ""
				}
			}
		case "/":
			m.router = true
			m.routerFocus = 0
			m.routerDraft = ""
			m.notice = ""
			m.clampRouter()
		case "n":
			m.input = true
			m.notice = ""
		case "a":
			if len(m.tasks) > 0 {
				m.relatedTask = m.tasks[m.selected].ID
				m.input = true
				m.notice = ""
			}
		case "b":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if t.Status != orchestrator.Running {
					m.notice = "Subagents can only be spawned from a running task."
				} else {
					m.subagentTask = t.ID
					m.input = true
					m.notice = "Subagent: enter a self-contained mandate."
				}
			}
		case "pgup":
			m.scrollDetail(m.detailPage())
		case "pgdown":
			m.scrollDetail(-m.detailPage())
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
				m.contextTask = m.tasks[m.selected].ID
				m.input = true
				m.notice = ""
			}
		case "F":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				m.followUpTask = t.ID
				m.input = true
				m.notice = ""
				if t.Status == orchestrator.Queued || t.Status == orchestrator.Running {
					m.notice = "Follow up this task; context does not expand its mandate."
				} else {
					m.notice = "Follow up this task; it will be requeued and needs r to start."
				}
			}
		case "up", "k":
			if m.detailFocus || m.fullscreen {
				m.scrollDetail(1)
				return m, nil
			}
			m.historyOffset = 0
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.detailFocus || m.fullscreen {
				m.scrollDetail(-1)
				return m, nil
			}
			m.historyOffset = 0
			if m.selected < len(m.tasks)-1 {
				m.selected++
			}
		case "p":
			if len(m.tasks) > 0 {
				m.reportTask = m.tasks[m.selected].ID
				m.input = true
				m.notice = "Report: progress, waiting, blocked, ready or failed, followed by a message"
			}
		case "s":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if err := m.service.SetContextSubscription(t.ID, t.ContextPaused); err != nil {
					m.notice = err.Error()
				} else {
					m.notice = "Thread subscription updated."
				}
			}
		case "w":
			if len(m.tasks) > 0 && !m.isolationBusy {
				t := m.tasks[m.selected]
				if t.Status != orchestrator.Queued || t.ScheduleRequested {
					m.notice = "Choose workspace isolation before requesting a start."
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
					m.notice = "Execution requested; waiting tasks start when capacity is available."
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
		case "d":
			if len(m.tasks) > 0 {
				task := m.tasks[m.selected]
				if task.Status == orchestrator.Running {
					m.notice = "Cancel the running task and wait before deleting."
				} else {
					m.deleteTaskID, m.deleteTaskTitle = task.ID, task.Title
					m.notice = ""
				}
			}
		case "D":
			count := m.service.DeletableCompletedTasks()
			if count == 0 {
				m.notice = "No completed tasks to delete."
			} else {
				m.deleteCompleted, m.deleteCount = true, count
				m.notice = ""
			}
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
