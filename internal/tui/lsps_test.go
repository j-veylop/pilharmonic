package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestLSPCommandTogglesRecognizedCheck(t *testing.T) {
	service := &fakeLoginService{languageChecks: []orchestrator.LanguageCheckInfo{
		{ID: "go", Name: "Go", Marker: "go.mod", Commands: "gofmt, gopls, go", Detected: true, Enabled: true},
		{ID: "node", Name: "Node/TypeScript", Marker: "package.json", Commands: "prettier, eslint, npm", Enabled: true},
	}}
	m := model{width: 100, height: 28, service: service}
	m = m.slashCommand("/lsp")
	if !m.lsps.open {
		t.Fatal("/lsp did not open the dialog")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"LANGUAGE CHECKS", "Go", "Node/TypeScript", "enabled", "not detected"} {
		if !strings.Contains(view, want) {
			t.Fatalf("lsp view %q missing %q", view, want)
		}
	}

	m = key(m, runeKey("j"))
	m = key(m, runeKey("enter"))
	if len(service.checksSetCalls) != 1 || service.checksSetCalls[0] != "node=false" {
		t.Fatalf("toggle calls = %v", service.checksSetCalls)
	}
	if !strings.Contains(m.notice.text, "node disabled") {
		t.Fatalf("toggle notice = %q", m.notice.text)
	}

	m = key(m, runeKey("esc"))
	if m.lsps.open {
		t.Fatal("esc did not close the dialog")
	}
	if plain := ansi.Strip(m.View()); strings.Contains(plain, "LANGUAGE CHECKS") {
		t.Fatalf("dialog remained in view: %q", plain)
	}
}
