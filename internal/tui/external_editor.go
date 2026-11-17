package tui

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

type externalEditorMsg struct {
	path   string
	err    error
	router bool
}

func (m model) openExternalEditor(router bool) (model, tea.Cmd) {
	name := os.Getenv("EDITOR")
	if name == "" {
		m.notice.text = "Set EDITOR to an editor executable first."
		return m, nil
	}
	f, err := os.CreateTemp("", "pilharmonic-prompt-*.txt")
	if err != nil {
		m.notice.text = "Could not create editor file: " + err.Error()
		return m, nil
	}
	text := m.input.Text
	if router {
		text = m.router.Text
	}
	_, err = f.WriteString(text)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name())
		m.notice.text = "Could not write editor file: " + err.Error()
		return m, nil
	}
	cmd := editorCommand(name, f.Name())
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return externalEditorMsg{path: f.Name(), err: err, router: router} })
}

func editorCommand(name, path string) *exec.Cmd {
	cmd := exec.Command(name, path)
	// The TUI output is wrapped for synchronization; editors need the real TTY.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func (m *model) finishExternalEditor(msg externalEditorMsg) {
	if msg.err != nil {
		m.notice.text = fmt.Sprintf("Editor failed: %v; draft file kept at %s", msg.err, msg.path)
		return
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.notice.text = fmt.Sprintf("Could not read editor file: %v; file kept at %s", err, msg.path)
		return
	}
	os.Remove(msg.path)
	e := &m.input.Editor
	if msg.router {
		e = &m.router.Editor
	}
	e.Text, e.Cursor, e.Scroll = string(data), len(data), 0
}
