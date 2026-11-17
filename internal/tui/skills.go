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
}

// openSkillMenu shows every installed skill, enabled or not.
func (m *model) openSkillMenu() {
	m.skills = skillMenuState{open: true}
	m.router.open = false
	m.notice.text = "Skills: enter toggles the selected skill."
}

func (m model) skillRows() []orchestrator.SkillInfo {
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
	case "enter", " ":
		if len(skills) == 0 {
			return m, nil
		}
		skill := skills[max(0, min(len(skills)-1, m.skills.index))]
		enabled := !skill.Enabled
		service := m.service
		return m, func() tea.Msg {
			return skillToggleMsg{name: skill.Name, enabled: enabled, err: service.SetSkillEnabled(skill.Name, enabled)}
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
	return menuView(background, w, h, "SKILLS", "enter toggles · esc closes", body, rows, m.skills.index, "No skills installed.", "j/k select · enter toggle · esc close", mw, mh)
}
