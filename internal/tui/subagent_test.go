package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestSubagentComposerSpawnsChild(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	started := make(chan struct{}, 1)
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task orchestrator.Task, _ io.Writer) error {
		if task.ParentID != "" {
			return nil
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}))
	parent, err := s.CreateTask("parent", "parent mandate", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if !strings.Contains(m.View(), "SUBAGENT TASK") {
		t.Fatal("subagent prompt not shown")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("child mandate")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	var child orchestrator.Task
	for _, task := range s.ListTasks() {
		if task.ParentID == parent.ID {
			child = task
		}
	}
	if child.ID == "" || child.Prompt != "child mandate" || m.notice == "" {
		t.Fatalf("subagent not created: %+v notice=%q", child, m.notice)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "● child mandate") {
		t.Fatal("subagent not marked in task list")
	}
	if strings.Contains(plain, "↳") {
		t.Fatalf("subagent row still shows an arrow marker: %q", plain)
	}
}

type runnerFunc func(context.Context, orchestrator.Task, io.Writer) error

func (f runnerFunc) Run(ctx context.Context, task orchestrator.Task, out io.Writer) error {
	return f(ctx, task, out)
}
