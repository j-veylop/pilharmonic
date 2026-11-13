package tui

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
		m.notice = m.providerCredentialNotice()
	case "/login":
		provider := ""
		if len(fields) > 1 {
			provider = fields[1]
		}
		if provider == "" {
			m.loginMenu = true
			m.loginIndex = 0
			m.router = false
			m.routerDraft, m.routerCursor, m.routerScroll = "", 0, 0
			m.notice = "Select a provider"
			return m
		}
		m.loginProvider = provider
		m.loginKey = ""
		m.notice = "Enter API key for " + provider + " (Enter to save, Esc to cancel)"
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
			m.notice = "Unknown class: " + class + " · use big or small"
			return m
		}
		m.openModelMenu(class)
		return m
	case "/agents":
		m.agentClassMenu = true
		m.agentClassIndex = 0
		m.router = false
		m.routerDraft, m.routerCursor, m.routerScroll = "", 0, 0
		m.notice = "Select an agent to toggle big/small"
		return m
	case "/logout":
		if len(fields) < 2 || fields[1] == "" {
			m.notice = "Usage: /logout <provider>"
			return m
		}
		m.notice = m.removeProviderCredential(fields[1])
	case "/help":
		// The overlay documents the slash vocabulary too, so /help opens the
		// real help menu instead of a one-line notice.
		return m.openHelp()
	default:
		m.notice = "Unknown command · " + slashCommandsHelp
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
		m.loginProvider, m.loginKey = "", ""
		m.notice = "Login canceled"
		return m, nil
	case "ctrl+c":
		return m.requestQuit(), nil
	case "backspace", "ctrl+h":
		m.loginKey = trimLastRune(m.loginKey)
		return m, nil
	}
	if len(msg.Runes) > 0 {
		m.loginKey += string(msg.Runes)
	}
	return m, nil
}

// submitLoginKey stores the typed key and leaves login mode. The notice names
// the provider only, never the key.
func (m model) submitLoginKey() (model, tea.Cmd) {
	provider, key := m.loginProvider, m.loginKey
	m.loginProvider, m.loginKey = "", ""
	m.routerDraft, m.routerCursor, m.routerScroll = "", 0, 0
	if m.service == nil {
		m.notice = "Provider login is not connected to a service."
		return m, nil
	}
	if err := m.service.SetProviderCredential(provider, key); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.notice = "Stored API key for " + provider
	return m, nil
}

// loginComposerLines renders the masked key prompt in place of the request
// draft, keeping the composer's single-row shape.
func (m model) loginComposerLines(width int) []string {
	masked := "(hidden)"
	if m.loginKey != "" {
		masked = strings.Repeat("*", utf8.RuneCountInString(m.loginKey))
	}
	line := "Enter API key for " + terminalText(m.loginProvider) + ": " + masked
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
		m.loginMenu = false
		if m.anyProviderConfigured() {
			m.notice = "Login closed"
		} else {
			m.notice = "No provider configured"
		}
		return m, nil
	case "up", "k":
		m.loginIndex = max(0, min(last, m.loginIndex-1))
		return m, nil
	case "down", "j":
		m.loginIndex = max(0, min(last, m.loginIndex+1))
		return m, nil
	case "enter":
		if len(credentials) == 0 {
			m.notice = "No providers known"
			return m, nil
		}
		provider := credentials[max(0, min(last, m.loginIndex))].Provider
		m.loginMenu = false
		m.loginProvider = provider
		m.loginKey = ""
		// The masked key prompt lives in the request composer, so key entry
		// always happens there regardless of where the menu was opened.
		m.router = true
		m.routerFocus = routerFocusComposer
		m.routerDraft, m.routerCursor, m.routerScroll = "", 0, 0
		m.notice = "Enter API key for " + provider + " (Enter to save, Esc to cancel)"
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
	inner := min(52, w-8)
	mh := min(len(credentials)+8, h-2)
	if inner < 12 || mh < 6 {
		return fit(background, w, h)
	}
	mw := inner + 4
	selected := max(0, min(len(credentials)-1, m.loginIndex))

	var content []string
	content = append(content, accentStyle.Render("LOGIN")+mutedStyle.Render("  Select a provider"), "")
	if len(credentials) == 0 {
		content = append(content, mutedStyle.Render("No providers known."))
	}
	// Window the list around the selection so the footer hint survives a
	// terminal too short for every provider.
	rows := max(1, mh-5)
	start := 0
	if selected >= rows {
		start = min(selected-rows+1, max(0, len(credentials)-rows))
	}
	for i := start; i < len(credentials) && i < start+rows; i++ {
		credential := credentials[i]
		marker, name, tag := "  ", mutedStyle.Render(credential.Provider), mutedStyle.Render("no key")
		if i == selected {
			marker, name = accentStyle.Render("› "), titleStyle.Render(credential.Provider)
		}
		if credential.Configured {
			tag = accentStyle.Render("configured")
		}
		plain := 2 + lipgloss.Width(credential.Provider) + lipgloss.Width(ansi.Strip(tag))
		content = append(content, marker+name+strings.Repeat(" ", max(1, inner-plain))+tag)
	}
	for len(content) < mh-3 {
		content = append(content, "")
	}
	content = append(content, accentStyle.Render("j/k select · enter login · f1 help · esc close"))
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, h)
	for row := range result {
		line := ""
		if row < len(bg) {
			line = ansi.Strip(bg[row])
		}
		line = lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
		if row >= y && row < y+len(box) {
			result[row] = mutedStyle.Faint(true).Render(ansi.Cut(line, 0, x)) + box[row-y] + mutedStyle.Faint(true).Render(ansi.Cut(line, x+mw, w))
		} else {
			result[row] = mutedStyle.Faint(true).Render(line)
		}
	}
	return fit(strings.Join(result, "\n"), w, h)
}

