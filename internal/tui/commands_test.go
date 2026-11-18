package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/tui/editor"
)

// errTestCredential stands in for an orchestrator rejection during login.
var errTestCredential = errors.New("API key is required")

// credentialCall records one SetProviderCredential invocation so tests can
// assert the provider and the exact key the UI handed to the service.
type credentialCall struct {
	provider string
	key      string
}

// fakeLoginService is a credential-aware stand-in for the orchestrator. The
// embedded nil Service panics on any method a test does not expect.
type fakeLoginService struct {
	Service
	tasks         []orchestrator.Task
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
	skills        []orchestrator.SkillInfo
	skillSetCalls []string // recorded as "name=enabled"
	agents        []orchestrator.AgentInfo
	agentSkills   map[string][]orchestrator.SkillInfo
}

func (f *fakeLoginService) ListTasks() []orchestrator.Task       { return f.tasks }
func (f *fakeLoginService) ListAgents() []orchestrator.AgentInfo { return f.agents }
func (f *fakeLoginService) ListAgentSkills(agent string) []orchestrator.SkillInfo {
	return f.agentSkills[agent]
}
func (f *fakeLoginService) SetAgentSkillEnabled(agent, name string, enabled bool) error {
	f.skillSetCalls = append(f.skillSetCalls, fmt.Sprintf("%s/%s=%t", agent, name, enabled))
	for i := range f.agentSkills[agent] {
		if f.agentSkills[agent][i].Name == name {
			f.agentSkills[agent][i].Enabled = enabled
		}
	}
	return nil
}
func (f *fakeLoginService) SessionUsage() orchestrator.SessionUsage {
	return orchestrator.SessionUsage{}
}
func (f *fakeLoginService) PersistenceError() error { return nil }
func (f *fakeLoginService) StartupWarning() string  { return "" }

func (f *fakeLoginService) ListSkills() []orchestrator.SkillInfo { return f.skills }

func (f *fakeLoginService) SetSkillEnabled(name string, enabled bool) error {
	f.skillSetCalls = append(f.skillSetCalls, fmt.Sprintf("%s=%t", name, enabled))
	for i := range f.skills {
		if f.skills[i].Name == name {
			f.skills[i].Enabled = enabled
		}
	}
	return nil
}

func (f *fakeLoginService) SubmitRequest(text, _ string) (orchestrator.Task, error) {
	f.submitCalls = append(f.submitCalls, text)
	return orchestrator.Task{}, nil
}

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

