package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
	"github.com/j-veylop/pilharmonic/internal/tui/editor"
)

type followService struct {
	*fakeLoginService
	requeued bool
	started  []string
}

func (f *followService) FollowUp(string, string) (bool, error) { return f.requeued, nil }
func (f *followService) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return nil
}

func TestFollowUpResumesConversationImmediately(t *testing.T) {
	for _, test := range []struct {
		name     string
		requeued bool
		want     int
	}{
		{"finished task resumes", true, 1},
		{"running task only gets context", false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &followService{fakeLoginService: &fakeLoginService{}, requeued: test.requeued}
			m := model{service: service, input: composer{kind: followUpInput, target: "task"}}
			msg := m.submitInput("next step")
			if len(service.started) != test.want {
				t.Fatalf("started %v, want %d", service.started, test.want)
			}
			if !test.requeued && !strings.Contains(actionResultMsg(msg).text(), "delivery status") {
				t.Fatalf("running follow-up notice = %q", actionResultMsg(msg).text())
			}
		})
	}
}

func TestConversationShowsPromptFollowUpsAndAttempts(t *testing.T) {
	base := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	task := orchestrator.Task{
		ID: "task", Title: "work", Prompt: "original mandate", Status: orchestrator.Completed,
		CreatedAt: base,
		Context:   []orchestrator.Trigger{{Text: "refine the detail", CreatedAt: base.Add(time.Minute)}},
		Attempts: []orchestrator.Attempt{
			{ID: "a1", Status: orchestrator.Completed, StartedAt: base, Output: "first answer"},
			{ID: "a2", Status: orchestrator.Completed, StartedAt: base.Add(2 * time.Minute), Output: "second answer"},
		},
	}
	text := ansi.Strip(strings.Join((model{tasks: []orchestrator.Task{task}}).detailContent(80), "\n"))
	order := []string{"▌ original mandate", "first answer", "refine the detail", "second answer"}
	last := -1
	for _, want := range order {
		at := strings.Index(text, want)
		if at < 0 || at < last {
			t.Fatalf("%q missing or out of order: %q", want, text)
		}
		last = at
	}
	if !strings.Contains(text, "attempt a1") || !strings.Contains(text, "attempt a2") || !strings.Contains(text, "▌ refine the detail") {
		t.Fatalf("conversation lost its chat meta or user rail: %q", text)
	}
}

func TestConversationShowsTaskErrorOnce(t *testing.T) {
	for _, tc := range []struct {
		name          string
		taskError     string
		attemptError  string
		wantTaskError int
	}{
		{"same error", "failure", "failure", 1},
		{"different errors", "failure", "earlier failure", 2},
		{"task error only", "failure", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := model{tasks: []orchestrator.Task{{Prompt: "work", Error: tc.taskError, Attempts: []orchestrator.Attempt{{Error: tc.attemptError}}}}}
			text := ansi.Strip(strings.Join(m.detailContent(80), "\n"))
			if got := strings.Count(text, "failure"); got != tc.wantTaskError {
				t.Fatalf("error count = %d, want %d: %q", got, tc.wantTaskError, text)
			}
		})
	}
}

func TestChatboxFollowsSelectedTaskInDetailedView(t *testing.T) {
	service := &followService{fakeLoginService: &fakeLoginService{anyConfigured: true}, requeued: true}
	m := model{
		width: 100, height: 28, service: service, detailFocus: true,
		tasks:  []orchestrator.Task{{ID: "task", Title: "work", Agent: "general", Status: orchestrator.Completed}},
		router: routerState{open: true, Editor: editor.Editor{Text: "next step", Cursor: len("next step")}},
	}
	_, cmd := m.submitRequest(false)
	msg, ok := cmd().(routerResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("follow-up result: %#v", msg)
	}
	if len(service.started) != 1 || service.started[0] != "task" {
		t.Fatalf("conversation did not resume: %v", service.started)
	}
	if len(service.submitCalls) != 0 {
		t.Fatalf("chatbox created a task instead of following up: %v", service.submitCalls)
	}
	if !strings.Contains(msg.notice, "continuing the conversation") {
		t.Fatalf("notice = %q", msg.notice)
	}
}

func TestChatboxCreatesTaskInMainView(t *testing.T) {
	service := &followService{fakeLoginService: &fakeLoginService{anyConfigured: true}}
	m := model{
		width: 100, height: 28, service: service,
		tasks:  []orchestrator.Task{{ID: "task", Title: "work", Agent: "general", Status: orchestrator.Completed}},
		router: routerState{open: true, Editor: editor.Editor{Text: "new work", Cursor: len("new work")}},
	}
	_, cmd := m.submitRequest(false)
	if _, ok := cmd().(routerResultMsg); !ok {
		t.Fatal("main-view submit did not return a result")
	}
	if len(service.submitCalls) != 1 || service.submitCalls[0] != "new work" {
		t.Fatalf("main view did not create a task: %v", service.submitCalls)
	}
	if len(service.started) != 0 {
		t.Fatalf("main view resumed a task: %v", service.started)
	}
}

func TestCycleSubtaskSelectsDescendants(t *testing.T) {
	m := model{tasks: []orchestrator.Task{
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child"},
		{ID: "parent", Title: "parent"},
	}, selected: 1, detailFocus: true}
	m.cycleSubtask(1)
	if m.tasks[m.selected].ID != "child" {
		t.Fatalf("cycle selected %q", m.tasks[m.selected].ID)
	}
	m.cycleSubtask(-1)
	if m.tasks[m.selected].ID != "parent" {
		t.Fatalf("cycle back selected %q", m.tasks[m.selected].ID)
	}
}

func TestRunningTaskShowsAnimatedTurnSpinner(t *testing.T) {
	started := time.Now().Add(-3 * time.Second)
	m := model{
		chatAnim: newChatAnim(),
		tasks: []orchestrator.Task{{ID: "task", Title: "work", Prompt: "do work", Status: orchestrator.Running,
			Attempts: []orchestrator.Attempt{{ID: "attempt", Status: orchestrator.Running, StartedAt: started}}}},
	}
	for i := 0; i < 30; i++ {
		m.chatAnim.Advance()
	}
	m.chatAnim.SetSuffixText(m.selectedTurnElapsed())
	text := ansi.Strip(strings.Join(m.detailContent(80), "\n"))
	line := ""
	for _, row := range strings.Split(text, "\n") {
		if strings.Contains(row, "3s") {
			line = row
			break
		}
	}
	if line == "" {
		t.Fatalf("turn spinner missing from the output: %q", text)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "  "), " 3s")
	if len([]rune(strings.TrimSpace(body))) < 10 {
		t.Fatalf("turn spinner showed a single character: %q", line)
	}
}
