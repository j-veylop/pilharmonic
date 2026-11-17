package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

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
