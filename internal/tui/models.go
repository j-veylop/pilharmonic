package tui

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// Model menu steps: class -> provider -> model, with a free-text step for
// model IDs the curated catalog does not list.
type modelStep int

const (
	modelStepClass modelStep = iota
	modelStepProvider
	modelStepModel
	modelStepCustom
)

// customModelChoice is the sentinel row that opens free-text model entry.
const customModelChoice = "custom…"

var modelClasses = []string{orchestrator.ModelClassBig, orchestrator.ModelClassSmall}

// modelChoices lists the selectable rows for the current step. Only providers
// with a key can serve a model.
func (m model) modelChoices() []string {
	switch m.models.step {
	case modelStepProvider:
		var names []string
		for _, credential := range m.service.ProviderCredentials() {
			if credential.Configured {
				names = append(names, credential.Provider)
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
	case modelStepCustom:
		return nil
	}
	return modelClasses
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
	return "Select a model class"
}

// openModelMenu arms the menu, on the provider step when /model names a class.
func (m *model) openModelMenu(class string) {
	m.models = modelMenuState{menu: true, class: class}
	m.router.open = false
	m.notice.text = "Select a model class"
	if class != "" {
		m.models.step = modelStepProvider
		m.notice.text = "Select a provider for " + class
	}
}

// modelMenuKey drives the model menu. Selections reach the service only on the
// final step, so an abandoned menu changes nothing.
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
		if m.models.step == modelStepClass {
			m.models.menu = false
			m.notice.text = "Model menu closed"
			return m, nil
		}
		m.models.step--
		m.models.index = 0
		m.notice.text = ""
	case "enter":
		if len(choices) == 0 {
			m.notice.text = "Nothing to choose"
			return m, nil
		}
		return m.selectModelChoice(choices[max(0, min(len(choices)-1, m.models.index))])
	}
	return m, nil
}

// selectModelChoice advances the menu or, on the model step, pins the class to
// the chosen provider/model selector.
func (m model) selectModelChoice(choice string) (model, tea.Cmd) {
	switch m.models.step {
	case modelStepClass:
		m.models.class, m.models.step, m.models.index = choice, modelStepProvider, 0
		m.notice.text = "Select a provider for " + choice
	case modelStepProvider:
		m.models = modelMenuState{menu: true, step: modelStepModel, class: m.models.class, provider: choice, loading: true}
		m.notice.text = ""
		return m, fetchModelsCmd(m.service, choice)
	case modelStepModel:
		if choice == customModelChoice {
			m.models.step, m.models.customInput = modelStepCustom, ""
			return m, nil
		}
		return m.applyModelSelection(m.models.provider + "/" + choice), nil
	}
	return m, nil
}

// applyModelSelection pins the chosen class and closes the menu. A provider
// with a daily discount window is labelled with the rate in force now, because
// that is the price the next attempt will be charged.
func (m model) applyModelSelection(selector string) model {
	class := m.models.class
	m.models.menu, m.models.step, m.models.index, m.models.customInput = false, modelStepClass, 0, ""
	notice := "Model for " + class + ": " + selector
	if label := modelRateLabel(selector, time.Now()); label != "" {
		notice += " · " + label + " now"
	}
	m.notice.text = resultFor(notice, m.service.SetModel(class, selector)).text()
	return m
}

// fetchedModels shows a provider's fetched list, or the built-in list and a
// notice when the fetch failed.
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

// fetchModelsCmd fetches a provider's live model list off the update loop. The
// curated catalog is the labelled fallback, so a failed fetch never blocks
// model selection.
func fetchModelsCmd(service Service, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		models, err := service.FetchModels(ctx, provider)
		return fetchModelsMsg{provider: provider, models: models, err: err}
	}
}

// modelCustomKey captures keys while a model ID is typed.
func (m model) modelCustomKey(key string) model {
	switch key {
	case "ctrl+c":
		return m.requestQuit()
	case "enter":
		modelID := strings.TrimSpace(m.models.customInput)
		if modelID == "" {
			m.notice.text = "Enter a model ID"
			return m
		}
		return m.applyModelSelection(m.models.provider + "/" + modelID)
	case "esc":
		m.models.step, m.models.index, m.models.customInput = modelStepModel, 0, ""
	case "backspace", "ctrl+h":
		m.models.customInput = trimLastRune(m.models.customInput)
	default:
		if modelTextKey(key) {
			m.models.customInput += key
		}
	}
	return m
}

// modelTextKey reports whether a key string is literal text. Named keys and
// modifier chords are not.
func modelTextKey(key string) bool {
	if key == "" || strings.Contains(key, "+") || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return false
	}
	switch key {
	case "tab", "enter", "esc", "backspace", "delete", "insert", "space",
		"home", "end", "up", "down", "left", "right", "pgup", "pgdown":
		return false
	}
	return true
}

func (m model) modelMenuView(background string, w, h int) string {
	choices := m.modelChoices()
	var body []string
	empty, footer := m.modelEmptyHint(), "j/k select · enter next · f1 help · esc back"
	mh := min(len(choices)+8, h-2)
	switch {
	case m.models.step == modelStepCustom:
		body = append(body, accentStyle.Render("› ")+titleStyle.Render(m.models.provider+"/"+m.models.customInput)+accentStyle.Render("▏"))
		empty, footer, mh = "", "enter save · f1 help · esc back · ctrl+c quit", min(9, h-2)
	case m.models.step == modelStepModel && m.models.loading:
		body = append(body, mutedStyle.Render("Loading models…"))
	case m.models.step == modelStepModel && m.models.fetchFailed:
		body = append(body, mutedStyle.Render("Live list unavailable; showing built-in models"))
	}
	rows := make([]menuRow, len(choices))
	for i, choice := range choices {
		rows[i] = menuRow{label: choice, tag: m.modelChoiceTag(choice)}
	}
	return menuView(background, w, h, "MODEL", m.modelMenuTitle(), body, rows, m.models.index, empty, footer, min(52, w-8)+4, mh)
}

// modelChoiceTag is a row's right-hand note: the class's current model, the
// current rate window, or the promise of free-text entry.
func (m model) modelChoiceTag(choice string) string {
	switch m.models.step {
	case modelStepClass:
		current := m.service.ModelSelection()
		model := current.Small
		if choice == orchestrator.ModelClassBig {
			model = current.Big
		}
		if model == "" {
			return mutedStyle.Render("default")
		}
		if tag := modelRateTag(model, time.Now()); tag != "" {
			return accentStyle.Render(model) + mutedStyle.Render(" · ") + tag + mutedStyle.Render(" now")
		}
		return accentStyle.Render(model)
	case modelStepModel:
		if choice == customModelChoice {
			return mutedStyle.Render("type any ID")
		}
		return modelRateTag(m.models.provider+"/"+choice, time.Now())
	}
	return ""
}

// modelRateLabel names the price window in force for a selector now, or ""
// for a model priced the same all day or not priced at all. The window belongs
// to the orchestrator's price table.
func modelRateLabel(selector string, now time.Time) string {
	price, known := orchestrator.PriceFor(selector, now)
	if !known || !price.TimeOfDay {
		return ""
	}
	return price.Rate(now)
}

// modelRateTag highlights the discounted window and mutes the list price.
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
	case modelStepClass:
		return "No classes available."
	case modelStepProvider:
		return "No provider configured; type /login to log in."
	}
	return "No curated models; use custom…"
}
