package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

// slashCommandsHelp is the one-line summary shown by /help and alongside an
// unknown command, so the operator always sees the whole vocabulary.
const slashCommandsHelp = "/login — pick a provider (or /login <provider>), /logout <provider>, /providers, /model [big|small], /agents, /help"

// forcedLoginNotice is the standing message while no provider is configured.
const forcedLoginNotice = "No provider configured — log in first"

// slashCommand handles a command typed into the new-request form. It never
// submits a task; the router clears the draft after dispatching.
func (m model) slashCommand(line string) model {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return m
	}
	switch fields[0] {
	case "/providers":
		m.notice.text = m.providerCredentialNotice()
	case "/login":
		provider := ""
		if len(fields) > 1 {
			provider = fields[1]
		}
		if provider == "" {
			m.login.menu = true
			m.login.index = 0
			m.router.open = false
			m.clearRouterDraft()
			m.notice.text = "Select a provider"
			return m
		}
		m.login.provider = provider
		m.login.key = ""
		m.notice.text = "Enter API key for " + provider + " (Enter to save, Esc to cancel)"
	case "/model":
		class := ""
		if len(fields) > 1 {
			class = fields[1]
		}
		if class == "" {
			m.openModelMenu("")
			return m
		}
		if !m.knownClass(class) {
			m.notice.text = "Unknown class: " + class + " · use big or small"
			return m
		}
		m.openModelMenu(class)
		return m
	case "/agents":
		m.agentClass.menu = true
		m.agentClass.index = 0
		m.router.open = false
		m.clearRouterDraft()
		m.notice.text = "Select an agent to toggle big/small"
		return m
	case "/logout":
		if len(fields) < 2 || fields[1] == "" {
			m.notice.text = "Usage: /logout <provider>"
			return m
		}
		m.notice.text = m.removeProviderCredential(fields[1])
	case "/help":
		// The overlay documents the slash vocabulary too, so /help opens the
		// real help menu instead of a one-line notice.
		return m.openHelp()
	default:
		m.notice.text = "Unknown command · " + slashCommandsHelp
	}
	return m
}

// providerCredentialNotice lists every known provider and whether a key is
// stored. The keys themselves are never part of the notice.
func (m model) providerCredentialNotice() string {
	if m.service == nil {
		return "not connected"
	}
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

// removeProviderCredential forgets a stored key and reports the outcome as a
// notice.
func (m model) removeProviderCredential(provider string) string {
	if m.service == nil {
		return "not connected"
	}
	if err := m.service.RemoveProviderCredential(provider); err != nil {
		return err.Error()
	}
	return "Removed API key for " + provider
}

// loginKeyMsg captures every key press while an API key is being entered. The
// typed key only ever reaches the service; the prompt shows a mask.
func (m model) loginKeyMsg(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.submitLoginKey()
	case "esc":
		m.login.provider, m.login.key = "", ""
		m.notice.text = "Login canceled"
		return m, nil
	case "ctrl+c":
		return m.requestQuit(), nil
	case "backspace", "ctrl+h":
		m.login.key = trimLastRune(m.login.key)
		return m, nil
	}
	if len(msg.Runes) > 0 {
		m.login.key += string(msg.Runes)
	}
	return m, nil
}

// submitLoginKey stores the typed key and leaves login mode. The notice names
// the provider only, never the key.
func (m model) submitLoginKey() (model, tea.Cmd) {
	provider, key := m.login.provider, m.login.key
	m.login.provider, m.login.key = "", ""
	m.clearRouterDraft()
	if m.service == nil {
		m.notice.text = "Provider login is not connected to a service."
		return m, nil
	}
	if err := m.service.SetProviderCredential(provider, key); err != nil {
		m.notice.text = err.Error()
		return m, nil
	}
	m.notice.text = "Stored API key for " + provider
	return m, nil
}

// loginComposerLines renders the masked key prompt in place of the request
// draft, keeping the composer's single-row shape.
func (m model) loginComposerLines(width int) []string {
	masked := "(hidden)"
	if m.login.key != "" {
		masked = strings.Repeat("*", utf8.RuneCountInString(m.login.key))
	}
	line := "Enter API key for " + terminalText(m.login.provider) + ": " + masked
	content := max(1, width-lipgloss.Width(composerPrompt)-1)
	return []string{fit(accentStyle.Render(composerPrompt)+line, content, 1) + "▏"}
}

