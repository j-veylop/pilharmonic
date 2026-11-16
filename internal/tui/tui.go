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
	"pilharmonic/internal/tui/editor"
)

const runningGlyphPlaceholder = "\uE000"

var defaultRunningGlyph = spinner.MiniDot.Frames[0]

type tickMsg time.Time

// actionResultMsg reports a mutating service call by its notice or error.
type actionResultMsg struct {
	notice string
	err    error
}

// composerResultMsg reports a composer submission. The draft is retained when
// err is set so the operator can correct it.
type composerResultMsg actionResultMsg

type isolationResultMsg actionResultMsg

// routerResultMsg reports a submitted request. A task with an empty ID means
// nothing was accepted and the draft is still the operator's only copy.
type routerResultMsg struct {
	task   orchestrator.Task
	notice string
	err    error
}

type fetchModelsMsg struct {
	provider string
	models   []string
	err      error
}

// Service is the orchestrator surface the terminal drives. The board reads
// snapshots, usage and standing conditions on its one refresh tick; slow calls
// run as commands outside the update loop.
type Service interface {
	ListTasks() []orchestrator.Task
	SessionUsage() orchestrator.SessionUsage
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
	PersistenceError() error
	StartupWarning() string
}

var _ Service = (*orchestrator.Service)(nil)

type model struct {
	pendingCmd    tea.Cmd
	activity      activityState
	thinking      bool
	vimG          bool
	vimCount      int
	setup         setupState
	agentPicker   pickerState[orchestrator.AgentInfo]
	profilePicker pickerState[orchestrator.Profile]
	service       Service
	workspace     string
	tasks         []orchestrator.Task
	usage         orchestrator.SessionUsage
	selected      int
	router        routerState
	width, height int
	input         composer
	historyOffset int
	notice        noticeState
	tab           detailTab
	detailFocus   bool
	fullscreen    bool
	sidebarHidden bool
	icons         bool
	theme         string
	frame         int
	attachments   []string
	help          helpState
	isolationBusy bool
	cleanup       cleanupRequest
	review        reviewPrompt
	cleanupBusy   bool
	selection     textSelection
	clipboard     io.Writer
	spinner       spinner.Model
	spinnerActive bool
	markdown      *markdownCache
	quitConfirm   bool
	quitChoice    int
	login         loginState
	models        modelMenuState
}

// detailTab is the detail pane's OUTPUT, INFO or ACTIVITY view.
type detailTab int

const (
	outputTab detailTab = iota
	infoTab
	activityTab
)

var detailTabNames = []string{"OUTPUT", "INFO", "ACTIVITY"}

type activityState struct {
	filter   activityFilter
	id       string
	attempt  string
	expanded bool
}

// routerState is the request box. Its draft survives esc.
type routerState struct {
	editor.Editor
	open bool
}

// noticeState is the notice line above the footer. text expires; service is a
// standing condition such as a failed save, replaced on every refresh.
type noticeState struct {
	text    string
	ticks   int
	shown   string
	service string
}

type helpState struct {
	open   bool
	page   int
	scroll int
}

// loginState is the provider menu and the API-key prompt, armed while
// provider is set. The key is never rendered.
type loginState struct {
	provider string
	key      string
	menu     bool
	index    int
}

type modelMenuState struct {
	menu         bool
	step         modelStep
	index        int
	class        string
	provider     string
	customInput  string
	list         []string // fetched models for listProvider
	listProvider string
	loading      bool
	fetchFailed  bool // the built-in list is shown instead
}

// Options are presentation choices fixed at startup.
type Options struct {
	Icons bool
	Theme string
}

// Run starts the terminal interface. The service owns task execution.
func Run(service *orchestrator.Service, workspace string, opts Options) error {
	initial := newModel(service, workspace)
	initial.icons = opts.Icons
	initial.theme = ApplyTheme(opts.Theme)
	output := newTerminalOutput(os.Stdout)
	initial.clipboard = output
	_, err := tea.NewProgram(initial, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(output)).Run()
	return err
}

