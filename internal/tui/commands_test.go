package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// errTestCredential stands in for an orchestrator rejection during login.
var errTestCredential = errors.New("API key is required")

// credentialCall records one SetProviderCredential invocation so tests can
// assert the provider and the exact key the UI handed to the service.
type credentialCall struct {
	provider string
	key      string
}

// fakeLoginService is a credential-aware stand-in for the orchestrator. Only
// the credential and model methods carry behaviour; everything else returns
// zero values because the router never calls it while handling slash commands.
type fakeLoginService struct {
	creds         []orchestrator.ProviderCredential
	setCalls      []credentialCall
	removeCalls   []string
	setErr        error
	removeErr     error
	anyConfigured bool
	submitCalls   []string
	overrides     orchestrator.ModelSelection
	models        map[string][]string
	setModelCalls []string // recorded as "class=selector"
	setModelErr   error
	agentClasses  []orchestrator.AgentClass
	setClassCalls []string // recorded as "agent=class"
}

var _ Service = (*fakeLoginService)(nil)

func (f *fakeLoginService) ListTasks() []orchestrator.Task       { return nil }
func (f *fakeLoginService) ListAgents() []orchestrator.AgentInfo { return nil }
func (f *fakeLoginService) ListProfiles() []orchestrator.Profile { return nil }

// SessionUsage reports no recorded tokens: slash-command tests never run work.
func (f *fakeLoginService) SessionUsage() orchestrator.SessionUsage {
	return orchestrator.SessionUsage{}
}

func (f *fakeLoginService) SubmitRequest(text, _ string) (orchestrator.Task, error) {
	f.submitCalls = append(f.submitCalls, text)
	return orchestrator.Task{}, nil
}

func (f *fakeLoginService) Start(context.Context, string) error { return nil }
func (f *fakeLoginService) Retry(string) error                  { return nil }
func (f *fakeLoginService) Cancel(string) error                 { return nil }

func (f *fakeLoginService) ApproveReview(string, string, string) error { return nil }
func (f *fakeLoginService) RejectReview(string, string, string) error  { return nil }

func (f *fakeLoginService) FollowUp(string, string) (bool, error) { return false, nil }

func (f *fakeLoginService) CreateTask(string, string, string) (orchestrator.Task, error) {
	return orchestrator.Task{}, nil
}

func (f *fakeLoginService) CreateRelatedTask(string, string, string, string) (orchestrator.Task, error) {
	return orchestrator.Task{}, nil
}

func (f *fakeLoginService) SpawnSubagent(string, string, string, string) (orchestrator.Task, error) {
	return orchestrator.Task{}, nil
}

func (f *fakeLoginService) RecordReport(string, string, string) error   { return nil }
func (f *fakeLoginService) AddManualThreadContext(string, string) error { return nil }
func (f *fakeLoginService) SetContextSubscription(string, bool) error   { return nil }
func (f *fakeLoginService) SetTaskIsolation(string, bool) error         { return nil }
func (f *fakeLoginService) SetTaskAgent(string, string) error           { return nil }
func (f *fakeLoginService) SetTaskProfile(string, string) error         { return nil }

func (f *fakeLoginService) CompletedDeletionCandidates() []string { return nil }
func (f *fakeLoginService) WorktreeDiscardCandidates() []string   { return nil }
func (f *fakeLoginService) DeleteTask(string) error               { return nil }
func (f *fakeLoginService) DiscardWorktree(string) error          { return nil }

func (f *fakeLoginService) DeleteCompletedTasksDetailed([]string) (int, []orchestrator.SkippedDeletion, error) {
	return 0, nil, nil
}

func (f *fakeLoginService) DiscardMergedWorktrees([]string) (int, error) { return 0, nil }

// The fake never fails a save and has no startup warning to report.
func (f *fakeLoginService) PersistenceError() error { return nil }
func (f *fakeLoginService) StartupWarning() string  { return "" }

func (f *fakeLoginService) SetProviderCredential(provider, key string) error {
	f.setCalls = append(f.setCalls, credentialCall{provider: provider, key: key})
	if f.setErr != nil {
		return f.setErr
	}
	for i := range f.creds {
		if f.creds[i].Provider == provider {
			f.creds[i].Configured = true
		}
	}
	return nil
}

func (f *fakeLoginService) RemoveProviderCredential(provider string) error {
	f.removeCalls = append(f.removeCalls, provider)
	if f.removeErr != nil {
		return f.removeErr
	}
	for i := range f.creds {
		if f.creds[i].Provider == provider {
			f.creds[i].Configured = false
		}
	}
	return nil
}

func (f *fakeLoginService) ProviderCredentials() []orchestrator.ProviderCredential {
	return f.creds
}