// trimLastRune drops the trailing character of text, keeping UTF-8 intact.
func trimLastRune(text string) string {
	if text == "" {
		return text
	}
	_, size := utf8.DecodeLastRuneInString(text)
	return text[:len(text)-size]
}

// providerCredentials lists the known providers, or an empty list when the
// terminal is not connected to a service.
func (m model) providerCredentials() []orchestrator.ProviderCredential {
	if m.service == nil {
		return []orchestrator.ProviderCredential{}
	}
	return m.service.ProviderCredentials()
}

// anyProviderConfigured reports whether a request can be routed at all. A
// missing service counts as unconfigured so the menu still appears.
func (m model) anyProviderConfigured() bool {
	return m.service != nil && m.service.AnyProviderConfigured()
}

// providerMenuKey drives the provider login menu. The menu is the discoverable
// path to login; the /login slash command opens the same state.
func (m model) providerMenuKey(key string) (model, tea.Cmd) {
	credentials := m.providerCredentials()
	last := len(credentials) - 1
	switch key {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc", "q":
		m.login.menu = false
		if m.anyProviderConfigured() {
			m.notice.text = "Login closed"
		} else {
			m.notice.text = "No provider configured"
		}
		return m, nil
	case "up", "k", "down", "j":
		moveMenuIndex(key, &m.login.index, len(credentials))
		return m, nil
	case "enter":
		if len(credentials) == 0 {
			m.notice.text = "No providers known"
			return m, nil
		}
		provider := credentials[max(0, min(last, m.login.index))].Provider
		m.login.menu = false
		m.login.provider = provider
		m.login.key = ""
		// The masked key prompt lives in the request composer, so key entry
		// always happens there regardless of where the menu was opened.
		m.router.open = true
		m.router.focus = routerFocusComposer
		m.clearRouterDraft()
		m.notice.text = "Enter API key for " + provider + " (Enter to save, Esc to cancel)"
		m.clampRouter()
		return m, nil
	}
	return m, nil
}

// loginMenuView draws the provider menu as a centered modal over the current
// screen, reusing helpView's overlay so both surfaces read as one family. Keys
// are never rendered here.
func (m model) loginMenuView(background string, w, h int) string {
	credentials := m.providerCredentials()
	rows := make([]menuRow, len(credentials))
	for i, credential := range credentials {
		tag := mutedStyle.Render("no key")
		if credential.Configured {
			tag = accentStyle.Render("configured")
		}
		rows[i] = menuRow{label: credential.Provider, tag: tag}
	}
	mw := min(52, w-8) + 4
	mh := min(len(credentials)+8, h-2)
	return menuView(background, w, h, "LOGIN", "Select a provider", nil, rows, m.login.index, "No providers known.", "j/k select · enter login · f1 help · esc close", mw, mh)
}

// Model menu steps. The menu walks agent -> provider -> model, with a free-text
// step for model IDs the curated catalog does not list.
type modelStep int

const (
	modelStepClass modelStep = iota
	modelStepProvider
	modelStepModel
	modelStepCustom
)

// customModelChoice is the sentinel row that opens free-text model entry.
const customModelChoice = "custom…"

// modelClasses returns the two model classes the operator chooses between.
func (m model) modelClasses() []string {
	return []string{orchestrator.ModelClassBig, orchestrator.ModelClassSmall}
}

// knownClass reports whether name is a valid model class.
func (m model) knownClass(name string) bool {
	return slices.Contains(m.modelClasses(), name)
}

// configuredProviders lists the providers that hold an API key. Only they can
// serve a model, so the model menu never offers an unconfigured one.
func (m model) configuredProviders() []orchestrator.ProviderCredential {
	providers := []orchestrator.ProviderCredential{}
	for _, credential := range m.providerCredentials() {
		if credential.Configured {
			providers = append(providers, credential)
		}
	}
	return providers
}

