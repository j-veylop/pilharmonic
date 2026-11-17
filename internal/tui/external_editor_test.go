package tui

import (
	"os"
	"testing"
)

func TestEditorCommandUsesTerminalOutput(t *testing.T) {
	cmd := editorCommand("nvim", "draft.txt")
	if cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr {
		t.Fatal("editor output must use the terminal files directly")
	}
}

func TestExternalEditorResult(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "draft")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("edited\ntext"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	m := model{}
	m.finishExternalEditor(externalEditorMsg{path: f.Name(), router: true})
	if m.router.Text != "edited\ntext" || m.router.Cursor != len(m.router.Text) {
		t.Fatalf("draft = %#v", m.router.Editor)
	}
	if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains: %v", err)
	}
}

func TestExternalEditorMissing(t *testing.T) {
	t.Setenv("EDITOR", "")
	m := model{}
	m.router.Text = "original"
	m, cmd := m.openExternalEditor(true)
	if cmd != nil || m.router.Text != "original" || m.notice.text == "" {
		t.Fatal("missing editor must retain draft and show notice")
	}
}