func (f *fakeLoginService) SetAgentModel(agent, model string) error {
	f.setModelCalls = append(f.setModelCalls, "agent:"+agent+"="+model)
	return f.setModelErr
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
			if m.router.Text != "" || m.router.Cursor != 0 {
				t.Fatalf("command left the draft behind: draft=%q cursor=%d", m.router.Text, m.router.Cursor)
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
	plain := ansi.Strip(strings.Join(helpLines(helpTopics[m.help.page], 70), "\n"))
	// The commands sit in the key column and their argument forms are documented
	// once, in the topic intro, so the table stays aligned.
	for _, want := range []string{"/login", "/logout", "/providers", "/model", "/help", "/login <provider>", "/model [big|small]"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("help overlay missing %q: %q", want, plain)
		}
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
	if m.router.Text != "" {
		t.Fatalf("typed key leaked into the draft: %q", m.router.Text)
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

func TestProviderMenuKeySelectsProviderAndStartsKeyEntry(t *testing.T) {
	fake := &fakeLoginService{creds: []orchestrator.ProviderCredential{
		{Provider: "deepseek"},
		{Provider: "openai", Configured: true},
	}}
	m := model{service: fake, width: 120, height: 35, login: loginState{menu: true, index: 0}}
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
	next := m.providerMenuKey("esc")
	if next.login.menu || next.quitConfirm {
		t.Fatal("escape left the menu open")
	}
	if !strings.Contains(next.notice.text, "No provider configured") {
		t.Fatalf("forced escape notice missing: %q", next.notice.text)
	}

	configured := model{service: &fakeLoginService{anyConfigured: true}, width: 120, height: 35, login: loginState{menu: true}}
	closed := configured.providerMenuKey("esc")
	if closed.login.menu || strings.Contains(closed.notice.text, "No provider configured") {
		t.Fatalf("escape from a configured menu misreported state: %q", closed.notice.text)
	}
}

// Login and model menus fit every size, list their choices and never render a
// typed API key.
func TestLoginAndModelMenuViews(t *testing.T) {
	const secret = "sk-should-not-render"
	small := [][2]int{{120, 35}, {80, 24}, {40, 12}}
	loginFake := &fakeLoginService{creds: []orchestrator.ProviderCredential{
		{Provider: "deepseek"},
		{Provider: "openai", Configured: true},
	}}
	models := modelMenuService()
	cases := []struct {
		name     string
		m        model
		sizes    [][2]int
		want     []string
		wideWant []string // only where the footer hint fits
		unwanted []string
	}{
		{
			name:  "login prompt",
			m:     model{router: routerState{open: true}, login: loginState{provider: "openai", key: strings.Repeat("k", 40)}},
			sizes: append(small, [2]int{30, 10}),
			want:  []string{"Enter API key"},
		},
		{
			name:     "login menu",
			m:        model{service: loginFake, login: loginState{menu: true, index: 1, key: secret}},
			sizes:    small,
			want:     []string{"LOGIN", "Select a provider", "openai", "deepseek", "configured", "no key", "j/k select"},
			unwanted: []string{secret},
		},
		{
			name:     "model class step",
			m:        model{service: models, login: loginState{key: secret}, models: modelMenuState{menu: true}},
			sizes:    small[:1],
			want:     []string{"MODEL", "Select a model class or agent", "big", "small", "default", "j/k select"},
			unwanted: []string{secret},
		},
		{
			name:     "model provider step",
			m:        model{service: models, login: loginState{key: secret}, models: modelMenuState{menu: true, step: modelStepProvider, class: "big"}},
			sizes:    small[:1],
			want:     []string{"Select a provider for big", "openai"},
			unwanted: []string{secret, "deepseek"},
		},
		{
			name:     "model step",
			m:        model{service: models, login: loginState{key: secret}, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}},
			sizes:    small,
			want:     []string{"MODEL", "Select a model (openai)", "gpt-5", "gpt-5-mini", customModelChoice, "j/k select"},
			wideWant: []string{"esc back"},
			unwanted: []string{secret},
		},
		{
			name:  "rate-tagged model rows",
			m:     model{service: deepSeekMenuService(), models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "deepseek"}},
			sizes: small,
			want:  []string{"deepseek-chat"},
		},
	}
	for _, tc := range cases {
		for _, size := range tc.sizes {
			m := tc.m
			m.width, m.height = size[0], size[1]
			view := m.View()
			assertFits(t, view, size[0], size[1])
			plain := ansi.Strip(view)
			want := tc.want
			if size[0] >= 80 {
				want = append(append([]string{}, want...), tc.wideWant...)
			}
			for _, w := range want {
				if !strings.Contains(plain, w) {
					t.Fatalf("%s %v: missing %q: %s", tc.name, size, w, plain)
				}
			}
			for _, u := range tc.unwanted {
				if strings.Contains(plain, u) {
					t.Fatalf("%s %v: rendered %q: %s", tc.name, size, u, plain)
				}
			}
		}
	}
}

func TestRouterSubmitForcesLoginWithoutProvider(t *testing.T) {
	fake := &fakeLoginService{}
	m := model{service: fake, router: routerState{open: true, Editor: editor.Editor{Text: "write tests", Cursor: len("write tests")}}, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.login.menu || m.login.index != 0 {
		t.Fatalf("submit without a provider did not open the menu: menu=%v index=%d", m.login.menu, m.login.index)
	}
	if !strings.Contains(m.notice.text, "No provider configured") {
		t.Fatalf("forced login notice missing: %q", m.notice.text)
	}
	if m.router.Text != "write tests" {
		t.Fatalf("blocked submit discarded the draft: %q", m.router.Text)
	}
	if len(fake.submitCalls) != 0 {
		t.Fatalf("blocked submit reached the service: %+v", fake.submitCalls)
	}

	configured := &fakeLoginService{anyConfigured: true}
	m = model{service: configured, router: routerState{open: true, Editor: editor.Editor{Text: "write tests", Cursor: len("write tests")}}, width: 120, height: 35}
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
	if !strings.Contains(m.notice.text, "Select a model class or agent") {
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

func TestModelMenuAssignsAgentClass(t *testing.T) {
	fake := modelMenuService()
	fake.agents = []orchestrator.AgentInfo{{Name: "tester"}}
	m := typeSlash(model{service: fake, router: routerState{open: true}, width: 120, height: 35}, "/model")
	m = key(m, runeKey("j"))
	m = key(m, runeKey("j"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter}) // Agents…
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter}) // tester
	if m.models.agent != "tester" || !strings.Contains(m.modelMenuTitle(), "tester") {
		t.Fatalf("agent selection: %+v", m.models)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 1 || fake.setModelCalls[0] != "agent:tester=big" {
		t.Fatalf("assignment calls: %v", fake.setModelCalls)
	}
	m = typeSlash(model{service: fake, router: routerState{open: true}, width: 120, height: 35}, "/model tester")
	if m.models.agent != "tester" || m.models.step != modelStepAgentModel {
		t.Fatalf("named agent selection: %+v", m.models)
	}
	m = key(m, runeKey("j"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.setModelCalls) != 2 || fake.setModelCalls[1] != "agent:tester=small" {
		t.Fatalf("assignment calls: %v", fake.setModelCalls)
	}
}

func TestModelMenuAssignsAgentCustomModel(t *testing.T) {
	fake := modelMenuService()
	fake.agents = []orchestrator.AgentInfo{{Name: "tester"}}
	fake.models["openai"] = []string{"catalog-model"}
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	m := typeSlash(model{service: fake, router: routerState{open: true}}, "/model tester")
	for _, size := range [][2]int{{120, 35}, {40, 12}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		assertFits(t, view, size[0], size[1])
		for _, want := range []string{"big", "small", customModelChoice} {
			if !strings.Contains(ansi.Strip(view), want) {
				t.Fatalf("agent model chooser missing %q: %s", want, view)
			}
		}
	}
	m = key(key(m, runeKey("j")), runeKey("j"))
	m = key(m, enter) // custom…
	if m.models.step != modelStepProvider || m.models.agent != "tester" {
		t.Fatalf("custom selection: %+v", m.models)
	}
	back := key(m, esc)
	if back.models.step != modelStepAgentModel || back.models.agent != "tester" || len(fake.setModelCalls) != 0 {
		t.Fatalf("back from custom provider: %+v, calls %v", back.models, fake.setModelCalls)
	}
	back = key(back, esc)
	if back.models.step != modelStepAgent || back.models.agent != "" {
		t.Fatalf("back from agent chooser: %+v", back.models)
	}
	m = key(m, enter) // openai; drain the catalog fetch
	if m.models.loading || m.models.listProvider != "openai" {
		t.Fatalf("provider catalog did not load: %+v", m.models)
	}
	typed := key(key(m, runeKey("j")), enter) // custom ID rather than a catalog entry
	typed = key(key(typed, runeKey("ft:gpt-4.1+suffix")), enter)
	m = key(m, enter) // catalog-model
	if typed.models.menu || m.models.menu || len(fake.setModelCalls) != 2 ||
		fake.setModelCalls[0] != "agent:tester=openai/ft:gpt-4.1+suffix" || fake.setModelCalls[1] != "agent:tester=openai/catalog-model" {
		t.Fatalf("custom assignment calls: %v", fake.setModelCalls)
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

// deepSeekMenuService configures only DeepSeek so the model step lists its
// curated catalog.
func deepSeekMenuService() *fakeLoginService {
	return &fakeLoginService{
		creds:  []orchestrator.ProviderCredential{{Provider: "deepseek", Configured: true}},
		models: map[string][]string{"deepseek": {"deepseek-chat", "deepseek-reasoner"}},
	}
}

// Only models the price table charges by time of day name a window: in the
// label itself, the model rows, the pinned class and the confirmation notice.
func TestModelRateWindows(t *testing.T) {
	peak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		selector string
		at       time.Time
		want     string
	}{
		{"deepseek/deepseek-chat", peak, orchestrator.RatePeak},
		{"deepseek/deepseek-reasoner", offPeak, orchestrator.RateOffPeak},
		{"openai/gpt-5", offPeak, ""},
		{"deepseek/deepseek-unlisted", offPeak, ""},
		{"custom/model", offPeak, ""},
	} {
		if got := modelRateLabel(tc.selector, tc.at); got != tc.want {
			t.Errorf("%s at %s: label %q, want %q", tc.selector, tc.at.Format("15:04"), got, tc.want)
		}
	}

	// The menu reads the clock itself, so render everything inside one window
	// and skip if a boundary passed meanwhile.
	label := modelRateLabel("deepseek/deepseek-chat", time.Now())
	deepseek := deepSeekMenuService()
	deepseek.overrides = orchestrator.ModelSelection{Big: "deepseek/deepseek-chat"}
	modelStep := modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "deepseek"}
	rows := ansi.Strip(model{service: deepseek, width: 120, height: 35, models: modelStep}.View())
	class := ansi.Strip(model{service: deepseek, width: 120, height: 35, models: modelMenuState{menu: true}}.View())
	pinnedFake := deepSeekMenuService()
	pinned := key(model{service: pinnedFake, width: 120, height: 35, models: modelStep}, tea.KeyMsg{Type: tea.KeyEnter})
	modelStep.provider = "openai"
	openaiRows := ansi.Strip(model{service: modelMenuService(), width: 120, height: 35, models: modelStep}.View())
	openai := key(model{service: modelMenuService(), width: 120, height: 35, models: modelStep}, tea.KeyMsg{Type: tea.KeyEnter})
	if after := modelRateLabel("deepseek/deepseek-chat", time.Now()); after != label {
		t.Skipf("pricing window changed during the test: %s then %s", label, after)
	}

	if !strings.Contains(rows, "deepseek-chat") || strings.Count(rows, label) != 2 {
		t.Fatalf("model rows lost the %q window: %s", label, rows)
	}
	if !strings.Contains(class, "deepseek/deepseek-chat · "+label+" now") || strings.Count(class, label) != 1 {
		t.Fatalf("class step did not label only the pinned class: %s", class)
	}
	if len(pinnedFake.setModelCalls) != 1 || pinnedFake.setModelCalls[0] != "big=deepseek/deepseek-chat" {
		t.Fatalf("model step pinned %v, want big=deepseek/deepseek-chat", pinnedFake.setModelCalls)
	}
	if want := "Model for big: deepseek/deepseek-chat · " + label + " now"; !strings.Contains(pinned.notice.text, want) {
		t.Fatalf("confirmation notice missing %q: %q", want, pinned.notice.text)
	}
	if strings.Contains(openaiRows, orchestrator.RatePeak) || strings.Contains(openaiRows, orchestrator.RateOffPeak) {
		t.Fatalf("all-day provider named a window: %s", openaiRows)
	}
	if openai.notice.text != "Model for big: openai/gpt-5" {
		t.Fatalf("all-day confirmation = %q, want no window", openai.notice.text)
	}
}

func TestModelMenuUsesFetchedListInsteadOfBuiltIn(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
	updated, _ := m.Update(fetchModelsMsg{provider: "openai", models: []string{"live-model"}})
	m = updated.(model)
	choices := m.modelChoices()
	if len(choices) != 2 || choices[0] != "live-model" || m.models.fetchFailed {
		t.Fatalf("fetched list not shown: %v failed=%v", choices, m.models.fetchFailed)
	}
}

func TestModelMenuSaysWhenFetchFailedAndBuiltInListIsShown(t *testing.T) {
	fake := modelMenuService()
	m := model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}
	updated, _ := m.Update(fetchModelsMsg{provider: "openai", err: errors.New("models endpoint returned 401")})
	m = updated.(model)
	if !m.models.fetchFailed || !strings.Contains(m.notice.text, "401") {
		t.Fatalf("fetch failure not reported: failed=%v notice=%q", m.models.fetchFailed, m.notice.text)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Live list unavailable") {
		t.Fatal("menu does not say the built-in list is shown")
	}
	if len(m.modelChoices()) < 2 {
		t.Fatal("built-in fallback list missing")
	}
}

func TestLoginKeyEntryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		last   tea.KeyMsg
		notice string
		stored int
	}{
		{"escape cancels", nil, tea.KeyMsg{Type: tea.KeyEsc}, "Login canceled", 0},
		{"service error", errTestCredential, tea.KeyMsg{Type: tea.KeyEnter}, errTestCredential.Error(), 1},
	} {
		fake := &fakeLoginService{setErr: tc.err}
		m := typeSlash(model{service: fake, router: routerState{open: true}, width: 120, height: 35}, "/login openai")
		m = key(key(m, runeKey("sk-secret")), tc.last)
		if !strings.Contains(m.notice.text, tc.notice) || m.login != (loginState{}) || !m.router.open || len(fake.setCalls) != tc.stored {
			t.Fatalf("%s: notice=%q login=%+v router=%v calls=%d", tc.name, m.notice.text, m.login, m.router.open, len(fake.setCalls))
		}
	}
	fake := modelMenuService()
	fake.setModelErr = errors.New("\"big\" is not a known agent")
	m := key(model{service: fake, width: 120, height: 35, models: modelMenuState{menu: true, step: modelStepModel, class: "big", provider: "openai"}}, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice.text, "not a known agent") || m.models.menu {
		t.Fatalf("model error: notice=%q menu=%v", m.notice.text, m.models.menu)
	}
}
