package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestSubscriptionLoginDispatch(t *testing.T) {
	for _, source := range []string{"slash", "alias", "menu"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			service := &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai"}, {Provider: "openai-subscription"}}}
			m := model{service: service, router: routerState{open: true}, width: 100, height: 30}
			var next tea.Model
			var cmd tea.Cmd
			if source != "menu" {
				m.router.Text = "/login openai subscription"
				if source == "alias" {
					m.router.Text = "/login openai-subscription"
				}
				next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			} else {
				m.router.open = false
				m.login = loginState{menu: true, index: 1}
				view := ansi.Strip(m.loginMenuView("", 100, 30))
				for _, label := range []string{"openai API key", "openai-subscription", "ChatGPT"} {
					if !strings.Contains(view, label) {
						t.Fatalf("missing %q: %s", label, view)
					}
				}
				next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			}
			got := next.(model)
			if cmd == nil || got.pendingCmd != nil || got.login != (loginState{}) || !got.router.open || got.router.Text != "" {
				t.Fatalf("login dispatch failed: model=%+v command=%v", got, cmd != nil)
			}
			if msg := cmd(); reflect.TypeOf(msg).String() != "tea.execMsg" {
				t.Fatalf("expected terminal-suspending process message, got %T", msg)
			}
			if len(service.setCalls) != 0 || len(service.submitCalls) != 0 {
				t.Fatal("subscription login stored an API key or submitted a request")
			}
		})
	}
}

func TestSubscriptionLoginCompletion(t *testing.T) {
	for _, err := range []error{nil, errors.New("codex not found"), errors.New("exit status 1")} {
		m := model{service: &fakeLoginService{creds: []orchestrator.ProviderCredential{{Provider: "openai-subscription", Configured: true}}}}
		next, cmd := m.Update(subscriptionLoginResult(err))
		notice := next.(model).notice.text
		if cmd != nil {
			t.Fatal("completion launched another command")
		}
		if err == nil {
			if !strings.Contains(notice, "/model big") || !strings.Contains(notice, "openai-subscription") {
				t.Fatalf("missing routing guidance: %q", notice)
			}
		} else if !strings.Contains(notice, err.Error()) || !strings.Contains(notice, "/login openai subscription") {
			t.Fatalf("missing failure and retry guidance: %q", notice)
		}
	}
}

func TestSubscriptionCredentialIsNativeProvider(t *testing.T) {
	service := &fakeLoginService{creds: []orchestrator.ProviderCredential{
		{Provider: "openai"}, {Provider: "openai-subscription", Configured: true},
	}}
	m := model{service: service, models: modelMenuState{step: modelStepProvider}}
	choices := m.modelChoices()
	if len(choices) != 1 || choices[0] != "openai-subscription" {
		t.Fatalf("native subscription provider missing: %v", choices)
	}
	view := ansi.Strip(m.loginMenuView("", 100, 30))
	if strings.Count(view, "openai-subscription") != 1 {
		t.Fatalf("subscription row missing or duplicated: %s", view)
	}
}

func TestSubscriptionLoginRejectsUnknownMethod(t *testing.T) {
	m := model{}
	m = m.slashCommand("/login openai subscription extra")
	if m.pendingCmd != nil || m.login.provider != "" || !strings.Contains(m.notice.text, "Usage:") {
		t.Fatalf("invalid method started login: %+v", m)
	}
}

func TestSubscriptionHelp(t *testing.T) {
	for _, topic := range helpTopics {
		if topic.title != "Commands" {
			continue
		}
		text := strings.Join(topic.intro, " ")
		for _, want := range []string{"/login openai subscription", "/model big", "openai-subscription"} {
			if !strings.Contains(text, want) {
				t.Fatalf("help missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "@codex") {
			t.Fatal("help still requires a Codex executor")
		}
		return
	}
	t.Fatal("commands help missing")
}

func TestSubscriptionLoginRequiresUsableCredentials(t *testing.T) {
	for _, credentials := range [][]orchestrator.ProviderCredential{
		nil,
		{{Provider: "openai-subscription"}},
		{{Provider: "openai", Configured: true}},
	} {
		m := model{service: &fakeLoginService{creds: credentials}}
		next, _ := m.Update(subscriptionLoginResult(nil))
		notice := next.(model).notice.text
		if strings.Contains(notice, "login complete") || !strings.Contains(notice, "no usable ChatGPT subscription credentials") || !strings.Contains(notice, "/login openai subscription") {
			t.Fatalf("credentials=%+v notice=%q", credentials, notice)
		}
	}
}
