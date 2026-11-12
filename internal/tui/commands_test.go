package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func (f *fakeLoginService) DeleteCompletedTasks([]string) (int, error) { return 0, nil }

func (f *fakeLoginService) DeleteCompletedTasksDetailed([]string) (int, []orchestrator.SkippedDeletion, error) {
	return 0, nil, nil
}

func (f *fakeLoginService) DiscardMergedWorktrees([]string) (int, error) { return 0, nil }

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
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(command)})
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
		{
			name:       "help lists the vocabulary",
			command:    "/help",
			wantNotice: []string{"/login — pick a provider", "/login <provider>", "/logout <provider>", "/providers", "/model [big|small]", "/help"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeLoginService{creds: tc.creds}
			m := model{service: fake, router: true, width: 120, height: 35}
			m = typeSlash(m, tc.command)
			for _, want := range tc.wantNotice {
				if !strings.Contains(m.notice, want) {
					t.Fatalf("notice %q missing %q", m.notice, want)
				}
			}
			if m.loginProvider != tc.wantLoginProvider || m.loginKey != tc.wantLoginKey {
				t.Fatalf("login state = %q/%q, want %q/%q", m.loginProvider, m.loginKey, tc.wantLoginProvider, tc.wantLoginKey)
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
			if m.routerDraft != "" || m.routerCursor != 0 {
				t.Fatalf("command left the draft behind: draft=%q cursor=%d", m.routerDraft, m.routerCursor)
			}
			if !m.router {
				t.Fatal("slash command closed the router")
			}
		})
	}
}

func TestSlashProvidersWithoutService(t *testing.T) {
	m := model{router: true, width: 120, height: 35}
	m = typeSlash(m, "/providers")
	if !strings.Contains(m.notice, "not connected") {
		t.Fatalf("notice %q did not report a missing service", m.notice)
	}
}

func TestLoginModeMasksAndSubmitsKey(t *testing.T) {
	const secret = "sk-secret"
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, router: true, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	if m.loginProvider != "openai" || m.loginKey != "" {
		t.Fatalf("login not armed: provider=%q key=%q", m.loginProvider, m.loginKey)
	}
	if prompt := ansi.Strip(m.View()); !strings.Contains(prompt, "Enter API key for openai: (hidden)") {
		t.Fatalf("masked prompt missing: %s", prompt)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(secret)})
	if m.loginKey != secret {
		t.Fatalf("typed key = %q, want %q", m.loginKey, secret)
	}
	if m.routerDraft != "" {
		t.Fatalf("typed key leaked into the draft: %q", m.routerDraft)
	}
	if masked := ansi.Strip(m.View()); strings.Contains(masked, secret) || !strings.Contains(masked, strings.Repeat("*", len(secret))) {
		t.Fatalf("prompt did not mask the key: %s", masked)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.loginKey != secret[:len(secret)-1] {
		t.Fatalf("backspace left %q, want %q", m.loginKey, secret[:len(secret)-1])
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setCalls) != 1 {
		t.Fatalf("set calls = %+v, want exactly one", fake.setCalls)
	}
	if fake.setCalls[0].provider != "openai" || fake.setCalls[0].key != secret[:len(secret)-1] {
		t.Fatalf("stored %+v, want openai/%q", fake.setCalls[0], secret[:len(secret)-1])
	}
	if m.loginProvider != "" || m.loginKey != "" {
		t.Fatalf("login mode stayed armed: provider=%q key=%q", m.loginProvider, m.loginKey)
	}
	if !strings.Contains(m.notice, "Stored API key for openai") {
		t.Fatalf("stored notice missing: %q", m.notice)
	}
	if strings.Contains(m.notice, secret) {
		t.Fatalf("notice leaked the key: %q", m.notice)
	}
}