// modelsForProvider lists the built-in fallback model IDs for a provider prefix.
func (m model) modelsForProvider(provider string) []string {
	if m.service == nil {
		return nil
	}
	return m.service.ModelsForProvider(provider)
}

// modelChoices lists the selectable rows for the current step.
func (m model) modelChoices() []string {
	switch m.models.step {
	case modelStepProvider:
		credentials := m.configuredProviders()
		names := make([]string, 0, len(credentials))
		for _, credential := range credentials {
			names = append(names, credential.Provider)
		}
		return names
	case modelStepModel:
		if m.models.listProvider == m.models.provider && len(m.models.list) > 0 {
			return append(append([]string(nil), m.models.list...), customModelChoice)
		}
		if m.models.loading {
			return nil
		}
		models := m.modelsForProvider(m.models.provider)
		return append(models, customModelChoice)
	case modelStepCustom:
		return nil
	default:
		return m.modelClasses()
	}
}

// modelMenuTitle is the heading for the current step.
func (m model) modelMenuTitle() string {
	switch m.models.step {
	case modelStepProvider:
		return "Select a provider for " + m.models.class
	case modelStepModel:
		return "Select a model (" + m.models.provider + ")"
	case modelStepCustom:
		return "Enter a model ID (" + m.models.provider + ")"
	default:
		return "Select a model class"
	}
}

// openModelMenu arms the menu, optionally on the provider step for a class
// named by /model <class>. Any open request form is cleared so the menu owns
// the keyboard.
func (m *model) openModelMenu(class string) {
	m.models.menu = true
	m.models.index = 0
	m.models.customInput = ""
	m.models.provider = ""
	m.router.open = false
	m.clearRouterDraft()
	if class == "" {
		m.models.class = ""
		m.models.step = modelStepClass
		m.notice.text = "Select a model class"
		return
	}
	m.models.class = class
	m.models.step = modelStepProvider
	m.notice.text = "Select a provider for " + class
}

// modelMenuKey drives the agent -> provider -> model menu. Selections reach the
// service only on the final step, so an abandoned menu changes nothing.
func (m model) modelMenuKey(key string) (model, tea.Cmd) {
	if m.models.step == modelStepCustom {
		return m.modelCustomKey(key)
	}
	choices := m.modelChoices()
	last := len(choices) - 1
	switch key {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc":
		if m.models.step == modelStepClass {
			m.models.menu = false
			m.notice.text = "Model menu closed"
			return m, nil
		}
		m.models.step--
		m.models.index = 0
		m.notice.text = ""
		return m, nil
	case "up", "k", "down", "j":
		moveMenuIndex(key, &m.models.index, len(choices))
		return m, nil
	case "enter":
		if len(choices) == 0 {
			m.notice.text = "Nothing to choose"
			return m, nil
		}
		return m.selectModelChoice(choices[max(0, min(last, m.models.index))])
	}
	return m, nil
}

// selectModelChoice advances the menu or, on the model step, pins the agent to
// the chosen provider/model selector.
func (m model) selectModelChoice(choice string) (model, tea.Cmd) {
	switch m.models.step {
	case modelStepClass:
		m.models.class = choice
		m.models.step = modelStepProvider
		m.models.index = 0
		m.notice.text = "Select a provider for " + choice
		return m, nil
	case modelStepProvider:
		m.models.provider = choice
		m.models.step = modelStepModel
		m.models.index = 0
		m.notice.text = ""
		m.models.list = nil
		m.models.listProvider = ""
		m.models.loading = true
		m.models.fetchFailed = false
		return m, m.fetchModelsCmd(choice)
	case modelStepModel:
		if choice == customModelChoice {
			m.models.step = modelStepCustom
			m.models.customInput = ""
			return m, nil
		}
		return m.applyModelSelection(m.models.provider + "/" + choice)
	}
	return m, nil
}