// newModel builds the starting model. Without any usable provider the operator
// lands on the login menu instead of the request box, because no request can
// be routed until a credential exists.
func newModel(service Service, workspace string) model {
	m := model{
		service:   service,
		workspace: workspace,
		tasks:     service.ListTasks(),
		usage:     service.SessionUsage(),
		router:    routerState{open: true},
		spinner:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		markdown:  &markdownCache{},
	}
	m.notice.service = serviceNotice(service)
	if !service.AnyProviderConfigured() {
		m.login = loginState{menu: true}
		m.router.open = false
		m.notice.text = forcedLoginNotice
	}
	return m
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

// serviceNotice reports a failed background save before a startup warning.
func serviceNotice(service Service) string {
	if err := service.PersistenceError(); err != nil {
		return "State save failed: " + err.Error()
	}
	return service.StartupWarning()
}

func (m model) Init() tea.Cmd {
	if m.hasLiveWork() {
		return tea.Batch(tick(), m.spinner.Tick)
	}
	return tick()
}

// hasLiveWork reports whether any task can change without operator input; the
// spinner only runs then.
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
	case subscriptionLoginMsg:
		m.notice.text = "Codex login exited, but no usable ChatGPT subscription credentials were found · retry the app login with /login openai subscription (uses app-specific auth storage)"
		if msg.err != nil {
			m.notice.text = "ChatGPT subscription login failed: " + msg.err.Error() + " · retry /login openai subscription"
		} else {
			for _, credential := range m.service.ProviderCredentials() {
				if credential.Provider == "openai-subscription" && credential.Configured {
					m.notice.text = "ChatGPT subscription login complete · use /model big and choose openai-subscription"
					break
				}
			}
		}
	case actionResultMsg:
		m.finish(msg)
	case composerResultMsg:
		if msg.err == nil {
			m.input = composer{}
		}
		m.finish(actionResultMsg(msg))
	case isolationResultMsg:
		m.isolationBusy = false
		m.finish(actionResultMsg(msg))
	case cleanupResultMsg:
		m.cleanupBusy = false
		m.finish(actionResultMsg{notice: string(msg)})
		m.selection = textSelection{}
	case routerResultMsg:
		m.routerResult(msg)
	case fetchModelsMsg:
		m.fetchedModels(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		m.cancelStaleSelection()
	case tickMsg:
		m.frame++
		return m, m.tick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		m.frame++
		m.spinnerActive = m.hasLiveWork()
		if !m.spinnerActive {
			return m, nil
		}
		return m, cmd
	case selectionCopiedMsg:
		if msg.err != nil {
			m.notice.text = "Could not copy selection: " + msg.err.Error()
		} else if msg.lines > 0 {
			m.notice.text = fmt.Sprintf("Copied %d line(s) to clipboard via OSC 52.", msg.lines)
		}
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		return m.keyMsg(msg)
	}
	return m, nil
}

func (m *model) finish(result actionResultMsg) {
	m.notice.text = result.text()
	m.refreshTasks()
	m.clampScroll()
}

// routerResult selects an accepted request's task. A task with an empty ID
// means nothing was accepted, so the draft is kept.
func (m *model) routerResult(msg routerResultMsg) {
	m.notice.text = resultFor(msg.notice, msg.err).text()
	if msg.task.ID == "" {
		return
	}
	m.refreshTasks()
	m.router = routerState{open: m.router.open}
	m.attachments = nil
	m.selectTask(msg.task.ID)
	m.clampScroll()
}

func (m *model) tick() tea.Cmd {
	oldID, oldLines := m.selectedID(), m.detailLineCount()
	m.refreshTasks()
	if oldID != m.selectedID() {
		m.resetActivitySelection()
		m.historyOffset = 0
		m.selection = textSelection{}
	} else if m.historyOffset > 0 {
		m.historyOffset += m.detailLineCount() - oldLines
	}
	m.clampScroll()
	m.cancelStaleSelection()
	m.expireNotice()
	if m.hasLiveWork() && !m.spinnerActive {
		m.spinnerActive = true
		return tea.Batch(tick(), m.spinner.Tick)
	}
	return tick()
}

// keyMsg routes a key to the surface that owns the keyboard, topmost first.
func (m model) keyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.selection = textSelection{}
	key := msg.String()
	switch {
	case m.quitConfirm:
		return m.quitKey(msg)
	case m.help.open:
		return m.helpKey(key), nil
	case m.helpShortcut(msg):
		return m.openHelp(), nil
	// Login owns every key while it is armed, so a secret never reaches a draft.
	case m.login.provider != "":
		return m.loginKeyMsg(msg), nil
	case m.login.menu:
		m = m.providerMenuKey(key)
		cmd := m.takePendingCommand()
		return m, cmd
	case m.models.menu:
		return m.modelMenuKey(key)
	case m.setup.target != "":
		return m.setupKey(key)
	case m.router.open:
		return m.routerKey(msg)
	case m.cleanup.kind != "":
		return m.cleanupKey(key)
	case m.review.target != "":
		return m.reviewKey(key)
	case m.input.kind != "":
		return m.composerKey(msg)
	case m.activityKey(key):
		return m, nil
	}
	oldID := m.selectedID()
	var cmd tea.Cmd
	if !m.vimNavigation(key) {
		m, cmd = m.boardKey(key)
	}
	if oldID != m.selectedID() {
		m.resetActivitySelection()
	}
	return m, cmd
}

func (m *model) clamp() { m.selected = max(0, min(m.selected, len(m.tasks)-1)) }

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
	m.usage = m.service.SessionUsage()
	m.notice.service = serviceNotice(m.service)
	m.selectTask(selected)
}

// expireNotice clears a notice that has stayed unchanged for about five ticks.
func (m *model) expireNotice() {
	if m.notice.text == "" {
		m.notice.ticks, m.notice.shown = 0, ""
		return
	}
	if m.notice.text != m.notice.shown {
		m.notice.shown, m.notice.ticks = m.notice.text, 1
		return
	}
	m.notice.ticks++
	if m.notice.ticks >= 5 {
		m.notice.text, m.notice.shown, m.notice.ticks = "", "", 0
	}
}

// selectTask moves the board selection to the task with id, if it exists.
func (m *model) selectTask(id string) {
	for i, task := range m.tasks {
		if id != "" && task.ID == id {
			m.selected = i
			break
		}
	}
	m.clamp()
}

func (m model) selectedID() string {
	t, _ := m.selectedTask()
	return t.ID
}

func (m *model) clampScroll() { m.historyOffset = max(0, min(m.historyOffset, m.maxScroll())) }

func (m *model) scrollDetail(delta int) { m.historyOffset += delta; m.clampScroll() }
