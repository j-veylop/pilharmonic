package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/providers"
)

// slashCommandsHelp is shown alongside an unknown command, so the operator
// always sees the whole vocabulary.
const slashCommandsHelp = "/login — pick a provider (or /login <provider>; /login openai subscription for ChatGPT), /logout <provider>, /providers, /model [big|small], /skills, /attach <path>, /detach <path|all>, /theme [auto|dark|light], /help"

// forcedLoginNotice is the standing message while no provider is configured.
const forcedLoginNotice = "No provider configured — log in first"

// slashCommand handles a command typed into the request box. It never
// submits a task; the router clears the draft after dispatching.
func (m model) slashCommand(line string) model {
	fields := strings.Fields(line)
	arg := ""
	if len(fields) > 1 {
		arg = fields[1]
	}
	switch fields[0] {
	case "/providers":
		m.notice.text = m.providerCredentialNotice()
	case "/login":
		if arg == "" {
			m.login = loginState{menu: true}
			m.router.open = false
			m.notice.text = "Select a provider"
			return m
		}
		if arg == "openai-subscription" && len(fields) == 2 {
			return m.startSubscriptionLogin()
		}
		if len(fields) > 2 {
			if len(fields) == 3 && arg == "openai" && fields[2] == "subscription" {
				return m.startSubscriptionLogin()
			}
			m.notice.text = "Usage: /login <provider> or /login openai subscription"
			return m
		}
		m.login.provider, m.login.key = arg, ""
		m.notice.text = "Enter API key for " + arg
	case "/attach":
		if err := m.attach(arg); err != nil {
			m.notice.text = err.Error()
		} else {
			m.notice.text = "Attached " + filepath.Base(filepath.Clean(arg)) + " to the next request."
		}
	case "/detach":
		if arg == "" {
			m.notice.text = "Usage: /detach <path|all>"
			return m
		}
		m.detach(arg)
		m.notice.text = "Attachments: " + fmt.Sprintf("%d", len(m.attachments))
	case "/theme":
		mode := arg
		switch mode {
		case "":
			switch m.theme {
			case "dark":
				mode = "light"
			case "light":
				mode = "auto"
			default:
				mode = "dark"
			}
		case "auto", "dark", "light":
		default:
			m.notice.text = "Usage: /theme [auto|dark|light]"
			return m
		}
		m.theme = ApplyTheme(mode)
		m.setAnimColors()
		m.notice.text = "Theme: " + m.theme
	case "/model":
		if arg != "" && !slices.Contains(modelClasses, arg) && !slices.ContainsFunc(m.service.ListAgents(), func(a orchestrator.AgentInfo) bool { return a.Name == arg }) {
			m.notice.text = "Unknown class or agent: " + arg + " · use big, small, or an agent name"
			return m
		}
		m.openModelMenu(arg)
	case "/skills":
		m.openSkillMenu()
	case "/logout":
		if arg == "" {
			m.notice.text = "Usage: /logout <provider>"
			return m
		}
		m.notice.text = resultFor("Removed API key for "+arg, m.service.RemoveProviderCredential(arg)).text()
	case "/help":
		return m.openHelp()
	default:
		m.notice.text = "Unknown command · " + slashCommandsHelp
	}
	return m
}

// providerCredentialNotice lists every known provider and whether a key is
// stored. The keys themselves are never part of the notice.
func (m model) providerCredentialNotice() string {
	credentials := m.service.ProviderCredentials()
	if len(credentials) == 0 {
		return "Providers: none known"
	}
	parts := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		state := "no key"
		if credential.Configured {
			state = "configured"
		}
		parts = append(parts, credential.Provider+" ("+state+")")
	}
	return "Providers: " + strings.Join(parts, ", ")
}

// loginKeyMsg captures every key press while an API key is being entered. The
// typed key only ever reaches the service; the prompt shows a mask.
func (m model) loginKeyMsg(msg tea.KeyMsg) model {
	switch msg.String() {
	case "enter":
		provider, key := m.login.provider, m.login.key
		m.login.provider, m.login.key = "", ""
		m.clearRouterDraft()
		m.notice.text = resultFor("Stored API key for "+provider, m.service.SetProviderCredential(provider, key)).text()
	case "esc":
		m.login.provider, m.login.key = "", ""
		m.notice.text = "Login canceled"
	case "ctrl+c":
		return m.requestQuit()
	case "backspace", "ctrl+h":
		m.login.key = trimLastRune(m.login.key)
	default:
		m.login.key += string(msg.Runes)
	}
	return m
}

// loginComposerLines renders the masked key prompt in place of the request
// draft, keeping the composer's single-row shape.
func (m model) loginComposerLines(width int) []string {
	masked := "(hidden)"
	if m.login.key != "" {
		masked = strings.Repeat("*", utf8.RuneCountInString(m.login.key))
	}
	line := "Enter API key for " + terminalText(m.login.provider) + ": " + masked
	content := max(1, width-1)
	return []string{fit(line, content, 1) + cursorGlyph()}
}

func trimLastRune(text string) string {
	_, size := utf8.DecodeLastRuneInString(text)
	return text[:len(text)-size]
}

// providerMenuKey drives the provider login menu; /login opens the same state.
func (m model) providerMenuKey(key string) model {
	credentials := m.service.ProviderCredentials()
	if m.listMenuKey(key, &m.login.index, len(credentials)) {
		return m
	}
	switch key {
	case "esc", "q":
		m.login.menu = false
		m.notice.text = "No provider configured"
		if m.service.AnyProviderConfigured() {
			m.notice.text = "Login closed"
			m.router.open = true
		}
	case "enter":
		if len(credentials) == 0 {
			m.notice.text = "No providers known"
			return m
		}
		provider := credentials[max(0, min(len(credentials)-1, m.login.index))].Provider
		if provider == "openai-subscription" {
			return m.startSubscriptionLogin()
		}
		m.login = loginState{provider: provider}
		// The masked key prompt lives in the request composer, wherever the
		// menu was opened.
		m.router.open = true
		m.clearRouterDraft()
		m.notice.text = "Enter API key for " + provider
	}
	return m
}

func (m model) loginMenuView(background string, w, h int) string {
	credentials := m.service.ProviderCredentials()
	rows := make([]menuRow, len(credentials))
	for i, credential := range credentials {
		tag := mutedStyle.Render("no key")
		if credential.Configured {
			tag = accentStyle.Render("configured")
		}
		label := credential.Provider
		if label == "openai" {
			label += " API key"
		}
		if label == "openai-subscription" {
			label += " (ChatGPT)"
			tag = mutedStyle.Render("codex login")
			if credential.Configured {
				tag = accentStyle.Render("configured")
			}
		}
		rows[i] = menuRow{label: label, tag: tag}
	}
	mw := min(52, w-8) + 4
	mh := min(len(credentials)+8, h-2)
	return menuView(background, w, h, "LOGIN", "Select a provider", nil, rows, m.login.index, "No providers known.", "j/k select · enter login · f1 help · esc close", mw, mh)
}

type subscriptionLoginMsg struct{ err error }

func subscriptionLoginResult(err error) tea.Msg {
	return subscriptionLoginMsg{err: err}
}

func (m model) startSubscriptionLogin() model {
	m.login = loginState{}
	m.router.open = true
	m.clearRouterDraft()
	m.notice.text = "Opening Codex ChatGPT subscription login in your browser"
	m.pendingCmd = tea.ExecProcess(providers.LoginCommand(false), subscriptionLoginResult)
	return m
}

func (m *model) takePendingCommand() tea.Cmd {
	command := m.pendingCmd
	m.pendingCmd = nil
	return command
}
