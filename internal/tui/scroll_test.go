package tui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

func TestOutputScrollKeyboardAndMouse(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("test", strings.Repeat("mandate ", 400), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		task.Output += fmt.Sprintf("line-%02d\n", i)
	}
	m := model{service: s, width: 120, height: 22, tasks: []orchestrator.Task{task}}
	if view := m.View(); !strings.Contains(view, "line-39") || strings.Contains(view, "line-00") {
		t.Fatalf("initial view not at bottom: %s", view)
	}
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = next.(model)
	if m.historyOffset != 3 {
		t.Fatalf("mouse offset %d", m.historyOffset)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(model)
	if m.historyOffset != m.maxScroll() || m.historyOffset == 0 {
		t.Fatalf("home offset %d of %d", m.historyOffset, m.maxScroll())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(model)
	if m.historyOffset != 0 {
		t.Fatalf("end offset %d", m.historyOffset)
	}
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = next.(model)
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = next.(model)
	if m.historyOffset != 0 {
		t.Fatalf("wheel down offset %d", m.historyOffset)
	}
}

func TestWrappedDetailAndScrollBounds(t *testing.T) {
	task := orchestrator.Task{ID: "x", Title: "title", Prompt: "begin-" + strings.Repeat("x", 100) + "-end", Output: strings.Repeat("a", 200) + "THE-END"}
	m := model{width: 80, height: 24, tasks: []orchestrator.Task{task}}
	content := strings.Join(m.detailContent(20), "\n")
	if !strings.Contains(content, "begin-") || !strings.Contains(strings.Join(strings.Fields(ansi.Strip(content)), ""), "THE-END") || len(strings.Split(content, "\n")) < 10 {
		t.Fatalf("long lines not wrapped: %q", content)
	}
	m.scrollDetail(10000)
	if m.historyOffset != m.maxScroll() || !strings.Contains(m.View(), "begin-") {
		t.Fatalf("cannot reach top: %s", m.View())
	}
	m.scrollDetail(-10000)
	if m.historyOffset != 0 || !strings.Contains(ansi.Strip(m.View()), "END") {
		t.Fatalf("cannot reach bottom: %s", m.View())
	}
}

type streamingRunner struct {
	first chan struct{}
	more  chan struct{}
	done  chan struct{}
}

func (r streamingRunner) Run(_ context.Context, _ orchestrator.Task, out io.Writer) error {
	for i := 0; i < 50; i++ {
		fmt.Fprintf(out, "line-%02d\n", i)
	}
	close(r.first)
	<-r.more
	fmt.Fprint(out, "new-line\n")
	close(r.done)
	return nil
}
func TestScrollPositionSurvivesNewOutput(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := streamingRunner{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	s.RegisterRunner("pi", r)
	task, err := s.CreateTask("task", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-r.first
	m := model{service: s, width: 120, height: 22, tasks: s.ListTasks()}
	m.scrollDetail(10)
	before := m.View()
	close(r.more)
	<-r.done
	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(model)
	after := m.View()
	if !strings.Contains(before, "line-39") || !strings.Contains(after, "line-39") || strings.Contains(after, "new-line") {
		t.Fatalf("scroll jumped after new output\nbefore %s\nafter %s", before, after)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
}