// Model menu steps. The menu walks agent -> provider -> model, with a free-text
// step for model IDs the curated catalog does not list.
const (
	modelStepClass = iota
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
	return name == orchestrator.ModelClassBig || name == orchestrator.ModelClassSmall
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

// modelsForProvider lists the curated model IDs for a provider prefix.
func (m model) modelsForProvider(provider string) []string {
	if m.service == nil {
		return nil
	}
	return m.service.ModelsForProvider(provider)
}

// modelChoices lists the selectable rows for the current step.
func (m model) modelChoices() []string {
	switch m.modelStep {
	case modelStepProvider:
		credentials := m.configuredProviders()
		names := make([]string, 0, len(credentials))
		for _, credential := range credentials {
			names = append(names, credential.Provider)
		}
		return names
	case modelStepModel:
		if m.modelListProvider == m.modelProvider && len(m.modelList) > 0 {
			return append(append([]string(nil), m.modelList...), customModelChoice)
		}
		if m.modelsLoading {
			return nil
		}
		models := m.modelsForProvider(m.modelProvider)
		return append(models, customModelChoice)
	case modelStepCustom:
		return nil
	default:
		return m.modelClasses()
	}
}

// modelMenuTitle is the heading for the current step.
func (m model) modelMenuTitle() string {
	switch m.modelStep {
	case modelStepProvider:
		return "Select a provider for " + m.modelClass
	case modelStepModel:
		return "Select a model (" + m.modelProvider + ")"
	case modelStepCustom:
		return "Enter a model ID (" + m.modelProvider + ")"
	default:
		return "Select a model class"
	}
}

// openModelMenu arms the menu, optionally on the provider step for a class
// named by /model <class>. Any open request form is cleared so the menu owns
// the keyboard.
func (m *model) openModelMenu(class string) {
	m.modelMenu = true
	m.modelIndex = 0
	m.modelCustomInput = ""
	m.modelProvider = ""
	m.router = false
	m.routerDraft, m.routerCursor, m.routerScroll = "", 0, 0
	if class == "" {
		m.modelClass = ""
		m.modelStep = modelStepClass
		m.notice = "Select a model class"
		return
	}
	m.modelClass = class
	m.modelStep = modelStepProvider
	m.notice = "Select a provider for " + class
}

// modelMenuKey drives the agent -> provider -> model menu. Selections reach the
// service only on the final step, so an abandoned menu changes nothing.
func (m model) modelMenuKey(key string) (model, tea.Cmd) {
	if m.modelStep == modelStepCustom {
		return m.modelCustomKey(key)
	}
	choices := m.modelChoices()
	last := len(choices) - 1
	switch key {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc":
		if m.modelStep == modelStepClass {
			m.modelMenu = false
			m.notice = "Model menu closed"
			return m, nil
		}
		m.modelStep--
		m.modelIndex = 0
		m.notice = ""
		return m, nil
	case "up", "k":
		m.modelIndex = max(0, min(last, m.modelIndex-1))
		return m, nil
	case "down", "j":
		m.modelIndex = max(0, min(last, m.modelIndex+1))
		return m, nil
	case "enter":
		if len(choices) == 0 {
			m.notice = "Nothing to choose"
			return m, nil
		}
		return m.selectModelChoice(choices[max(0, min(last, m.modelIndex))])
	}
	return m, nil
}

// selectModelChoice advances the menu or, on the model step, pins the agent to
// the chosen provider/model selector.
func (m model) selectModelChoice(choice string) (model, tea.Cmd) {
	switch m.modelStep {
	case modelStepClass:
		m.modelClass = choice
		m.modelStep = modelStepProvider
		m.modelIndex = 0
		m.notice = "Select a provider for " + choice
		return m, nil
	case modelStepProvider:
		m.modelProvider = choice
		m.modelStep = modelStepModel
		m.modelIndex = 0
		m.notice = ""
		m.modelList = nil
		m.modelListProvider = ""
		m.modelsLoading = true
		return m, m.fetchModelsCmd(choice)
	case modelStepModel:
		if choice == customModelChoice {
			m.modelStep = modelStepCustom
			m.modelCustomInput = ""
			return m, nil
		}
		return m.applyModelSelection(m.modelProvider + "/" + choice)
	}
	return m, nil
}

// applyModelSelection pins the chosen class and closes the menu. The notice
// names the selector, never a credential.
func (m model) applyModelSelection(selector string) (model, tea.Cmd) {
	class := m.modelClass
	m.modelMenu = false
	m.modelStep = modelStepClass
	m.modelIndex = 0
	m.modelCustomInput = ""
	if m.service == nil {
		m.notice = "Model selection is not connected to a service."
		return m, nil
	}
	if err := m.service.SetModel(class, selector); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.notice = "Model for " + class + ": " + selector
	return m, nil
}

// fetchModelsCmd fetches a provider's current model list from its API endpoint
// and delivers it as a fetchModelsMsg. The curated catalog is the fallback on
// error, so a failed fetch never blocks model selection.
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
		modelID := strings.TrimSpace(m.modelCustomInput)
		if modelID == "" {
			m.notice = "Enter a model ID"
			return m, nil
		}
		return m.applyModelSelection(m.modelProvider + "/" + modelID)
	case "esc":
		m.modelStep = modelStepModel
		m.modelIndex = 0
		m.modelCustomInput = ""
		return m, nil
	case "backspace", "ctrl+h":
		m.modelCustomInput = trimLastRune(m.modelCustomInput)
		return m, nil
	}
	if modelTextKey(key) {
		m.modelCustomInput += key
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
	inner := min(52, w-8)
	mh := min(len(choices)+8, h-2)
	if m.modelStep == modelStepCustom {
		mh = min(9, h-2)
	}
	if inner < 12 || mh < 6 {
		return fit(background, w, h)
	}
	mw := inner + 4
	selected := max(0, min(len(choices)-1, m.modelIndex))

	var content []string
	content = append(content, accentStyle.Render("MODEL")+mutedStyle.Render("  "+m.modelMenuTitle()), "")
	if m.modelStep == modelStepCustom {
		prompt := m.modelProvider + "/" + m.modelCustomInput
		content = append(content, accentStyle.Render("› ")+titleStyle.Render(prompt)+accentStyle.Render("▏"))
	}
	if m.modelsLoading && m.modelStep == modelStepModel {
		content = append(content, mutedStyle.Render("Loading models…"))
	}
	if len(choices) == 0 && m.modelStep != modelStepCustom {
		content = append(content, mutedStyle.Render(m.modelEmptyHint()))
	}
	// Window the list around the selection so the footer hint survives a
	// terminal too short for every choice.
	rows := max(1, mh-5)
	start := 0
	if selected >= rows {
		start = min(selected-rows+1, max(0, len(choices)-rows))
	}
	for i := start; i < len(choices) && i < start+rows; i++ {
		choice := choices[i]
		marker, name := "  ", mutedStyle.Render(choice)
		if i == selected {
			marker, name = accentStyle.Render("› "), titleStyle.Render(choice)
		}
		// Drop the tag rather than crowd out the name in a narrow modal.
		tag := m.modelChoiceTag(choice)
		plain := 2 + lipgloss.Width(choice) + lipgloss.Width(ansi.Strip(tag))
		if tag == "" || plain >= inner {
			content = append(content, marker+name)
			continue
		}
		content = append(content, marker+name+strings.Repeat(" ", inner-plain)+tag)
	}
	for len(content) < mh-3 {
		content = append(content, "")
	}
	if m.modelStep == modelStepCustom {
		content = append(content, accentStyle.Render("enter save · f1 help · esc back · ctrl+c quit"))
	} else {
		content = append(content, accentStyle.Render("j/k select · enter next · f1 help · esc back"))
	}
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, h)
	for row := range result {
		line := ""
		if row < len(bg) {
			line = ansi.Strip(bg[row])
		}
		line = lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
		if row >= y && row < y+len(box) {
			result[row] = mutedStyle.Faint(true).Render(ansi.Cut(line, 0, x)) + box[row-y] + mutedStyle.Faint(true).Render(ansi.Cut(line, x+mw, w))
		} else {
			result[row] = mutedStyle.Faint(true).Render(line)
		}
	}
	return fit(strings.Join(result, "\n"), w, h)
}