// applyModelSelection pins the chosen class and closes the menu. The notice
// names the selector, never a credential.
func (m model) applyModelSelection(selector string) (model, tea.Cmd) {
	class := m.models.class
	m.models.menu = false
	m.models.step = modelStepClass
	m.models.index = 0
	m.models.customInput = ""
	if m.service == nil {
		m.notice.text = "Model selection is not connected to a service."
		return m, nil
	}
	if err := m.service.SetModel(class, selector); err != nil {
		m.notice.text = err.Error()
		return m, nil
	}
	notice := "Model for " + class + ": " + selector
	// A provider with a daily discount window is labelled with the rate in
	// force now, because that is the price the next attempt will be charged.
	if label := modelRateLabel(selector, time.Now()); label != "" {
		notice += " · " + label + " now"
	}
	m.notice.text = notice
	return m, nil
}

// fetchModelsCmd fetches a provider's current model list from its API and
// delivers it as a fetchModelsMsg. The curated catalog is the fallback on error,
// and the menu says so, so a failed fetch never blocks model selection or passes
// the built-in list off as live.
func (m model) fetchModelsCmd(provider string) tea.Cmd {
	return func() tea.Msg {
		if m.service == nil {
			return fetchModelsMsg{provider: provider, err: errors.New("not connected")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		models, err := m.service.FetchModels(ctx, provider)
		return fetchModelsMsg{provider: provider, models: models, err: err}
	}
}

// modelCustomKey captures keys while a model ID is typed. Only literal text
// reaches the field; navigation keys are handled explicitly.
func (m model) modelCustomKey(key string) (model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "enter":
		modelID := strings.TrimSpace(m.models.customInput)
		if modelID == "" {
			m.notice.text = "Enter a model ID"
			return m, nil
		}
		return m.applyModelSelection(m.models.provider + "/" + modelID)
	case "esc":
		m.models.step = modelStepModel
		m.models.index = 0
		m.models.customInput = ""
		return m, nil
	case "backspace", "ctrl+h":
		m.models.customInput = trimLastRune(m.models.customInput)
		return m, nil
	}
	if modelTextKey(key) {
		m.models.customInput += key
	}
	return m, nil
}

// modelTextKey reports whether a key string is literal text for a free-text
// field. Named keys and modifier chords are not text.
func modelTextKey(key string) bool {
	if key == "" || strings.Contains(key, "+") {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return false
		}
	}
	switch key {
	case "tab", "enter", "esc", "backspace", "delete", "insert", "space",
		"home", "end", "up", "down", "left", "right", "pgup", "pgdown":
		return false
	}
	return true
}

// modelMenuView draws the model menu as a centered modal over the current
// screen, reusing loginMenuView's overlay so both surfaces read as one family.
func (m model) modelMenuView(background string, w, h int) string {
	choices := m.modelChoices()
	var body []string
	if m.models.step == modelStepCustom {
		prompt := m.models.provider + "/" + m.models.customInput
		body = append(body, accentStyle.Render("› ")+titleStyle.Render(prompt)+accentStyle.Render("▏"))
	}
	if m.models.loading && m.models.step == modelStepModel {
		body = append(body, mutedStyle.Render("Loading models…"))
	}
	if m.models.fetchFailed && m.models.step == modelStepModel {
		body = append(body, mutedStyle.Render("Live list unavailable; showing built-in models"))
	}
	rows := make([]menuRow, len(choices))
	for i, choice := range choices {
		rows[i] = menuRow{label: choice, tag: m.modelChoiceTag(choice)}
	}
	empty := ""
	if m.models.step != modelStepCustom {
		empty = m.modelEmptyHint()
	}
	footer := "j/k select · enter next · f1 help · esc back"
	if m.models.step == modelStepCustom {
		footer = "enter save · f1 help · esc back · ctrl+c quit"
	}
	mw := min(52, w-8) + 4
	mh := min(len(choices)+8, h-2)
	if m.models.step == modelStepCustom {
		mh = min(9, h-2)
	}
	return menuView(background, w, h, "MODEL", m.modelMenuTitle(), body, rows, m.models.index, empty, footer, mw, mh)
}

// modelChoiceTag is the muted right-hand note for a row: the class's current
// model, the provider state, or the promise of free-text entry.
func (m model) modelChoiceTag(choice string) string {
	switch m.models.step {
	case modelStepClass:
		current := orchestrator.ModelSelection{}
		if m.service != nil {
			current = m.service.ModelSelection()
		}
		model := current.Small
		if choice == orchestrator.ModelClassBig {
			model = current.Big
		}
		if model == "" {
			return mutedStyle.Render("default")
		}
		return accentStyle.Render(model) + modelRateSuffix(model, time.Now())
	case modelStepModel:
		if choice == customModelChoice {
			return mutedStyle.Render("type any ID")
		}
		return modelRateTag(m.models.provider+"/"+choice, time.Now())
	}
	return ""
}

