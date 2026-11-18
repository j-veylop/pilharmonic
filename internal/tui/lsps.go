package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// lspMenuState is the recognized-language-check toggle dialog. It holds
// presentation state only; the service owns the durable enabled set.
type lspMenuState struct {
	open  bool
	index int
}

func (m *model) openLSPMenu() {
	m.lsps = lspMenuState{open: true}
	m.router.open = false
}

func (m model) lspRows() []orchestrator.LanguageCheckInfo {
	return m.service.ListLanguageChecks(m.workspace)
}

// lspMenuKey moves, toggles or closes the dialog. A toggle runs off the update
// loop; the list refreshes when the service reports back.
func (m model) lspMenuKey(key string) (model, tea.Cmd) {
	checks := m.lspRows()
	if m.listMenuKey(key, &m.lsps.index, len(checks)) {
		return m, nil
	}
	switch key {
	case "esc", "q":
		m.lsps = lspMenuState{}
		m.notice.text = "Language checks closed."
	case "enter", " ":
		if len(checks) == 0 {
			return m, nil
		}
		check := checks[max(0, min(len(checks)-1, m.lsps.index))]
		enabled := !check.Enabled
		service := m.service
		return m, func() tea.Msg {
			return lspToggleMsg{id: check.ID, enabled: enabled, err: service.SetLanguageCheckEnabled(check.ID, enabled)}
		}
	}
	return m, nil
}

func (m *model) finishLSPToggle(msg lspToggleMsg) {
	if msg.err != nil {
		m.notice.text = "Language check update failed: " + msg.err.Error()
		return
	}
	state := "disabled"
	if msg.enabled {
		state = "enabled"
	}
	m.notice.text = "Language check " + msg.id + " " + state + "."
}

func (m model) lspMenuView(background string, w, h int) string {
	checks := m.lspRows()
	rows := make([]menuRow, len(checks))
	for i, check := range checks {
		tag := mutedStyle.Render("not detected")
		switch {
		case !check.Enabled:
			tag = mutedStyle.Render("disabled")
		case check.Detected:
			tag = accentStyle.Render("enabled")
		}
		rows[i] = menuRow{label: check.Name, tag: tag}
	}
	mw := min(72, w-8) + 4
	mh := min(len(rows)+8, h-2)
	var body []string
	if len(checks) > 0 {
		selected := checks[max(0, min(len(checks)-1, m.lsps.index))]
		body = append(body, mutedStyle.Render(fit(selected.Marker+" · "+selected.Commands, mw-4, 1)))
	}
	return menuView(background, w, h, "LANGUAGE CHECKS", "automatic format, diagnostics and tests after edits", body, rows, m.lsps.index, "No language checks recognized.", "j/k select · enter toggle · esc close", mw, mh)
}