func (f *fakeLoginService) AnyProviderConfigured() bool { return f.anyConfigured }

func (f *fakeLoginService) SetModel(class, model string) error {
	f.setModelCalls = append(f.setModelCalls, class+"="+model)
	if f.setModelErr != nil {
		return f.setModelErr
	}
	if class == orchestrator.ModelClassBig {
		f.overrides.Big = model
	} else {
		f.overrides.Small = model
	}
	return nil
}

func (f *fakeLoginService) ModelSelection() orchestrator.ModelSelection {
	return f.overrides
}

func (f *fakeLoginService) ModelsForProvider(provider string) []string {
	return f.models[provider]
}

func (f *fakeLoginService) FetchModels(_ context.Context, provider string) ([]string, error) {
	return f.models[provider], nil
}

func (f *fakeLoginService) SetAgentClass(agent, class string) error {
	f.setClassCalls = append(f.setClassCalls, agent+"="+class)
	for i := range f.agentClasses {
		if f.agentClasses[i].Agent == agent {
			f.agentClasses[i].Class = class
			f.agentClasses[i].Overridden = class != ""
			return nil
		}
	}
	f.agentClasses = append(f.agentClasses, orchestrator.AgentClass{Agent: agent, Class: class, Overridden: class != ""})
	return nil
}

func (f *fakeLoginService) AgentClasses() []orchestrator.AgentClass {
	return f.agentClasses
}

// typeSlash types a command in the router form and submits it.
func typeSlash(m model, command string) model {
	m = key(m, runeKey(command))
	return key(m, tea.KeyMsg{Type: tea.KeyEnter})
}

func TestSlashCommandsSetNotices(t *testing.T) {
	cases := []struct {
		name              string
		command           string
		creds             []orchestrator.ProviderCredential
		wantNotice        []string
		wantLoginProvider string
		wantLoginKey      string
		wantRemoved       []string
	}{
		{
			name:    "providers lists configured state",
			command: "/providers",
			creds: []orchestrator.ProviderCredential{
				{Provider: "openai", Configured: true},
				{Provider: "deepseek", Configured: false},
			},
			wantNotice: []string{"openai (configured)", "deepseek (no key)"},
		},
		{
			name:              "login starts key entry",
			command:           "/login openai",
			wantNotice:        []string{"Enter API key for openai", "Esc to cancel"},
			wantLoginProvider: "openai",
		},
		{
			name:        "logout removes the key",
			command:     "/logout openai",
			creds:       []orchestrator.ProviderCredential{{Provider: "openai", Configured: true}},
			wantNotice:  []string{"Removed API key for openai"},
			wantRemoved: []string{"openai"},
		},
		{
			name:       "unknown command lists the vocabulary",
			command:    "/foo",
			wantNotice: []string{"Unknown command", "/login <provider>"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeLoginService{creds: tc.creds}
			m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
			m = typeSlash(m, tc.command)
			for _, want := range tc.wantNotice {
				if !strings.Contains(m.notice.text, want) {
					t.Fatalf("notice %q missing %q", m.notice.text, want)
				}
			}
			if m.login.provider != tc.wantLoginProvider || m.login.key != tc.wantLoginKey {
				t.Fatalf("login state = %q/%q, want %q/%q", m.login.provider, m.login.key, tc.wantLoginProvider, tc.wantLoginKey)
			}
			if len(fake.setCalls) != 0 {
				t.Fatalf("slash command stored a credential: %+v", fake.setCalls)
			}
			if len(fake.removeCalls) != len(tc.wantRemoved) {
				t.Fatalf("remove calls = %v, want %v", fake.removeCalls, tc.wantRemoved)
			}
			for i, want := range tc.wantRemoved {
				if fake.removeCalls[i] != want {
					t.Fatalf("remove calls = %v, want %v", fake.removeCalls, tc.wantRemoved)
				}
			}
			if m.router.draft != "" || m.router.cursor != 0 {
				t.Fatalf("command left the draft behind: draft=%q cursor=%d", m.router.draft, m.router.cursor)
			}
			if !m.router.open {
				t.Fatal("slash command closed the router")
			}
		})
	}
}

// /help opens the real help overlay rather than printing a notice, and the
// commands an operator can type are documented inside the menu.
func TestSlashHelpOpensTheOverlay(t *testing.T) {
	fake := &fakeLoginService{}
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/help")
	if !m.help.open {
		t.Fatal("/help did not open the help overlay")
	}
	if !m.router.open {
		t.Fatal("/help closed the request form underneath")
	}
	for i, topic := range helpTopics {
		if topic.title != "Commands" {
			continue
		}
		m.help.page = i
	}
	plain := ansi.Strip(strings.Join(plainHelp(m, 70), "\n"))
	// The commands sit in the key column and their argument forms are documented
	// once, in the topic intro, so the table stays aligned.
	for _, want := range []string{"/login", "/logout", "/providers", "/model", "/agents", "/help", "/login <provider>", "/model [big|small]"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("help overlay missing %q: %q", want, plain)
		}
	}
}

