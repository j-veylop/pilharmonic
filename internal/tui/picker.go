package tui

import "pilharmonic/internal/orchestrator"

// pickerState is a queued task's agent or profile chooser. target is captured
// when the picker opens.
type pickerState[T any] struct {
	target  string
	choices []T
	index   int
}

func openPicker[T any](p *pickerState[T], target string, choices []T, current func(T) bool) {
	*p = pickerState[T]{target: target, choices: choices}
	for i, choice := range choices {
		if current(choice) {
			p.index = i
		}
	}
}

// pickerKey drives an open picker. Enter hands the choice to apply, which
// returns the success notice and the service error.
func pickerKey[T any](m *model, p *pickerState[T], key string, apply func(target string, choice T) (string, error)) {
	switch key {
	case "ctrl+c":
		*m = m.requestQuit()
		return
	case "enter":
		if len(p.choices) > 0 {
			notice, err := apply(p.target, p.choices[p.index])
			m.notice.text = resultFor(notice, err).text()
			m.refreshTasks()
		}
	case "esc":
	default:
		moveMenuIndex(key, &p.index, len(p.choices))
		return
	}
	*p = pickerState[T]{}
}

func (m model) profilePickerKey(key string) model {
	pickerKey(&m, &m.profilePicker, key, func(id string, p orchestrator.Profile) (string, error) {
		return "Profile selected: " + profileLabel(p), m.service.SetTaskProfile(id, p.Name)
	})
	return m
}

func (m model) agentPickerKey(key string) model {
	pickerKey(&m, &m.agentPicker, key, func(id string, a orchestrator.AgentInfo) (string, error) {
		return "Agent selected: " + a.Name, m.service.SetTaskAgent(id, a.Name)
	})
	return m
}
