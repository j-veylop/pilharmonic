package editor

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestEditorScrollingKeepsLongDraftVisible(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	e := Editor{Text: strings.Join(lines, "\n"), Cursor: len(strings.Join(lines, "\n"))}
	e.ensureVisible(20, 4)

	visible := ansi.Strip(strings.Join(e.View(20, 4, true), "\n"))
	if !strings.Contains(visible, "line-30") || strings.Contains(visible, "line-01") {
		t.Fatalf("long draft did not follow the cursor: %q", visible)
	}

	e.Cursor = 0
	e.ensureVisible(20, 4)
	visible = ansi.Strip(strings.Join(e.View(20, 4, true), "\n"))
	if !strings.Contains(visible, "ine-01") || strings.Contains(visible, "line-30") {
		t.Fatalf("cursor did not scroll back through the draft: %q", visible)
	}
}

func TestEditorWrapsInsteadOfTruncating(t *testing.T) {
	e := Editor{}
	e.insert(strings.Repeat("wrap-this ", 12) + "final-marker")
	e.ensureVisible(18, 6)
	lines := e.View(18, 6, true)
	if len(lines) < 4 {
		t.Fatalf("long paragraph did not wrap into multiple lines: %#v", lines)
	}
	for _, line := range lines {
		if width := lipgloss.Width(ansi.Strip(line)); width > 19 {
			t.Fatalf("wrapped line is wider than the editor: width=%d line=%q", width, line)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(lines, "")), "final-marker") {
		t.Fatalf("end of long draft is not visible: %#v", lines)
	}
}

func TestEditorUpFromFullWrappedLineAdvancesOneLine(t *testing.T) {
	const width = 5
	text := strings.Repeat("abcde", 4)
	e := Editor{Text: text, Cursor: len(text)}
	for want := 2; want >= 0; want-- {
		e.HandleKey(tea.KeyMsg{Type: tea.KeyUp}, width, 2)
		lines := wrapEditor(e.Text, width)
		if got := editorCursorLine(lines, e.Cursor); got != want {
			t.Fatalf("up from end of a full wrapped line: line=%d want %d cursor=%d", got, want, e.Cursor)
		}
	}
}

func TestEditorCursorColumnCountsWideRunes(t *testing.T) {
	e := Editor{Text: "日本 ab 語", Cursor: len("日本 ")}
	e.clamp()
	lines := wrapEditor(e.Text, 20)
	if column := editorCursorColumn(e.Text, lines[0], e.Cursor); column != 5 {
		t.Fatalf("cursor sits at column %d, want 5", column)
	}
	if got := ansi.Strip(insertEditorCursor(e.Text, 5)); got != e.Text {
		t.Fatalf("cursor changed the text: %q", got)
	}
}