// modelChoiceTag is the muted right-hand note for a row: the class's current
// model, the provider state, or the promise of free-text entry.
func (m model) modelChoiceTag(choice string) string {
	switch m.modelStep {
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
		return accentStyle.Render(model)
	case modelStepModel:
		if choice == customModelChoice {
			return mutedStyle.Render("type any ID")
		}
	}
	return ""
}

// modelEmptyHint explains an empty step so the operator knows what to fix.
func (m model) modelEmptyHint() string {
	switch m.modelStep {
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
		m.agentClassMenu = false
		m.notice = "Agent classes closed"
		return m, nil
	case "up", "k":
		m.agentClassIndex = max(0, min(last, m.agentClassIndex-1))
		return m, nil
	case "down", "j":
		m.agentClassIndex = max(0, min(last, m.agentClassIndex+1))
		return m, nil
	case "enter":
		if len(agents) == 0 {
			return m, nil
		}
		agent := agents[max(0, min(last, m.agentClassIndex))]
		class := orchestrator.ModelClassBig
		if agent.Class == orchestrator.ModelClassBig {
			class = orchestrator.ModelClassSmall
		}
		if m.service == nil {
			m.notice = "Agent classes are not connected to a service."
			return m, nil
		}
		if err := m.service.SetAgentClass(agent.Agent, class); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		m.notice = agent.Agent + " uses the " + class + " model"
		return m, nil
	}
	return m, nil
}

