package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"pilharmonic/internal/orchestrator"
)

func TestEditorBackspaceAndDeleteHandleRunes(t *testing.T) {
	e := editor{text: "aé世", cursor: len("aé世")}
	e.backspace()
	if e.text != "aé" {
		t.Fatalf("backspace left %q", e.text)
	}
	e.backspace()
	e.backspace()
	e.backspace()
	if e.text != "" || e.cursor != 0 {
		t.Fatalf("backspace past start left %q at %d", e.text, e.cursor)
	}
	e = editor{text: "aé世"}
	e.delete()
	if e.text != "é世" {
		t.Fatalf("delete left %q", e.text)
	}
	e.delete()
	e.delete()
	e.delete()
	if e.text != "" {
		t.Fatalf("delete past end left %q", e.text)
	}
}

func TestEditorLineMovement(t *testing.T) {
	e := editor{text: "ab\ncd", cursor: len("ab\ncd")}
	e.moveLineStart()
	if e.cursor != 3 {
		t.Fatalf("line start at %d", e.cursor)
	}
	e.moveStart()
	e.moveLineEnd()
	if e.cursor != 2 {
		t.Fatalf("line end at %d", e.cursor)
	}
	e.moveEnd()
	if e.cursor != 5 {
		t.Fatalf("buffer end at %d", e.cursor)
	}
	e.moveLineEnd()
	if e.cursor != 5 {
		t.Fatalf("end of last line at %d", e.cursor)
	}
	e.moveStart()
	e.moveLineStart()
	if e.cursor != 0 {
		t.Fatalf("start of first line at %d", e.cursor)
	}
}

func TestPickerMoveClampsWithinChoices(t *testing.T) {
	p := pickerState[string]{choices: []string{"a", "b", "c"}}
	p.move("up")
	p.move("k")
	if p.index != 0 {
		t.Fatalf("index %d after moving past start", p.index)
	}
	p.move("down")
	p.move("j")
	if p.index != 2 {
		t.Fatalf("index %d after two moves down", p.index)
	}
	p.move("down")
	p.move("j")
	if p.index != 2 {
		t.Fatalf("index %d after moving past end", p.index)
	}
}

func TestStatusLabelAlwaysPairsGlyphWithWord(t *testing.T) {
	for _, status := range []orchestrator.Status{
		orchestrator.Queued, orchestrator.Running, orchestrator.Completed,
		orchestrator.Failed, orchestrator.Canceled, orchestrator.Interrupted,
	} {
		label := statusLabel(status)
		if !strings.Contains(label, strings.ToUpper(string(status))) {
			t.Fatalf("status label %q does not name %s", label, status)
		}
	}
}

func TestTerminalOutputSerializesWritesToFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	out := newTerminalOutput(file)
	if _, err = out.Write([]byte("escape-sequence")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "escape-sequence" {
		t.Fatalf("wrote %q", data)
	}
}

func TestModelInitReturnsRenderCommands(t *testing.T) {
	m := model{spinner: spinner.New()}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init must return the tick and spinner commands")
	}
}
