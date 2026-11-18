package tui

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

type modelStep int

const (
	modelStepClass modelStep = iota
	modelStepAgent
	modelStepProvider
	modelStepModel
	modelStepCustom
)
const customModelChoice = "custom…"

var modelClasses = []string{orchestrator.ModelClassBig, orchestrator.ModelClassSmall}

func (m model) modelChoices() []string {
	switch m.models.step {
	case modelStepClass:
		return append(append([]string(nil), modelClasses...), "Agents…")
	case modelStepAgent:
		names := make([]string, 0)
		for _, agent := range m.service.ListAgents() {
			names = append(names, agent.Name)
		}
		return names
	case modelStepProvider:
		var names []string
		for _, c := range m.service.ProviderCredentials() {
			if c.Configured {
				names = append(names, c.Provider)
			}
		}
		return names
	case modelStepModel:
		if m.models.listProvider == m.models.provider && len(m.models.list) > 0 {
			return append(append([]string(nil), m.models.list...), customModelChoice)
		}
		if m.models.loading {
			return nil
		}
		return append(m.service.ModelsForProvider(m.models.provider), customModelChoice)
	}
	return nil
}
func (m model) modelMenuTitle() string {
	switch m.models.step {
	case modelStepProvider:
		return "Select a provider for " + m.models.class
	case modelStepModel:
		return "Select a model (" + m.models.provider + ")"
	case modelStepCustom:
		return "Enter a model ID (" + m.models.provider + ")"
	}
	switch m.models.step {
	case modelStepAgent:
		return "Select an agent"
	}
	if m.models.step == modelStepClass {
		return "Select a model class or agent"
	}
	return "Select a model class"
}
func (m *model) openModelMenu(class string) {
	m.models = modelMenuState{menu: true, class: class}
	m.router.open = false
	m.notice.text = "Select a model class or agent"
	if class == orchestrator.ModelClassBig || class == orchestrator.ModelClassSmall {
		m.models.step = modelStepProvider
		m.notice.text = "Select a provider for " + class
	} else if class != "" {
		m.models.agent = class
		m.notice.text = "Select a model class for " + class
	}
}
func (m model) modelMenuKey(key string) (model, tea.Cmd) {
	if m.models.step == modelStepCustom {
		return m.modelCustomKey(key), nil
	}
	choices := m.modelChoices()
	if m.listMenuKey(key, &m.models.index, len(choices)) {
		return m, nil
	}
	switch key {
	case "esc":
		if m.models.step == modelStepAgent && m.models.agent != "" {
			m.models.agent, m.models.class, m.models.index = "", "", 0
			return m, nil
		}
		if m.models.step == modelStepClass || m.models.step == modelStepAgent {
			m.models.menu = false
			m.notice.text = "Model menu closed"
			return m, nil
		}
		m.models.step--
		m.models.index = 0
	case "enter":
		if len(choices) == 0 {
			m.notice.text = "Nothing to choose"
			return m, nil
		}
		return m.selectModelChoice(choices[max(0, min(len(choices)-1, m.models.index))])
	}
	return m, nil
}
func (m model) selectModelChoice(choice string) (model, tea.Cmd) {
	switch m.models.step {
	case modelStepClass:
		if choice == "Agents…" {
			m.models.step = modelStepAgent
			m.models.index = 0
			m.notice.text = "Select an agent"
			return m, nil
		}
		if m.models.agent != "" {
			m.models.menu = false
			m.notice.text = resultFor("Model class for "+m.models.agent+": "+choice, m.service.SetAgentModel(m.models.agent, choice)).text()
			return m, nil
		}
		m.models.class = choice
		m.models.step = modelStepProvider
		m.models.index = 0
		m.notice.text = "Select a provider for " + choice
	case modelStepAgent:
		m.models.agent = choice
		m.models.index = 0
		m.models.step = modelStepClass
		m.models.class = "agent:" + choice
		m.notice.text = "Select a model class for " + choice
	case modelStepProvider:
		m.models = modelMenuState{menu: true, step: modelStepModel, class: m.models.class, agent: m.models.agent, provider: choice, loading: true}
		return m, fetchModelsCmd(m.service, choice)
	case modelStepModel:
		if choice == customModelChoice {
			m.models.step = modelStepCustom
			return m, nil
		}
		return m.applyModelSelection(m.models.provider + "/" + choice), nil
	}
	return m, nil
}
func (m model) applyModelSelection(selector string) model {
	class, agent := m.models.class, m.models.agent
	m.models.menu = false
	notice := "Model for " + class + ": " + selector
	if agent != "" {
		notice = "Model for " + agent + ": " + selector
	}
	if label := modelRateLabel(selector, time.Now()); label != "" {
		notice += " · " + label + " now"
	}
	if agent != "" {
		m.notice.text = resultFor(notice, m.service.SetAgentModel(agent, selector)).text()
	} else {
		m.notice.text = resultFor(notice, m.service.SetModel(class, selector)).text()
	}
	return m
}
func (m *model) fetchedModels(msg fetchModelsMsg) {
	if msg.provider != m.models.provider {
		return
	}
	m.models.loading = false
	m.models.fetchFailed = msg.err != nil || len(msg.models) == 0
	if !m.models.fetchFailed {
		m.models.list, m.models.listProvider = msg.models, msg.provider
		return
	}
	m.models.list, m.models.listProvider = nil, ""
	reason := "empty list"
	if msg.err != nil {
		reason = msg.err.Error()
	}
	m.notice.text = "Could not fetch " + msg.provider + " models: " + reason
}
func fetchModelsCmd(service Service, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		models, err := service.FetchModels(ctx, provider)
		return fetchModelsMsg{provider: provider, models: models, err: err}
	}
}
func (m model) modelCustomKey(key string) model {
	switch key {
	case "ctrl+c":
		return m.requestQuit()
	case "enter":
		id := strings.TrimSpace(m.models.customInput)
		if id == "" {
			m.notice.text = "Enter a model ID"
			return m
		}
		return m.applyModelSelection(m.models.provider + "/" + id)
	case "esc":
		m.models.step = modelStepModel
		m.models.index = 0
		m.models.customInput = ""
	case "backspace", "ctrl+h":
		m.models.customInput = trimLastRune(m.models.customInput)
	default:
		if modelTextKey(key) {
			m.models.customInput += key
		}
	}
	return m
}
func modelTextKey(key string) bool {
	if key == "" || strings.Contains(key, "+") || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return false
	}
	switch key {
	case "tab", "enter", "esc", "backspace", "delete", "insert", "space", "home", "end", "up", "down", "left", "right", "pgup", "pgdown":
		return false
	}
	return true
}
func (m model) modelMenuView(background string, w, h int) string {
	choices := m.modelChoices()
	var body []string
	empty, footer := m.modelEmptyHint(), "j/k select · enter next · f1 help · esc back"
	mh := min(len(choices)+8, h-2)
	if m.models.step == modelStepCustom {
		body = append(body, accentStyle.Render("› ")+titleStyle.Render(m.models.provider+"/"+m.models.customInput)+cursorGlyph())
		empty, footer, mh = "", "enter save · f1 help · esc back · ctrl+c quit", min(9, h-2)
	} else if m.models.step == modelStepModel && m.models.loading {
		body = append(body, mutedStyle.Render("Loading models…"))
	} else if m.models.step == modelStepModel && m.models.fetchFailed {
		body = append(body, mutedStyle.Render("Live list unavailable; showing built-in models"))
	}
	rows := make([]menuRow, len(choices))
	for i, c := range choices {
		rows[i] = menuRow{label: c, tag: m.modelChoiceTag(c)}
	}
	return menuView(background, w, h, "MODEL", m.modelMenuTitle(), body, rows, m.models.index, empty, footer, min(52, w-8)+4, mh)
}
func (m model) modelChoiceTag(choice string) string {
	switch m.models.step {
	case modelStepAgent:
		for _, agent := range m.service.ListAgents() {
			if agent.Name == choice {
				if class := m.service.ModelSelection().Agents[choice]; class != "" {
					return accentStyle.Render(class)
				}
			}
		}
		return ""
	case modelStepClass:
		current := m.service.ModelSelection()
		if choice == orchestrator.ModelClassBig && current.Big != "" {
			if tag := modelRateTag(current.Big, time.Now()); tag != "" {
				return accentStyle.Render(current.Big) + mutedStyle.Render(" · ") + tag + mutedStyle.Render(" now")
			}
			return accentStyle.Render(current.Big)
		}
		if choice == orchestrator.ModelClassSmall && current.Small != "" {
			if tag := modelRateTag(current.Small, time.Now()); tag != "" {
				return accentStyle.Render(current.Small) + mutedStyle.Render(" · ") + tag + mutedStyle.Render(" now")
			}
			return accentStyle.Render(current.Small)
		}
		if (choice == orchestrator.ModelClassBig && current.Big == "") || (choice == orchestrator.ModelClassSmall && current.Small == "") {
			return mutedStyle.Render("default")
		}
		return ""
	case modelStepModel:
		if choice == customModelChoice {
			return mutedStyle.Render("type any ID")
		}
		return modelRateTag(m.models.provider+"/"+choice, time.Now())
	}
	return ""
}
func modelRateLabel(selector string, now time.Time) string {
	price, known := orchestrator.PriceFor(selector, now)
	if !known || !price.TimeOfDay {
		return ""
	}
	return price.Rate(now)
}
func modelRateTag(selector string, now time.Time) string {
	switch modelRateLabel(selector, now) {
	case orchestrator.RateOffPeak:
		return accentStyle.Render(orchestrator.RateOffPeak)
	case orchestrator.RatePeak:
		return mutedStyle.Render(orchestrator.RatePeak)
	}
	return ""
}
func (m model) modelEmptyHint() string {
	switch m.models.step {
	case modelStepAgent:
		return "No agents available."
	case modelStepProvider:
		return "No provider configured; type /login to log in."
	}
	return "No curated models; use custom…"
}