// agentClassMenuView draws the per-agent big/small menu as a centered modal.
func (m model) agentClassMenuView(background string, w, h int) string {
	agents := m.agentClasses()
	mw, mh := min(76, w-4), min(28, h-2)
	inner := mw - 4
	content := []string{
		accentStyle.Render("AGENTS") + mutedStyle.Render("  big / small model"),
		"",
	}
	selected := max(0, min(len(agents)-1, m.agentClassIndex))
	rows := max(1, mh-5)
	start := 0
	if selected >= rows {
		start = min(selected-rows+1, max(0, len(agents)-rows))
	}
	for i := start; i < len(agents) && i < start+rows; i++ {
		agent := agents[i]
		marker, name := "  ", mutedStyle.Render(agent.Agent)
		if i == selected {
			marker, name = accentStyle.Render("› "), titleStyle.Render(agent.Agent)
		}
		tag := mutedStyle.Render(agent.Class)
		if agent.Overridden {
			tag = accentStyle.Render(agent.Class)
		}
		content = append(content, marker+name+strings.Repeat(" ", inner-lipgloss.Width(agent.Agent)-lipgloss.Width(ansi.Strip(tag)))+tag)
	}
	for len(content) < mh-3 {
		content = append(content, "")
	}
	content = append(content, accentStyle.Render("j/k select · enter toggle · f1 help · esc close"))
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, h)
	for row := range result {
		line := ""
		if row < len(bg) {
			line = ansi.Strip(bg[row])
		}
		line = lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
		if row >= y && row < y+len(box) {
			result[row] = mutedStyle.Faint(true).Render(ansi.Cut(line, 0, x)) + box[row-y] + mutedStyle.Faint(true).Render(ansi.Cut(line, x+mw, w))
		} else {
			result[row] = mutedStyle.Faint(true).Render(line)
		}
	}
	return fit(strings.Join(result, "\n"), w, h)
}
