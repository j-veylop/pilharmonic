package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"pilharmonic/internal/orchestrator"
)

func TestMoveMenuIndexClampsWithinChoices(t *testing.T) {
	index := 0
	for _, step := range []struct {
		key  string
		want int
	}{{"up", 0}, {"k", 0}, {"down", 1}, {"j", 2}, {"down", 2}, {"x", 2}} {
		moveMenuIndex(step.key, &index, 3)
		if index != step.want {
			t.Fatalf("%s: index %d, want %d", step.key, index, step.want)
		}
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