func TestSlashProvidersWithoutService(t *testing.T) {
	m := model{router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/providers")
	if !strings.Contains(m.notice.text, "not connected") {
		t.Fatalf("notice %q did not report a missing service", m.notice.text)
	}
}

func TestLoginModeMasksAndSubmitsKey(t *testing.T) {
	const secret = "sk-secret"
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	if m.login.provider != "openai" || m.login.key != "" {
		t.Fatalf("login not armed: provider=%q key=%q", m.login.provider, m.login.key)
	}
	if prompt := ansi.Strip(m.View()); !strings.Contains(prompt, "Enter API key for openai: (hidden)") {
		t.Fatalf("masked prompt missing: %s", prompt)
	}

	m = key(m, runeKey(secret))
	if m.login.key != secret {
		t.Fatalf("typed key = %q, want %q", m.login.key, secret)
	}
	if m.router.draft != "" {
		t.Fatalf("typed key leaked into the draft: %q", m.router.draft)
	}
	if masked := ansi.Strip(m.View()); strings.Contains(masked, secret) || !strings.Contains(masked, strings.Repeat("*", len(secret))) {
		t.Fatalf("prompt did not mask the key: %s", masked)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.login.key != secret[:len(secret)-1] {
		t.Fatalf("backspace left %q, want %q", m.login.key, secret[:len(secret)-1])
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setCalls) != 1 {
		t.Fatalf("set calls = %+v, want exactly one", fake.setCalls)
	}
	if fake.setCalls[0].provider != "openai" || fake.setCalls[0].key != secret[:len(secret)-1] {
		t.Fatalf("stored %+v, want openai/%q", fake.setCalls[0], secret[:len(secret)-1])
	}
	if m.login.provider != "" || m.login.key != "" {
		t.Fatalf("login mode stayed armed: provider=%q key=%q", m.login.provider, m.login.key)
	}
	if !strings.Contains(m.notice.text, "Stored API key for openai") {
		t.Fatalf("stored notice missing: %q", m.notice.text)
	}
	if strings.Contains(m.notice.text, secret) {
		t.Fatalf("notice leaked the key: %q", m.notice.text)
	}
}

func TestLoginModeEscapeCancelsWithoutClosingRouter(t *testing.T) {
	fake := &fakeLoginService{}
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	m = key(m, runeKey("sk-secret"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.login.provider != "" || m.login.key != "" {
		t.Fatalf("escape did not clear login state: provider=%q key=%q", m.login.provider, m.login.key)
	}
	if !strings.Contains(m.notice.text, "Login canceled") {
		t.Fatalf("cancel notice missing: %q", m.notice.text)
	}
	if !m.router.open {
		t.Fatal("escape during login closed the router")
	}
	if len(fake.setCalls) != 0 {
		t.Fatalf("canceled login stored a key: %+v", fake.setCalls)
	}
}

func TestLoginModeReportsServiceError(t *testing.T) {
	fake := &fakeLoginService{setErr: errTestCredential}
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	m = key(m, runeKey("sk-secret"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice.text, errTestCredential.Error()) {
		t.Fatalf("error notice missing: %q", m.notice.text)
	}
	if m.login.provider != "" || m.login.key != "" {
		t.Fatalf("failed login stayed armed: provider=%q key=%q", m.login.provider, m.login.key)
	}
}

func TestLoginPromptFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}, {30, 10}} {
		m := model{router: routerState{open: true}, width: size[0],
			height: size[1], login: loginState{provider: "openai", key: strings.Repeat("k", 40)},
		}
		view := m.View()
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
		if plain := ansi.Strip(view); !strings.Contains(plain, "Enter API key") {
			t.Fatalf("%v: login prompt missing: %s", size, plain)
		}
	}
}

// configureTestProvider makes the login precondition satisfiable for tests that
// route requests through a real service without caring about credentials.
func configureTestProvider(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "sk-test-key")
}

func TestNewModelForcesLoginMenuWithoutProvider(t *testing.T) {
	unconfigured := newModel(&fakeLoginService{}, "")
	if !unconfigured.login.menu || unconfigured.router.open {
		t.Fatalf("login menu not forced: menu=%v router=%v", unconfigured.login.menu, unconfigured.router.open)
	}
	if !strings.Contains(unconfigured.notice.text, "No provider configured") {
		t.Fatalf("forced login notice missing: %q", unconfigured.notice.text)
	}
	unconfigured.width, unconfigured.height = 120, 35
	if plain := ansi.Strip(unconfigured.View()); !strings.Contains(plain, "LOGIN") || !strings.Contains(plain, "Select a provider") {
		t.Fatalf("forced login menu not rendered: %s", plain)
	}

	configured := newModel(&fakeLoginService{anyConfigured: true}, "")
	if configured.login.menu || !configured.router.open {
		t.Fatalf("configured start opened the login menu: menu=%v router=%v", configured.login.menu, configured.router.open)
	}
}

func TestSlashLoginWithoutProviderOpensMenu(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/login")
	if !m.login.menu || m.login.index != 0 {
		t.Fatalf("menu not opened: menu=%v index=%d", m.login.menu, m.login.index)
	}
	if m.router.open {
		t.Fatal("/login left the request form open behind the menu")
	}
	if !strings.Contains(m.notice.text, "Select a provider") {
		t.Fatalf("menu notice missing: %q", m.notice.text)
	}
	if m.login.provider != "" {
		t.Fatalf("menu armed key entry for %q", m.login.provider)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "LOGIN") || !strings.Contains(plain, "openai") {
		t.Fatalf("menu not rendered: %s", plain)
	}
}

func TestSlashLoginWithProviderEntersKeyEntry(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	if m.login.provider != "openai" || m.login.key != "" || m.login.menu {
		t.Fatalf("direct login not armed: provider=%q key=%q menu=%v", m.login.provider, m.login.key, m.login.menu)
	}
}

func TestProviderMenuKeySelectsProviderAndStartsKeyEntry(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{
		{Provider: "deepseek"},
		{Provider: "openai", Configured: true},
	}}
	m := model{service: fake, width: 120, height: 35, login: loginState{menu: true, index: 0}}
	m = key(m, runeKey("j"))
	if m.login.index != 1 {
		t.Fatalf("j did not move the selection: %d", m.login.index)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.login.index != 1 {
		t.Fatalf("down moved past the end: %d", m.login.index)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.login.index != 0 {
		t.Fatalf("up did not move the selection: %d", m.login.index)
	}
	m = key(m, runeKey("k"))
	if m.login.index != 0 {
		t.Fatalf("k moved above the first provider: %d", m.login.index)
	}
	m = key(m, runeKey("j"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.login.menu {
		t.Fatal("enter left the menu open")
	}
	if m.login.provider != "openai" || m.login.key != "" {
		t.Fatalf("enter did not arm key entry: provider=%q key=%q", m.login.provider, m.login.key)
	}
	if !strings.Contains(m.notice.text, "Enter API key for openai") {
		t.Fatalf("key-entry notice missing: %q", m.notice.text)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Enter API key for openai: (hidden)") {
		t.Fatalf("masked prompt missing: %s", plain)
	}
	m = key(m, runeKey("sk-live"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setCalls) != 1 || fake.setCalls[0].provider != "openai" || fake.setCalls[0].key != "sk-live" {
		t.Fatalf("selected provider did not store the key: %+v", fake.setCalls)
	}
}

func TestProviderMenuEscapeClosesWithoutQuitting(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, width: 120, height: 35, login: loginState{menu: true}}
	next, cmd := m.providerMenuKey("esc")
	if cmd != nil {
		t.Fatal("escape quit instead of closing the menu")
	}
	if next.login.menu {
		t.Fatal("escape left the menu open")
	}
	if !strings.Contains(next.notice.text, "No provider configured") {
		t.Fatalf("forced escape notice missing: %q", next.notice.text)
	}

	configured := model{service: &fakeLoginService{anyConfigured: true}, width: 120, height: 35, login: loginState{menu: true}}
	closed, _ := configured.providerMenuKey("esc")
	if closed.login.menu || strings.Contains(closed.notice.text, "No provider configured") {
		t.Fatalf("escape from a configured menu misreported state: %q", closed.notice.text)
	}
}

func TestProviderMenuCtrlCAsksBeforeQuitting(t *testing.T) {
	m := model{service: &fakeLoginService{}, width: 120, height: 35, login: loginState{menu: true}}
	next, cmd := m.providerMenuKey("ctrl+c")
	if cmd != nil {
		t.Fatal("ctrl+c quit the menu without confirmation")
	}
	if !next.quitConfirm {
		t.Fatal("ctrl+c did not ask for confirmation")
	}
	if !strings.Contains(ansi.Strip(next.View()), "Leave pilharmonic?") {
		t.Fatal("confirmation dialog is not on screen")
	}
}

func TestLoginMenuBoardKeyOpensMenu(t *testing.T) {
	m := model{width: 120, height: 35}
	m = key(m, runeKey("L"))
	if !m.login.menu {
		t.Fatal("L did not open the provider menu")
	}
	if m.quitConfirm {
		t.Fatal("L armed the quit confirmation")
	}
}

func TestLoginMenuViewListsProvidersAndHidesKeys(t *testing.T) {
	const secret = "sk-should-not-render"
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{
		{Provider: "deepseek"},
		{Provider: "openai", Configured: true},
	}}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}} {
		m := model{service: fake, width: size[0], height: size[1], login: loginState{menu: true, index: 1, key: secret}}
		view := m.View()
		plain := ansi.Strip(view)
		for _, want := range []string{"LOGIN", "Select a provider", "openai", "deepseek", "configured", "no key", "j/k select"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("%v: menu missing %q: %s", size, want, plain)
			}
		}
		if strings.Contains(plain, secret) {
			t.Fatalf("%v: menu rendered the API key: %s", size, plain)
		}
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
	}
}

