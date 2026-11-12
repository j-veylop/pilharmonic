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
	"pilharmonic/internal/orchestrator"
)

const runningGlyphPlaceholder = "\uE000"

var defaultRunningGlyph = spinner.MiniDot.Frames[0]

type tickMsg time.Time

type isolationResultMsg struct {
	err     error
	enabled bool
}

// actionResultMsg reports a mutating service call that carries no UI-only
// payload beyond its notice.
type actionResultMsg struct {
	notice string
	err    error
}

// composerResultMsg reports a composer submission. The draft is retained when
// err is set so the operator can correct it.
type composerResultMsg struct {
	notice string
	err    error
}

// routerResultMsg reports a submitted request. A task with an empty ID means
// nothing was accepted and the draft is still the operator's only copy.
type routerResultMsg struct {
	task orchestrator.Task
	err  error
}

// fetchModelsMsg delivers a provider's model list (or its fetch error) to the
// model menu.
type fetchModelsMsg struct {
	provider string
	models   []string
	err      error
}

// Service is the orchestrator surface the terminal drives. It keeps the model
// testable and lets slow service calls run outside the update loop.
type Service interface {
	ListTasks() []orchestrator.Task
	ListAgents() []orchestrator.AgentInfo
	ListProfiles() []orchestrator.Profile
	SubmitRequest(text, workspace string) (orchestrator.Task, error)
	Start(ctx context.Context, id string) error
	Retry(id string) error
	Cancel(id string) error
	ApproveReview(id, commit, note string) error
	RejectReview(id, commit, note string) error
	FollowUp(taskID, text string) (bool, error)
	CreateTask(title, prompt, workspace string) (orchestrator.Task, error)
	CreateRelatedTask(parentID, title, prompt, workspace string) (orchestrator.Task, error)
	SpawnSubagent(parentID, title, prompt, agent string) (orchestrator.Task, error)
	RecordReport(taskID, kind, message string) error
	AddManualThreadContext(taskID, text string) error
	SetContextSubscription(taskID string, enabled bool) error
	SetTaskIsolation(id string, enabled bool) error
	SetTaskAgent(id, name string) error
	SetTaskProfile(id, name string) error
	CompletedDeletionCandidates() []string
	WorktreeDiscardCandidates() []string
	DeleteTask(id string) error
	DiscardWorktree(id string) error
	DeleteCompletedTasks(ids []string) (int, error)
	DeleteCompletedTasksDetailed(ids []string) (int, []orchestrator.SkippedDeletion, error)
	DiscardMergedWorktrees(ids []string) (int, error)
	SetProviderCredential(provider, key string) error
	RemoveProviderCredential(provider string) error
	ProviderCredentials() []orchestrator.ProviderCredential
	AnyProviderConfigured() bool
	SetModel(class, model string) error
	ModelSelection() orchestrator.ModelSelection
	ModelsForProvider(provider string) []string
	FetchModels(ctx context.Context, provider string) ([]string, error)
}

var _ Service = (*orchestrator.Service)(nil)

type model struct {
	activity          bool
	thinking          bool
	activityFilter    int
	activityID        string
	activityAttempt   string
	activityExpanded  bool
	vimG              bool
	vimCount          int
	agentPicker       pickerState[orchestrator.AgentInfo]
	profilePicker     pickerState[orchestrator.Profile]
	service           Service
	workspace         string
	tasks             []orchestrator.Task
	selected          int
	router            bool
	routerDraft       string
	routerCursor      int
	routerScroll      int
	routerFocus       int
	routerSelected    int
	width, height     int
	input             composer
	historyOffset     int
	notice            string
	history           bool
	detailFocus       bool
	fullscreen        bool
	help              bool
	helpPage          int
	helpScroll        int
	isolationBusy     bool
	cleanup           cleanupRequest
	cleanupBusy       bool
	selection         textSelection
	clipboard         io.Writer
	spinner           spinner.Model
	spinnerActive     bool
	markdown          *markdownCache
	quitConfirm       bool
	noticeTicks       int
	noticed           string
	loginProvider     string   // non-empty while entering an API key
	loginKey          string   // the key being typed (never rendered in full)
	loginMenu         bool     // the provider menu is open
	loginIndex        int      // selected row in the provider menu
	modelMenu         bool     // the agent -> provider -> model menu is open
	modelStep         int      // current model menu step; see modelStepClass
	modelIndex        int      // selected row in the current model menu step
	modelClass        string   // class chosen in the first step
	modelProvider     string   // provider chosen in the second step
	modelCustomInput  string   // model ID typed in the custom-model step
	modelList         []string // fetched models for modelProvider
	modelListProvider string   // provider modelList belongs to
	modelsLoading     bool     // a model fetch is in flight for modelProvider
}