func TestLoginModeEscapeCancelsWithoutClosingRouter(t *testing.T) {
	fake := &fakeLoginService{}
	m := model{service: fake, router: true, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-secret")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.loginProvider != "" || m.loginKey != "" {
		t.Fatalf("escape did not clear login state: provider=%q key=%q", m.loginProvider, m.loginKey)
	}
	if !strings.Contains(m.notice, "Login canceled") {
		t.Fatalf("cancel notice missing: %q", m.notice)
	}
	if !m.router {
		t.Fatal("escape during login closed the router")
	}
	if len(fake.setCalls) != 0 {
		t.Fatalf("canceled login stored a key: %+v", fake.setCalls)
	}
}

func TestLoginModeReportsServiceError(t *testing.T) {
	fake := &fakeLoginService{setErr: errTestCredential}
	m := model{service: fake, router: true, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-secret")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice, errTestCredential.Error()) {
		t.Fatalf("error notice missing: %q", m.notice)
	}
	if m.loginProvider != "" || m.loginKey != "" {
		t.Fatalf("failed login stayed armed: provider=%q key=%q", m.loginProvider, m.loginKey)
	}
}

func TestLoginPromptFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}, {30, 10}} {
		m := model{
			router:        true,
			width:         size[0],
			height:        size[1],
			loginProvider: "openai",
			loginKey:      strings.Repeat("k", 40),
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
	if !unconfigured.loginMenu || unconfigured.router {
		t.Fatalf("login menu not forced: menu=%v router=%v", unconfigured.loginMenu, unconfigured.router)
	}
	if !strings.Contains(unconfigured.notice, "No provider configured") {
		t.Fatalf("forced login notice missing: %q", unconfigured.notice)
	}
	unconfigured.width, unconfigured.height = 120, 35
	if plain := ansi.Strip(unconfigured.View()); !strings.Contains(plain, "LOGIN") || !strings.Contains(plain, "Select a provider") {
		t.Fatalf("forced login menu not rendered: %s", plain)
	}

	configured := newModel(&fakeLoginService{anyConfigured: true}, "")
	if configured.loginMenu || !configured.router {
		t.Fatalf("configured start opened the login menu: menu=%v router=%v", configured.loginMenu, configured.router)
	}
}

func TestSlashLoginWithoutProviderOpensMenu(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, router: true, width: 120, height: 35}
	m = typeSlash(m, "/login")
	if !m.loginMenu || m.loginIndex != 0 {
		t.Fatalf("menu not opened: menu=%v index=%d", m.loginMenu, m.loginIndex)
	}
	if m.router {
		t.Fatal("/login left the request form open behind the menu")
	}
	if !strings.Contains(m.notice, "Select a provider") {
		t.Fatalf("menu notice missing: %q", m.notice)
	}
	if m.loginProvider != "" {
		t.Fatalf("menu armed key entry for %q", m.loginProvider)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "LOGIN") || !strings.Contains(plain, "openai") {
		t.Fatalf("menu not rendered: %s", plain)
	}
}

func TestSlashLoginWithProviderEntersKeyEntry(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, router: true, width: 120, height: 35}
	m = typeSlash(m, "/login openai")
	if m.loginProvider != "openai" || m.loginKey != "" || m.loginMenu {
		t.Fatalf("direct login not armed: provider=%q key=%q menu=%v", m.loginProvider, m.loginKey, m.loginMenu)
	}
}