func TestLoginMenuWithoutServiceStillRenders(t *testing.T) {
	m := model{width: 120, height: 35, login: loginState{menu: true}}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "LOGIN") || !strings.Contains(plain, "No providers known.") {
		t.Fatalf("serviceless menu missing content: %s", plain)
	}
}

func TestRouterSubmitForcesLoginWithoutProvider(t *testing.T) {
	fake := &fakeLoginService{}
	m := model{service: fake, router: routerState{open: true, draft: "write tests", cursor: len("write tests")}, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.login.menu || m.login.index != 0 {
		t.Fatalf("submit without a provider did not open the menu: menu=%v index=%d", m.login.menu, m.login.index)
	}
	if !strings.Contains(m.notice.text, "No provider configured") {
		t.Fatalf("forced login notice missing: %q", m.notice.text)
	}
	if m.router.draft != "write tests" {
		t.Fatalf("blocked submit discarded the draft: %q", m.router.draft)
	}
	if len(fake.submitCalls) != 0 {
		t.Fatalf("blocked submit reached the service: %+v", fake.submitCalls)
	}

	configured := &fakeLoginService{anyConfigured: true}
	m = model{service: configured, router: routerState{open: true, draft: "write tests", cursor: len("write tests")}, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.login.menu {
		t.Fatal("submit re-opened the menu with a provider configured")
	}
	if len(configured.submitCalls) != 1 || configured.submitCalls[0] != "write tests" {
		t.Fatalf("request was not routed: %+v", configured.submitCalls)
	}
}

// modelMenuService is a fake with two agents, a curated catalog and both a
// configured and an unconfigured provider, so the menu can be exercised
// end to end without touching a real service.
func modelMenuService() *fakeLoginService {
	return &fakeLoginService{
		creds: []orchestrator.ProviderCredential{
			{Provider: "deepseek"},
			{Provider: "openai", Configured: true},
		},
		overrides: orchestrator.ModelSelection{},
		models:    map[string][]string{"openai": {"gpt-5", "gpt-5-mini"}},
	}
}

func TestSlashModelOpensAgentStep(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/model")
	if !m.models.menu || m.models.step != modelStepClass || m.models.index != 0 {
		t.Fatalf("menu not opened at the class step: menu=%v step=%d index=%d", m.models.menu, m.models.step, m.models.index)
	}
	if m.models.class != "" || m.models.provider != "" {
		t.Fatalf("menu opened with stale choices: class=%q provider=%q", m.models.class, m.models.provider)
	}
	if m.router.open {
		t.Fatal("/model left the request form open behind the menu")
	}
	if !strings.Contains(m.notice.text, "Select a model class") {
		t.Fatalf("menu notice missing: %q", m.notice.text)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "MODEL") || !strings.Contains(plain, "big") {
		t.Fatalf("menu not rendered: %s", plain)
	}
	if len(fake.setModelCalls) != 0 {
		t.Fatalf("opening the menu pinned a model: %+v", fake.setModelCalls)
	}
}

func TestSlashModelWithAgentStartsAtProviderStep(t *testing.T) {
	m := model{service: modelMenuService(), router: routerState{open: true}, width: 120, height: 35}
	m = typeSlash(m, "/model small")
	if !m.models.menu || m.models.step != modelStepProvider || m.models.class != "small" {
		t.Fatalf("named class did not skip the class step: menu=%v step=%d class=%q", m.models.menu, m.models.step, m.models.class)
	}

	unknown := model{service: modelMenuService(), router: routerState{open: true}, width: 120, height: 35}
	unknown = typeSlash(unknown, "/model ghost")
	if unknown.models.menu {
		t.Fatal("unknown agent opened the menu")
	}
	if !strings.Contains(unknown.notice.text, "Unknown class") {
		t.Fatalf("unknown agent notice missing: %q", unknown.notice.text)
	}
}

func TestModelMenuWalksAgentProviderModel(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepClass}}
	m = key(m, runeKey("j"))
	if m.models.index != 1 {
		t.Fatalf("j did not move the selection: %d", m.models.index)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.models.index != 1 {
		t.Fatalf("down moved past the last agent: %d", m.models.index)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.models.index != 0 {
		t.Fatalf("up did not move the selection: %d", m.models.index)
	}
	m = key(m, runeKey("k"))
	if m.models.index != 0 {
		t.Fatalf("k moved above the first agent: %d", m.models.index)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.models.step != modelStepProvider || m.models.class != "big" || m.models.index != 0 {
		t.Fatalf("class step did not advance: step=%d class=%q index=%d", m.models.step, m.models.class, m.models.index)
	}
	if choices := m.modelChoices(); len(choices) != 1 || choices[0] != "openai" {
		t.Fatalf("provider step offered %v, want only the configured provider", choices)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.models.step != modelStepModel || m.models.provider != "openai" || m.models.index != 0 {
		t.Fatalf("provider step did not advance: step=%d provider=%q index=%d", m.models.step, m.models.provider, m.models.index)
	}
	m = key(m, runeKey("j"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 1 || fake.setModelCalls[0] != "big=openai/gpt-5-mini" {
		t.Fatalf("model step pinned %v, want big=openai/gpt-5-mini", fake.setModelCalls)
	}
	if m.models.menu {
		t.Fatal("selecting a model left the menu open")
	}
	if !strings.Contains(m.notice.text, "Model for big: openai/gpt-5-mini") {
		t.Fatalf("confirmation notice missing: %q", m.notice.text)
	}
}

func TestModelMenuReportsServiceError(t *testing.T) {
	fake := modelMenuService()
	fake.setModelErr = errors.New("\"big\" is not a known agent")
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice.text, "not a known agent") {
		t.Fatalf("error notice missing: %q", m.notice.text)
	}
	if m.models.menu {
		t.Fatal("a failed selection left the menu open")
	}
}

func TestModelMenuEscapeStepsBackAndCloses(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai", index: 1}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.models.step != modelStepProvider || m.models.index != 0 {
		t.Fatalf("escape did not return to the provider step: step=%d index=%d", m.models.step, m.models.index)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.models.step != modelStepClass {
		t.Fatalf("escape did not return to the class step: step=%d", m.models.step)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.models.menu {
		t.Fatal("escape on the class step left the menu open")
	}
	if !strings.Contains(m.notice.text, "Model menu closed") {
		t.Fatalf("close notice missing: %q", m.notice.text)
	}
	if len(fake.setModelCalls) != 0 {
		t.Fatalf("an abandoned menu pinned a model: %+v", fake.setModelCalls)
	}
}

func TestModelMenuCtrlCAsksBeforeQuitting(t *testing.T) {
	m := model{service: modelMenuService(), width: 120, height: 35, models: modelMenuState{menu: true}}
	next, cmd := m.modelMenuKey("ctrl+c")
	if cmd != nil {
		t.Fatal("ctrl+c quit the menu without confirmation")
	}
	if !next.quitConfirm {
		t.Fatal("ctrl+c did not ask for confirmation")
	}
	if !strings.Contains(ansi.Strip(next.View()), "Leave pilharmonic?") {
		t.Fatal("confirmation dialog is not on screen")
	}
}

func TestModelMenuBoardKeyOpensMenu(t *testing.T) {
	m := model{service: modelMenuService(), width: 120, height: 35}
	m = key(m, runeKey("m"))
	if !m.models.menu || m.models.step != modelStepClass || m.models.index != 0 {
		t.Fatalf("m did not open the model menu: menu=%v step=%d index=%d", m.models.menu, m.models.step, m.models.index)
	}
	if m.quitConfirm {
		t.Fatal("m armed the quit confirmation")
	}
}

func TestModelMenuViewListsChoicesAndHidesKeys(t *testing.T) {
	const secret = "sk-should-not-render"
	fake := modelMenuService()

	agents := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true}, login: loginState{key: secret}}
	plain := ansi.Strip(agents.View())
	for _, want := range []string{"MODEL", "Select a model class", "big", "small", "default", "j/k select"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("class step missing %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, secret) {
		t.Fatalf("class step rendered an API key: %s", plain)
	}

	providers := agents
	providers.models.step = modelStepProvider
	providers.models.class = "big"
	plain = ansi.Strip(providers.View())
	for _, want := range []string{"Select a provider for big", "openai"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("provider step missing %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "deepseek") {
		t.Fatalf("provider step offered an unconfigured provider: %s", plain)
	}

	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}} {
		m := model{service: fake, width: size[0], height: size[1], login: loginState{key: secret}, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
		view := m.View()
		plain := ansi.Strip(view)
		wants := []string{"MODEL", "Select a model (openai)", "gpt-5", "gpt-5-mini", customModelChoice, "j/k select"}
		// The footer hint is clipped on a narrow modal, so only require it
		// where it fits.
		if size[0] >= 80 {
			wants = append(wants, "esc back")
		}
		for _, want := range wants {
			if !strings.Contains(plain, want) {
				t.Fatalf("%v: model step missing %q: %s", size, want, plain)
			}
		}
		if strings.Contains(plain, secret) {
			t.Fatalf("%v: model step rendered an API key: %s", size, plain)
		}
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
	}
}

func TestModelMenuCustomEntryPinsComposedSelector(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
	for i := 0; i < len(m.modelChoices())-1; i++ {
		m = key(m, runeKey("j"))
	}
	if m.modelChoices()[m.models.index] != customModelChoice {
		t.Fatalf("selection %d is %q, want the custom row", m.models.index, m.modelChoices()[m.models.index])
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.models.step != modelStepCustom || m.models.customInput != "" {
		t.Fatalf("custom row did not open the text step: step=%d input=%q", m.models.step, m.models.customInput)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Enter a model ID (openai)") || !strings.Contains(plain, "enter save") {
		t.Fatalf("custom prompt not rendered: %s", plain)
	}

	back := key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if back.models.step != modelStepModel || back.models.customInput != "" {
		t.Fatalf("escape did not return to the model step: step=%d input=%q", back.models.step, back.models.customInput)
	}
	if len(fake.setModelCalls) != 0 {
		t.Fatalf("escape pinned a model: %+v", fake.setModelCalls)
	}

	m = key(m, runeKey("my-model-9"))
	if m.models.customInput != "my-model-9" {
		t.Fatalf("typed model = %q, want my-model-9", m.models.customInput)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.models.customInput != "my-model-" {
		t.Fatalf("backspace left %q", m.models.customInput)
	}
	m = key(m, runeKey("9"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 1 || fake.setModelCalls[0] != "big=openai/my-model-9" {
		t.Fatalf("custom model pinned %v, want big=openai/my-model-9", fake.setModelCalls)
	}
	if m.models.menu {
		t.Fatal("saving a custom model left the menu open")
	}
	if !strings.Contains(m.notice.text, "Model for big: openai/my-model-9") {
		t.Fatalf("confirmation notice missing: %q", m.notice.text)
	}
}

func TestModelMenuWithoutServiceRendersAndReports(t *testing.T) {
	m := model{width: 120, height: 35, models: modelMenuState{menu: true}}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"MODEL", "Select a model class", "big", "small"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("serviceless menu missing %q: %s", want, plain)
		}
	}
	next, _ := m.modelMenuKey("enter")
	if next.models.step != modelStepProvider {
		t.Fatalf("enter on the class step did not advance to provider: step=%d", next.models.step)
	}
	next, _ = next.modelMenuKey("enter")
	if !next.models.menu {
		t.Fatal("enter on an empty provider list closed the menu")
	}
	if !strings.Contains(next.notice.text, "Nothing to choose") {
		t.Fatalf("empty step notice missing: %q", next.notice.text)
	}
}

// deepSeekMenuService configures only DeepSeek so the model step lists its
// curated catalog.
func deepSeekMenuService() *fakeLoginService {
	return &fakeLoginService{
		creds:  []orchestrator.ProviderCredential{{Provider: "deepseek", Configured: true}},
		models: map[string][]string{"deepseek": {"deepseek-chat", "deepseek-reasoner"}},
	}
}

// modelRateLabel names a window only for a model the price table says is
// charged differently at different hours, and reads the window from the
// orchestrator rather than deciding it here.
func TestModelRateLabelOnlyNamesTimeOfDayModels(t *testing.T) {
	peak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 20, 0, 0, 0, time.UTC)
	if got := modelRateLabel("deepseek/deepseek-chat", peak); got != orchestrator.RatePeak {
		t.Fatalf("peak label = %q, want %q", got, orchestrator.RatePeak)
	}
	if got := modelRateLabel("deepseek/deepseek-reasoner", offPeak); got != orchestrator.RateOffPeak {
		t.Fatalf("off-peak label = %q, want %q", got, orchestrator.RateOffPeak)
	}
	for _, selector := range []string{"openai/gpt-5", "deepseek/deepseek-unlisted", "custom/model"} {
		if got := modelRateLabel(selector, offPeak); got != "" {
			t.Errorf("%s named the window %q without a time-of-day price", selector, got)
		}
	}
}

// Every DeepSeek row in the model step names the pricing window in force, so
// the operator can see whether the choice is at list price or discounted.
func TestModelMenuLabelsDeepSeekRateWindow(t *testing.T) {
	fake := deepSeekMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "deepseek"}}
	now := time.Now()
	label := modelRateLabel("deepseek/deepseek-chat", now)
	if label == "" {
		t.Fatalf("deepseek-chat has no pricing window")
	}
	if after := modelRateLabel("deepseek/deepseek-chat", time.Now()); after != label {
		t.Skipf("pricing window closed while rendering: %s then %s", label, after)
	}
	plain := ansi.Strip(m.View())
	if got := strings.Count(plain, label); got != 2 {
		t.Fatalf("deepseek rows named the %q window %d times, want both rows: %s", label, got, plain)
	}
	for _, want := range []string{"deepseek-chat", "deepseek-reasoner", customModelChoice} {
		if !strings.Contains(plain, want) {
			t.Fatalf("model step missing %q: %s", want, plain)
		}
	}
}

// An all-day provider row carries no window label, so peak or off-peak is never
// claimed for a model whose price does not vary by hour.
func TestModelMenuOmitsWindowLabelForAllDayProvider(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
	plain := ansi.Strip(m.View())
	for _, unwanted := range []string{orchestrator.RatePeak, orchestrator.RateOffPeak} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("openai model step named %q: %s", unwanted, plain)
		}
	}
}

// Pinning a time-of-day model confirms the rate in force, because that is what
// the next attempt will be charged.
func TestModelSelectionNoticeNamesDeepSeekWindow(t *testing.T) {
	fake := deepSeekMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "deepseek"}}
	now := time.Now()
	label := modelRateLabel("deepseek/deepseek-chat", now)
	if after := modelRateLabel("deepseek/deepseek-chat", time.Now()); after != label {
		t.Skipf("pricing window closed while selecting: %s then %s", label, after)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 1 || fake.setModelCalls[0] != "big=deepseek/deepseek-chat" {
		t.Fatalf("model step pinned %v, want big=deepseek/deepseek-chat", fake.setModelCalls)
	}
	if want := "Model for big: deepseek/deepseek-chat · " + label + " now"; !strings.Contains(m.notice.text, want) {
		t.Fatalf("confirmation notice missing %q: %q", want, m.notice.text)
	}

	// A model with no window, or one the price table does not know, keeps the
	// plain confirmation.
	plain := model{service: modelMenuService(), width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
	plain = key(plain, tea.KeyMsg{Type: tea.KeyEnter})
	if plain.notice.text != "Model for big: openai/gpt-5" {
		t.Fatalf("all-day confirmation = %q, want no window", plain.notice.text)
	}
}

// The class step names the window of the model already pinned, and leaves a
// class that falls back to the profile untouched.
func TestModelMenuClassStepLabelsPinnedDeepSeekWindow(t *testing.T) {
	fake := deepSeekMenuService()
	fake.overrides = orchestrator.ModelSelection{Big: "deepseek/deepseek-chat"}
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepClass}}
	now := time.Now()
	label := modelRateLabel("deepseek/deepseek-chat", now)
	if after := modelRateLabel("deepseek/deepseek-chat", time.Now()); after != label {
		t.Skipf("pricing window closed while rendering: %s then %s", label, after)
	}
	view := m.View()
	plain := ansi.Strip(view)
	if want := "deepseek/deepseek-chat · " + label + " now"; !strings.Contains(plain, want) {
		t.Fatalf("class step missing %q: %s", want, plain)
	}
	if got := strings.Count(plain, label); got != 1 {
		t.Fatalf("class step named the window %d times, want only the pinned class: %s", got, plain)
	}
	if width, height := lipgloss.Width(view), lipgloss.Height(view); width > 120 || height > 35 {
		t.Fatalf("rendered %dx%d", width, height)
	}
}

// The window label gives way rather than overflowing a narrow modal, and the
// rows stay within the terminal.
func TestModelMenuRateLabelFitsNarrowTerminal(t *testing.T) {
	fake := deepSeekMenuService()
	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}} {
		m := model{service: fake, width: size[0], height: size[1], models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "deepseek"}}
		view := m.View()
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
		if plain := ansi.Strip(view); !strings.Contains(plain, "deepseek-chat") {
			t.Fatalf("%v: model row dropped: %s", size, plain)
		}
	}
}