// Run starts the terminal interface. The service owns task execution.
func Run(service *orchestrator.Service, workspace string) error {
	initial := newModel(service, workspace)
	output := newTerminalOutput(os.Stdout)
	initial.clipboard = output
	_, err := tea.NewProgram(initial, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(output)).Run()
	return err
}

// newModel builds the starting model. Without any usable provider the operator
// lands on the login menu instead of the request form, because no request can
// be routed until a credential exists.
func newModel(service Service, workspace string) model {
	m := model{
		service:   service,
		workspace: workspace,
		tasks:     service.ListTasks(),
		router:    true,
		spinner:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		markdown:  &markdownCache{},
	}
	if !service.AnyProviderConfigured() {
		m.loginMenu = true
		m.loginIndex = 0
		m.router = false
		m.notice = forcedLoginNotice
	}
	return m
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m model) Init() tea.Cmd {
	if m.hasLiveWork() {
		return tea.Batch(tick(), m.spinner.Tick)
	}
	return tick()
}

// hasLiveWork reports whether any task can still change output or scheduling
// state without operator input. The spinner only needs to run then.
func (m model) hasLiveWork() bool {
	for _, task := range m.tasks {
		if task.Status == orchestrator.Running || task.MergeStatus == orchestrator.MergeWaiting ||
			(task.Status == orchestrator.Queued && task.ScheduleRequested) {
			return true
		}
	}
	return false
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case actionResultMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = msg.notice
		}
		m.refreshTasks()
		m.clampScroll()
		return m, nil
	case composerResultMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
			if msg.notice != "" {
				m.notice = msg.notice + " · " + msg.err.Error()
			}
		} else {
			m.notice = msg.notice
			m.input = composer{}
		}
		m.refreshTasks()
		m.clampScroll()
		return m, nil
	case routerResultMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = routeNotice(msg.task)
		}
		if msg.task.ID == "" {
			return m, nil
		}
		m.refreshTasks()
		m.routerDraft = ""
		m.routerCursor = 0
		m.routerScroll = 0
		m.routerSelected = 0
		for i, current := range m.tasks {
			if current.ID == msg.task.ID {
				m.selected = i
				break
			}
		}
		m.clamp()
		m.clampScroll()
		return m, nil
	case cleanupResultMsg:
		m.cleanupBusy = false
		m.notice = string(msg)
		m.refreshTasks()
		m.clampScroll()
		m.selection = textSelection{}
		return m, nil
	case fetchModelsMsg:
		if msg.provider != m.modelProvider {
			return m, nil
		}
		m.modelsLoading = false
		if msg.err == nil && len(msg.models) > 0 {
			m.modelList = msg.models
			m.modelListProvider = msg.provider
		} else {
			m.modelList = nil
			m.modelListProvider = ""
		}
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
		m.refreshTasks()
		m.clampScroll()
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		m.cancelStaleSelection()
	case tickMsg:
		oldID := m.selectedID()
		oldLines := m.detailLineCount()
		m.refreshTasks()
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
		m.expireNotice()
		cmds := []tea.Cmd{tick()}
		if m.hasLiveWork() && !m.spinnerActive {
			m.spinnerActive = true
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(cmds...)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if !m.hasLiveWork() {
			m.spinnerActive = false
			return m, nil
		}
		m.spinnerActive = true
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
		// Login owns every key while it is armed, whether or not the request
		// form or the board is showing, so a secret can never reach a draft.
		if m.loginProvider != "" {
			return m.loginKeyMsg(msg)
		}
		if m.loginMenu {
			return m.providerMenuKey(msg.String())
		}
		if m.modelMenu {
			return m.modelMenuKey(msg.String())
		}
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

		if msg.String() != "q" {
			m.quitConfirm = false
		}
		switch msg.String() {
		case "q":
			if running, _ := m.headerCounts(); running > 0 && !m.quitConfirm {
				m.quitConfirm = true
				m.notice = fmt.Sprintf("%d tasks running; press q again to quit", running)
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+c":
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
		case "!":
			if m.jumpAttention() {
				if oldTaskID != m.selectedID() {
					m.activityID = ""
					m.activityExpanded = false
				}
			} else {
				m.notice = "No tasks need action."
			}
		case "L":
			m.loginMenu = true
			m.loginIndex = 0
		case "m":
			m.modelMenu = true
			m.modelStep = modelStepClass
			m.modelIndex = 0
			m.modelClass, m.modelProvider, m.modelCustomInput = "", "", ""
		case "/":
			m.router = true
			m.routerFocus = 0
			m.routerCursor = len(m.routerDraft)
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
				return m, startTaskCmd(m.service, m.tasks[m.selected].ID)
			}
		case "R":
			if len(m.tasks) > 0 {
				return m, retryTaskCmd(m.service, m.tasks[m.selected].ID)
			}
		case "v":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if !t.AwaitingReview() {
					m.notice = "No worktree commit awaits review."
				} else {
					return m, approveReviewCmd(m.service, t.ID, t.ReviewCommit)
				}
			}
		case "V":
			if len(m.tasks) > 0 {
				t := m.tasks[m.selected]
				if !t.AwaitingReview() {
					m.notice = "No worktree commit awaits review."
				} else {
					m.input = composer{kind: rejectReviewInput, target: t.ID, reviewCommit: t.ReviewCommit}
					m.notice = "Enter a reason to reject; the worktree is kept for F follow-up."
				}
			}
		case "d", "D", "x", "X":
			m.prepareCleanup(msg.String())
		case "c":
			if len(m.tasks) > 0 {
				return m, cancelTaskCmd(m.service, m.tasks[m.selected].ID)
			}
		}
		m.refreshTasks()
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

