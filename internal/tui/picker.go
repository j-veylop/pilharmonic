package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

type pickerState[T any] struct {
	target  string
	choices []T
	index   int
}

func (picker *pickerState[T]) move(key string) {
	switch key {
	case "up", "k":
		if picker.index > 0 {
			picker.index--
		}
	case "down", "j":
		if picker.index < len(picker.choices)-1 {
			picker.index++
		}
	}
}

func (m model) profilePickerKey(key string) (model, tea.Cmd) {
	switch key {
	case "esc":
		m.profilePicker = pickerState[orchestrator.Profile]{}
	case "ctrl+c":
		return m.requestQuit(), nil
	case "enter":
		if len(m.profilePicker.choices) > 0 {
			profile := m.profilePicker.choices[m.profilePicker.index]
			if err := m.service.SetTaskProfile(m.profilePicker.target, profile.Name); err != nil {
				m.notice.text = err.Error()
			} else {
				m.notice.text = "Profile selected: " + profileLabel(profile)
			}
			m.refreshTasks()
		}
		m.profilePicker = pickerState[orchestrator.Profile]{}
	default:
		m.profilePicker.move(key)
	}
	return m, nil
}

func (m model) agentPickerKey(key string) (model, tea.Cmd) {
	switch key {
	case "esc":
		m.agentPicker = pickerState[orchestrator.AgentInfo]{}
	case "ctrl+c":
		return m.requestQuit(), nil
	case "enter":
		if len(m.agentPicker.choices) > 0 {
			name := m.agentPicker.choices[m.agentPicker.index].Name
			if err := m.service.SetTaskAgent(m.agentPicker.target, name); err != nil {
				m.notice.text = err.Error()
			} else {
				m.notice.text = "Agent selected: " + name
			}
			m.refreshTasks()
		}
		m.agentPicker = pickerState[orchestrator.AgentInfo]{}
	default:
		m.agentPicker.move(key)
	}
	return m, nil
}