// modelRateLabel names the price in force for a selector now: "off-peak" or
// "peak" for a provider with a daily discount window, and empty for a model
// priced the same all day or not priced at all. The window belongs to the
// orchestrator's price table, so the terminal only reports the rate it names
// instead of deciding when a discount applies.
func modelRateLabel(selector string, now time.Time) string {
	price, known := orchestrator.PriceFor(selector, now)
	if !known || !price.TimeOfDay {
		return ""
	}
	return price.Rate(now)
}

// modelRateTag styles a model row's pricing window. The discounted window is
// the one worth noticing while choosing a model, so it takes the accent and the
// list price stays muted.
func modelRateTag(selector string, now time.Time) string {
	switch modelRateLabel(selector, now) {
	case orchestrator.RateOffPeak:
		return accentStyle.Render(orchestrator.RateOffPeak)
	case orchestrator.RatePeak:
		return mutedStyle.Render(orchestrator.RatePeak)
	}
	return ""
}

// modelRateSuffix appends the current pricing window to an already-styled
// selector, so a pinned model shows which rate it is charged at right now.
func modelRateSuffix(selector string, now time.Time) string {
	label := modelRateLabel(selector, now)
	if label == "" {
		return ""
	}
	return mutedStyle.Render(" · ") + modelRateTag(selector, now) + mutedStyle.Render(" now")
}

// modelEmptyHint explains an empty step so the operator knows what to fix.
func (m model) modelEmptyHint() string {
	switch m.models.step {
	case modelStepClass:
		return "No classes available."
	case modelStepProvider:
		return "No provider configured — press L to log in."
	default:
		return "No curated models; use custom…"
	}
}

// agentClasses lists the built-in agents and their current class for the
// per-agent big/small menu.
func (m model) agentClasses() []orchestrator.AgentClass {
	if m.service == nil {
		return nil
	}
	return m.service.AgentClasses()
}

// agentClassMenuKey drives the per-agent big/small menu. Enter toggles the
// selected agent's class; nothing is written unless the operator toggles one.
func (m model) agentClassMenuKey(key string) (model, tea.Cmd) {
	agents := m.agentClasses()
	last := len(agents) - 1
	switch key {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc":
		m.agentClass.menu = false
		m.notice.text = "Agent classes closed"
		return m, nil
	case "up", "k", "down", "j":
		moveMenuIndex(key, &m.agentClass.index, len(agents))
		return m, nil
	case "enter":
		if len(agents) == 0 {
			return m, nil
		}
		agent := agents[max(0, min(last, m.agentClass.index))]
		class := orchestrator.ModelClassBig
		if agent.Class == orchestrator.ModelClassBig {
			class = orchestrator.ModelClassSmall
		}
		if m.service == nil {
			m.notice.text = "Agent classes are not connected to a service."
			return m, nil
		}
		if err := m.service.SetAgentClass(agent.Agent, class); err != nil {
			m.notice.text = err.Error()
			return m, nil
		}
		m.notice.text = agent.Agent + " uses the " + class + " model"
		return m, nil
	}
	return m, nil
}

// agentClassMenuView draws the per-agent big/small menu as a centered modal.
func (m model) agentClassMenuView(background string, w, h int) string {
	agents := m.agentClasses()
	rows := make([]menuRow, len(agents))
	for i, agent := range agents {
		tag := mutedStyle.Render(agent.Class)
		if agent.Overridden {
			tag = accentStyle.Render(agent.Class)
		}
		rows[i] = menuRow{label: agent.Agent, tag: tag}
	}
	mw, mh := min(76, w-4), min(28, h-2)
	return menuView(background, w, h, "AGENTS", "big / small model", nil, rows, m.agentClass.index, "", "j/k select · enter toggle · f1 help · esc close", mw, mh)
}