// refreshTasks reloads the service snapshot while keeping the operator on the
// task they were reading. The service sorts newest first, so an inserted task
// would otherwise shift the selected slice index to a different task.
func (m *model) refreshTasks() {
	selected := m.selectedID()
	if m.service == nil {
		m.clamp()
		return
	}
	m.tasks = m.service.ListTasks()
	if selected != "" {
		for i, task := range m.tasks {
			if task.ID == selected {
				m.selected = i
				break
			}
		}
	}
	m.clamp()
}

// expireNotice clears a notice that has stayed unchanged for about five
// seconds. Comparing against the last seen text avoids touching every site
// that sets a notice.
func (m *model) expireNotice() {
	if m.notice == "" {
		m.noticeTicks, m.noticed = 0, ""
		return
	}
	if m.notice != m.noticed {
		m.noticed, m.noticeTicks = m.notice, 1
		return
	}
	m.noticeTicks++
	if m.noticeTicks >= 5 {
		m.notice, m.noticed, m.noticeTicks = "", "", 0
	}
}

func startTaskCmd(service Service, id string) tea.Cmd {
	return func() tea.Msg {
		if err := service.Start(context.Background(), id); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{notice: "Execution requested; queued tasks start when capacity frees."}
	}
}

func retryTaskCmd(service Service, id string) tea.Cmd {
	return func() tea.Msg {
		if err := service.Retry(id); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{notice: "Task queued for retry; press r to start."}
	}
}

func cancelTaskCmd(service Service, id string) tea.Cmd {
	return func() tea.Msg {
		if err := service.Cancel(id); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{notice: "Cancellation requested."}
	}
}

func approveReviewCmd(service Service, id, commit string) tea.Cmd {
	return func() tea.Msg {
		if err := service.ApproveReview(id, commit, ""); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{notice: "Review approved for commit " + commit + "; merge scheduled."}
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
