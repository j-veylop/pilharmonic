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
	"pilharmonic/internal/tui/anim"
	"pilharmonic/internal/tui/editor"
)

const runningGlyphPlaceholder = "\uE000"

var defaultRunningGlyph = spinner.MiniDot.Frames[0]

type tickMsg time.Time

// animTickMsg drives the shared animation clock at Crush's 20 fps.
type animTickMsg struct{}

// reviewDiffMsg reports a loaded review patch.
type reviewDiffMsg struct {
	target, commit, title, diff string
	err                         error
}

// diffStatEntry caches one worktree's line churn. key ties the value to the
// commit it describes so a new attempt never shows stale numbers.
type diffStatEntry struct {
	key     string
	added   int
	removed int
	at      time.Time
	failed  bool
}

type diffStatMsg struct {
	id, key        string
	added, removed int
	err            error
}

type skillToggleMsg struct {
	name    string
	enabled bool
	err     error
}

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
	ListSkills() []orchestrator.SkillInfo
	ListAgentSkills(agent string) []orchestrator.SkillInfo
	SetAgentSkillEnabled(agent, name string, enabled bool) error
	SetSkillEnabled(name string, enabled bool) error
	SubmitRequest(text, workspace string) (orchestrator.Task, error)
	Start(ctx context.Context, id string) error
	Retry(id string) error
	Cancel(id string) error
	ApproveReview(id, commit, note string) error
	RejectReview(id, commit, note string) error
	ReviewDiff(ctx context.Context, id, commit string) (string, error)
	TaskDiffStat(ctx context.Context, id string) (added, removed int, err error)
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
	SetAgentModel(agent, model string) error
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
	diffStats     map[string]diffStatEntry
	doneExpanded  bool
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
	attachments   []string
	help          helpState
	isolationBusy bool
	cleanup       cleanupRequest
	review        reviewPrompt
	reviewDiff    reviewDiffState
	cleanupBusy   bool
	selection     textSelection
	clipboard     io.Writer
	spinner       spinner.Model
	spinnerActive bool
	runningFrame  int
	chatAnim      *anim.Anim
	markdown      *markdownCache
	quitConfirm   bool
	quitChoice    int
	login         loginState
	models        modelMenuState
	skills        skillMenuState
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
	agent        string
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
		chatAnim:  newChatAnim(),
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

func animTick() tea.Cmd {
	return tea.Tick(anim.FrameInterval(), func(time.Time) tea.Msg { return animTickMsg{} })
}

// newChatAnim is the multi-character turn spinner Crush shows in the
// transcript while an assistant message is being produced.
func newChatAnim() *anim.Anim {
	return anim.New(anim.Settings{
		ID:          "assistant",
		Size:        15,
		GradColorA:  accent,
		GradColorB:  cyan,
		CycleColors: true,
		SuffixColor: muted,
	})
}

// setAnimColors re-renders the chat animation after a theme change.
func (m *model) setAnimColors() {
	if m.chatAnim != nil {
		m.chatAnim.SetColors(ink, accent, cyan, muted)
	}
}

// serviceNotice reports a failed background save before a startup warning.
func serviceNotice(service Service) string {
	if err := service.PersistenceError(); err != nil {
		return "State save failed: " + err.Error()
	}
	return service.StartupWarning()
}

func (m model) Init() tea.Cmd {
	if m.hasLiveWork() {
		return tea.Batch(tick(), animTick())
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
	case externalEditorMsg:
		m.finishExternalEditor(msg)
	case reviewDiffMsg:
		m = m.acceptReviewDiff(msg)
	case diffStatMsg:
		if m.diffStats == nil {
			m.diffStats = map[string]diffStatEntry{}
		}
		m.diffStats[msg.id] = diffStatEntry{key: msg.key, added: msg.added, removed: msg.removed, at: time.Now(), failed: msg.err != nil}
	case skillToggleMsg:
		m.finishSkillToggle(msg)
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
		return m, m.tick()
	case animTickMsg:
		m.runningFrame = (m.runningFrame + 1) % len(spinner.MiniDot.Frames)
		if m.chatAnim != nil {
			m.chatAnim.Advance()
			m.chatAnim.SetSuffixText(m.selectedTurnElapsed())
		}
		m.spinnerActive = m.hasLiveWork()
		if !m.spinnerActive {
			if m.chatAnim != nil {
				m.chatAnim.SetSuffixText("")
			}
			return m, nil
		}
		return m, animTick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
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
	cmds := []tea.Cmd{tick()}
	if m.hasLiveWork() && !m.spinnerActive {
		m.spinnerActive = true
		cmds = append(cmds, animTick())
	}
	cmds = append(cmds, m.diffStatCommands(time.Now())...)
	return tea.Batch(cmds...)
}

// diffStatCommands refreshes the visible worktrees' line churn off the update
// loop. A static task is fetched once; a running one at most every few seconds.
func (m model) diffStatCommands(now time.Time) []tea.Cmd {
	if m.service == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, row := range m.mainRows() {
		task := row.task
		if !task.Isolated || task.ExecutionWorkspace == "" {
			continue
		}
		key := diffStatKey(task)
		entry, cached := m.diffStats[task.ID]
		if cached && entry.key == key && !(task.Status == orchestrator.Running && now.Sub(entry.at) >= 5*time.Second) {
			continue
		}
		service, id := m.service, task.ID
		cmds = append(cmds, func() tea.Msg {
			added, removed, err := service.TaskDiffStat(context.Background(), id)
			return diffStatMsg{id: id, key: key, added: added, removed: removed, err: err}
		})
	}
	return cmds
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
	case m.skills.open:
		return m.skillMenuKey(key)
	case m.setup.target != "":
		return m.setupKey(key)
	case m.router.open:
		return m.routerKey(msg)
	case m.cleanup.kind != "":
		return m.cleanupKey(key)
	case m.reviewDiff.target != "":
		return m.reviewDiffKey(msg)
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
	m.pruneDiffStats()
	m.selectTask(selected)
	m.ensureVisibleSelection()
}

// pruneDiffStats forgets worktrees of tasks that no longer exist.
func (m *model) pruneDiffStats() {
	if len(m.diffStats) == 0 {
		return
	}
	live := make(map[string]bool, len(m.tasks))
	for _, task := range m.tasks {
		live[task.ID] = true
	}
	for id := range m.diffStats {
		if !live[id] {
			delete(m.diffStats, id)
		}
	}
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

func (m model) selectedTaskOrZero() orchestrator.Task {
	t, _ := m.selectedTask()
	return t
}

func (m model) selectedID() string {
	t, _ := m.selectedTask()
	return t.ID
}

// selectedTurnElapsed is the age of the selected task's live attempt.
func (m model) selectedTurnElapsed() string {
	task, ok := m.selectedTask()
	if !ok || task.Status != orchestrator.Running || len(task.Attempts) == 0 {
		return ""
	}
	started := task.Attempts[len(task.Attempts)-1].StartedAt
	if started.IsZero() {
		return ""
	}
	return elapsedLabel(time.Since(started))
}

// elapsedLabel keeps the suffix short: seconds under a minute, then minutes.
func elapsedLabel(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func (m *model) clampScroll() { m.historyOffset = max(0, min(m.historyOffset, m.maxScroll())) }

func (m *model) scrollDetail(delta int) { m.historyOffset += delta; m.clampScroll() }