func TestProviderMenuKeySelectsProviderAndStartsKeyEntry(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{
		{Provider: "deepseek"},
		{Provider: "openai", Configured: true},
	}}
	m := model{service: fake, width: 120, height: 35, loginMenu: true, loginIndex: 0}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.loginIndex != 1 {
		t.Fatalf("j did not move the selection: %d", m.loginIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.loginIndex != 1 {
		t.Fatalf("down moved past the end: %d", m.loginIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.loginIndex != 0 {
		t.Fatalf("up did not move the selection: %d", m.loginIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.loginIndex != 0 {
		t.Fatalf("k moved above the first provider: %d", m.loginIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.loginMenu {
		t.Fatal("enter left the menu open")
	}
	if m.loginProvider != "openai" || m.loginKey != "" {
		t.Fatalf("enter did not arm key entry: provider=%q key=%q", m.loginProvider, m.loginKey)
	}
	if !strings.Contains(m.notice, "Enter API key for openai") {
		t.Fatalf("key-entry notice missing: %q", m.notice)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Enter API key for openai: (hidden)") {
		t.Fatalf("masked prompt missing: %s", plain)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-live")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setCalls) != 1 || fake.setCalls[0].provider != "openai" || fake.setCalls[0].key != "sk-live" {
		t.Fatalf("selected provider did not store the key: %+v", fake.setCalls)
	}
}

func TestProviderMenuEscapeClosesWithoutQuitting(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}}}
	m := model{service: fake, width: 120, height: 35, loginMenu: true}
	next, cmd := m.providerMenuKey("esc")
	if cmd != nil {
		t.Fatal("escape quit instead of closing the menu")
	}
	if next.loginMenu {
		t.Fatal("escape left the menu open")
	}
	if !strings.Contains(next.notice, "No provider configured") {
		t.Fatalf("forced escape notice missing: %q", next.notice)
	}

	configured := model{service: &fakeLoginService{anyConfigured: true}, width: 120, height: 35, loginMenu: true}
	closed, _ := configured.providerMenuKey("esc")
	if closed.loginMenu || strings.Contains(closed.notice, "No provider configured") {
		t.Fatalf("escape from a configured menu misreported state: %q", closed.notice)
	}
}

func TestProviderMenuQuitsOnCtrlC(t *testing.T) {
	m := model{service: &fakeLoginService{}, width: 120, height: 35, loginMenu: true}
	_, cmd := m.providerMenuKey("ctrl+c")
	if cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not return a quit command")
	}
}

func TestLoginMenuBoardKeyOpensMenu(t *testing.T) {
	m := model{width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	if !m.loginMenu {
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
		m := model{service: fake, width: size[0], height: size[1], loginMenu: true, loginIndex: 1, loginKey: secret}
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
	m := model{width: 120, height: 35, loginMenu: true}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "LOGIN") || !strings.Contains(plain, "No providers known.") {
		t.Fatalf("serviceless menu missing content: %s", plain)
	}
}

func TestRouterSubmitForcesLoginWithoutProvider(t *testing.T) {
	fake := &fakeLoginService{}
	m := model{service: fake, router: true, routerDraft: "write tests", routerCursor: len("write tests"), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.loginMenu || m.loginIndex != 0 {
		t.Fatalf("submit without a provider did not open the menu: menu=%v index=%d", m.loginMenu, m.loginIndex)
	}
	if !strings.Contains(m.notice, "No provider configured") {
		t.Fatalf("forced login notice missing: %q", m.notice)
	}
	if m.routerDraft != "write tests" {
		t.Fatalf("blocked submit discarded the draft: %q", m.routerDraft)
	}
	if len(fake.submitCalls) != 0 {
		t.Fatalf("blocked submit reached the service: %+v", fake.submitCalls)
	}

	configured := &fakeLoginService{anyConfigured: true}
	m = model{service: configured, router: true, routerDraft: "write tests", routerCursor: len("write tests"), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.loginMenu {
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
	m := model{service: fake, router: true, width: 120, height: 35}
	m = typeSlash(m, "/model")
	if !m.modelMenu || m.modelStep != modelStepClass || m.modelIndex != 0 {
		t.Fatalf("menu not opened at the class step: menu=%v step=%d index=%d", m.modelMenu, m.modelStep, m.modelIndex)
	}
	if m.modelClass != "" || m.modelProvider != "" {
		t.Fatalf("menu opened with stale choices: class=%q provider=%q", m.modelClass, m.modelProvider)
	}
	if m.router {
		t.Fatal("/model left the request form open behind the menu")
	}
	if !strings.Contains(m.notice, "Select a model class") {
		t.Fatalf("menu notice missing: %q", m.notice)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "MODEL") || !strings.Contains(plain, "big") {
		t.Fatalf("menu not rendered: %s", plain)
	}
	if len(fake.setModelCalls) != 0 {
		t.Fatalf("opening the menu pinned a model: %+v", fake.setModelCalls)
	}
}

func TestSlashModelWithAgentStartsAtProviderStep(t *testing.T) {
	m := model{service: modelMenuService(), router: true, width: 120, height: 35}
	m = typeSlash(m, "/model small")
	if !m.modelMenu || m.modelStep != modelStepProvider || m.modelClass != "small" {
		t.Fatalf("named class did not skip the class step: menu=%v step=%d class=%q", m.modelMenu, m.modelStep, m.modelClass)
	}

	unknown := model{service: modelMenuService(), router: true, width: 120, height: 35}
	unknown = typeSlash(unknown, "/model ghost")
	if unknown.modelMenu {
		t.Fatal("unknown agent opened the menu")
	}
	if !strings.Contains(unknown.notice, "Unknown class") {
		t.Fatalf("unknown agent notice missing: %q", unknown.notice)
	}
}

func TestModelMenuWalksAgentProviderModel(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, modelMenu: true, modelStep: modelStepClass}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.modelIndex != 1 {
		t.Fatalf("j did not move the selection: %d", m.modelIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.modelIndex != 1 {
		t.Fatalf("down moved past the last agent: %d", m.modelIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.modelIndex != 0 {
		t.Fatalf("up did not move the selection: %d", m.modelIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.modelIndex != 0 {
		t.Fatalf("k moved above the first agent: %d", m.modelIndex)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelStep != modelStepProvider || m.modelClass != "big" || m.modelIndex != 0 {
		t.Fatalf("class step did not advance: step=%d class=%q index=%d", m.modelStep, m.modelClass, m.modelIndex)
	}
	if choices := m.modelChoices(); len(choices) != 1 || choices[0] != "openai" {
		t.Fatalf("provider step offered %v, want only the configured provider", choices)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelStep != modelStepModel || m.modelProvider != "openai" || m.modelIndex != 0 {
		t.Fatalf("provider step did not advance: step=%d provider=%q index=%d", m.modelStep, m.modelProvider, m.modelIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 1 || fake.setModelCalls[0] != "big=openai/gpt-5-mini" {
		t.Fatalf("model step pinned %v, want big=openai/gpt-5-mini", fake.setModelCalls)
	}
	if m.modelMenu {
		t.Fatal("selecting a model left the menu open")
	}
	if !strings.Contains(m.notice, "Model for big: openai/gpt-5-mini") {
		t.Fatalf("confirmation notice missing: %q", m.notice)
	}
}

func TestModelMenuReportsServiceError(t *testing.T) {
	fake := modelMenuService()
	fake.setModelErr = errors.New("\"big\" is not a known agent")
	m := model{service: fake, width: 120, height: 35, modelMenu: true,
		modelStep: modelStepModel, modelClass: "big", modelProvider: "openai"}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice, "not a known agent") {
		t.Fatalf("error notice missing: %q", m.notice)
	}
	if m.modelMenu {
		t.Fatal("a failed selection left the menu open")
	}
}

func TestModelMenuEscapeStepsBackAndCloses(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, modelMenu: true,
		modelStep: modelStepModel, modelClass: "big", modelProvider: "openai", modelIndex: 1}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modelStep != modelStepProvider || m.modelIndex != 0 {
		t.Fatalf("escape did not return to the provider step: step=%d index=%d", m.modelStep, m.modelIndex)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modelStep != modelStepClass {
		t.Fatalf("escape did not return to the class step: step=%d", m.modelStep)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modelMenu {
		t.Fatal("escape on the class step left the menu open")
	}
	if !strings.Contains(m.notice, "Model menu closed") {
		t.Fatalf("close notice missing: %q", m.notice)
	}
	if len(fake.setModelCalls) != 0 {
		t.Fatalf("an abandoned menu pinned a model: %+v", fake.setModelCalls)
	}
}

func TestModelMenuQuitsOnCtrlC(t *testing.T) {
	m := model{service: modelMenuService(), width: 120, height: 35, modelMenu: true}
	_, cmd := m.modelMenuKey("ctrl+c")
	if cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not return a quit command")
	}
}

func TestModelMenuBoardKeyOpensMenu(t *testing.T) {
	m := model{service: modelMenuService(), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if !m.modelMenu || m.modelStep != modelStepClass || m.modelIndex != 0 {
		t.Fatalf("m did not open the model menu: menu=%v step=%d index=%d", m.modelMenu, m.modelStep, m.modelIndex)
	}
	if m.quitConfirm {
		t.Fatal("m armed the quit confirmation")
	}
}

func TestModelMenuViewListsChoicesAndHidesKeys(t *testing.T) {
	const secret = "sk-should-not-render"
	fake := modelMenuService()

	agents := model{service: fake, width: 120, height: 35, modelMenu: true, loginKey: secret}
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
	providers.modelStep = modelStepProvider
	providers.modelClass = "big"
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
		m := model{service: fake, width: size[0], height: size[1], loginKey: secret,
			modelMenu: true, modelStep: modelStepModel, modelClass: "big", modelProvider: "openai"}
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
	m := model{service: fake, width: 120, height: 35, modelMenu: true,
		modelStep: modelStepModel, modelClass: "big", modelProvider: "openai"}
	for i := 0; i < len(m.modelChoices())-1; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	}
	if m.modelChoices()[m.modelIndex] != customModelChoice {
		t.Fatalf("selection %d is %q, want the custom row", m.modelIndex, m.modelChoices()[m.modelIndex])
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelStep != modelStepCustom || m.modelCustomInput != "" {
		t.Fatalf("custom row did not open the text step: step=%d input=%q", m.modelStep, m.modelCustomInput)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Enter a model ID (openai)") || !strings.Contains(plain, "enter save") {
		t.Fatalf("custom prompt not rendered: %s", plain)
	}

	back := key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if back.modelStep != modelStepModel || back.modelCustomInput != "" {
		t.Fatalf("escape did not return to the model step: step=%d input=%q", back.modelStep, back.modelCustomInput)
	}
	if len(fake.setModelCalls) != 0 {
		t.Fatalf("escape pinned a model: %+v", fake.setModelCalls)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("my-model-9")})
	if m.modelCustomInput != "my-model-9" {
		t.Fatalf("typed model = %q, want my-model-9", m.modelCustomInput)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.modelCustomInput != "my-model-" {
		t.Fatalf("backspace left %q", m.modelCustomInput)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 1 || fake.setModelCalls[0] != "big=openai/my-model-9" {
		t.Fatalf("custom model pinned %v, want big=openai/my-model-9", fake.setModelCalls)
	}
	if m.modelMenu {
		t.Fatal("saving a custom model left the menu open")
	}
	if !strings.Contains(m.notice, "Model for big: openai/my-model-9") {
		t.Fatalf("confirmation notice missing: %q", m.notice)
	}
}

func TestModelMenuWithoutServiceRendersAndReports(t *testing.T) {
	m := model{width: 120, height: 35, modelMenu: true}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"MODEL", "Select a model class", "big", "small"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("serviceless menu missing %q: %s", want, plain)
		}
	}
	next, _ := m.modelMenuKey("enter")
	if next.modelStep != modelStepProvider {
		t.Fatalf("enter on the class step did not advance to provider: step=%d", next.modelStep)
	}
	next, _ = next.modelMenuKey("enter")
	if !next.modelMenu {
		t.Fatal("enter on an empty provider list closed the menu")
	}
	if !strings.Contains(next.notice, "Nothing to choose") {
		t.Fatalf("empty step notice missing: %q", next.notice)
	}
}
