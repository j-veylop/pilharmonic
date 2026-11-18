package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
)

func TestSkillsAgentSelection(t *testing.T) {
	service := &fakeLoginService{agents: []orchestrator.AgentInfo{{Name: "general"}}, agentSkills: map[string][]orchestrator.SkillInfo{"general": {{Name: "verify", Enabled: true}}}}
	m := model{width: 80, height: 24, service: service}
	m.openSkillMenu()
	m = key(m, runeKey("tab"))
	for _, size := range [][2]int{{80, 24}, {40, 12}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		assertFits(t, view, size[0], size[1])
		if !strings.Contains(ansi.Strip(view), "general") {
			t.Fatalf("agent missing: %s", view)
		}
	}
	m = key(m, runeKey("enter"))
	if len(service.skillSetCalls) != 1 || service.skillSetCalls[0] != "general/verify=false" {
		t.Fatalf("calls: %v", service.skillSetCalls)
	}
}

func TestSkillsCommandTogglesInstalledSkill(t *testing.T) {
	service := &fakeLoginService{skills: []orchestrator.SkillInfo{
		{Name: "audit", Description: "Audit a change."},
		{Name: "verify", Description: "Verify a change.", Enabled: true},
	}}
	m := model{width: 100, height: 28, service: service}
	m = m.slashCommand("/skills")
	if !m.skills.open {
		t.Fatal("/skills did not open the toggle dialog")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"SKILLS", "audit", "verify", "enabled"} {
		if !strings.Contains(view, want) {
			t.Fatalf("skills view %q missing %q", view, want)
		}
	}

	m = key(m, runeKey("j"))
	m = key(m, runeKey("enter"))
	if len(service.skillSetCalls) != 1 || service.skillSetCalls[0] != "verify=false" {
		t.Fatalf("toggle calls = %v", service.skillSetCalls)
	}
	if service.skills[1].Enabled {
		t.Fatal("service skill stayed enabled")
	}
	if !strings.Contains(m.notice.text, "verify disabled") {
		t.Fatalf("toggle notice = %q", m.notice.text)
	}

	m = key(m, runeKey("esc"))
	if m.skills.open {
		t.Fatal("esc did not close the skills dialog")
	}
}
