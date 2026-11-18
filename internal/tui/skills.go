package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// skillMenuState is the installed-skill toggle dialog. It holds presentation
// state only; the service owns the durable enabled set.
type skillMenuState struct {
	open  bool
	index int
	scope int
}

// openSkillMenu shows every installed skill, enabled or not.
func (m *model) openSkillMenu() {
	m.skills = skillMenuState{open: true}
	m.router.open = false
}

func (m model) skillAgent() string {
	agents := m.service.ListAgents()
	if m.skills.scope <= 0 || m.skills.scope > len(agents) {
		return ""
	}
	return agents[m.skills.scope-1].Name
}

func (m model) skillRows() []orchestrator.SkillInfo {
	if agent := m.skillAgent(); agent != "" {
		return m.service.ListAgentSkills(agent)
	}
	return m.service.ListSkills()
}

// skillMenuKey moves, toggles or closes the skill dialog. A toggle runs off the
// update loop; the list refreshes when the service reports back.
func (m model) skillMenuKey(key string) (model, tea.Cmd) {
	skills := m.skillRows()
	if m.listMenuKey(key, &m.skills.index, len(skills)) {
		return m, nil
	}
	switch key {
	case "esc", "q":
		m.skills = skillMenuState{}
		m.notice.text = "Skills closed."
	case "left", "right", "tab":
		count := len(m.service.ListAgents()) + 1
		if key == "left" {
			m.skills.scope = (m.skills.scope + count - 1) % count
		} else {
			m.skills.scope = (m.skills.scope + 1) % count
		}
		m.skills.index = 0
	case "enter", " ":
		if len(skills) == 0 {
			return m, nil
		}
		skill := skills[max(0, min(len(skills)-1, m.skills.index))]
		enabled := !skill.Enabled
		service := m.service
		agent := m.skillAgent()
		return m, func() tea.Msg {
			var err error
			if agent == "" {
				err = service.SetSkillEnabled(skill.Name, enabled)
			} else {
				err = service.SetAgentSkillEnabled(agent, skill.Name, enabled)
			}
			return skillToggleMsg{name: skill.Name, enabled: enabled, err: err}
		}
	}
	return m, nil
}

func (m *model) finishSkillToggle(msg skillToggleMsg) {
	if msg.err != nil {
		m.notice.text = "Skill update failed: " + msg.err.Error()
		return
	}
	state := "disabled"
	if msg.enabled {
		state = "enabled"
	}
	m.notice.text = "Skill " + msg.name + " " + state + " for the next message."
}

func (m model) skillMenuView(background string, w, h int) string {
	skills := m.skillRows()
	rows := make([]menuRow, len(skills))
	for i, skill := range skills {
		tag := mutedStyle.Render("disabled")
		if skill.Enabled {
			tag = accentStyle.Render("enabled")
		}
		if skill.GlobalDisabled {
			tag = mutedStyle.Render("global off")
		}
		rows[i] = menuRow{label: skill.Name, tag: tag}
	}
	mw := min(68, w-8) + 4
	mh := min(len(rows)+8, h-2)
	body := []string(nil)
	if len(skills) > 0 {
		selected := skills[max(0, min(len(skills)-1, m.skills.index))]
		if selected.Description != "" {
			body = append(body, mutedStyle.Render(fit(selected.Description, mw-4, 1)))
		}
	}
	scope := m.skillAgent()
	if scope == "" {
		scope = "all agents"
	}
	return menuView(background, w, h, "SKILLS", scope+" · tab changes agent", body, rows, m.skills.index, "No skills installed.", "j/k select · enter toggle · tab agent · esc close", mw, mh)
}
