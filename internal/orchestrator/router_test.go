package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitRequestAcceptsAssignsAndStarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	task := mustGet(s.SubmitRequest("Fix the login bug\nPlease add coverage.", dir))(t)
	done := await(t, s, task.ID, Completed)
	if done.Title != "Fix the login bug" || done.Prompt != "Fix the login bug\nPlease add coverage." {
		t.Fatalf("unexpected mandate: %+v", done)
	}
	if done.Decision.Outcome != "accept" || done.Decision.PolicyVersion != LocalPolicyVersion || !strings.Contains(done.Decision.Reason, "router") {
		t.Fatalf("routing decision not recorded: %+v", done.Decision)
	}
	if len(done.Attempts) != 1 || done.Attempts[0].Agent != DefaultRouteAgent {
		t.Fatalf("assignment not recorded: %+v", done.Attempts)
	}
	routed := false
	for _, event := range done.Events {
		if event.Kind == "routed" && strings.Contains(event.Message, LocalPolicyVersion) {
			routed = true
		}
	}
	if !routed {
		t.Fatalf("routed event missing: %+v", done.Events)
	}
	restored := mustGet(Open(path))(t)
	defer func() { _ = restored.Shutdown() }()
	reloaded, _ := restored.GetTask(task.ID)
	if reloaded.Decision.PolicyVersion != LocalPolicyVersion || reloaded.Status != Completed {
		t.Fatalf("routing decision was not durable: %+v", reloaded.Decision)
	}
}

func TestTopLevelRoutingDefaultsToGeneral(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	runs := make(chan Task, 4)
	fake := runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		runs <- task
		return nil
	})
	for _, agent := range []string{DefaultRouteAgent, "implementer"} {
		s.RegisterRunner(agent, fake)
	}

	task := mustGet(s.SubmitRequest("add a parser", dir))(t)
	if task.Agent != DefaultRouteAgent {
		t.Fatalf("top-level request without a mention selected %q, want %q", task.Agent, DefaultRouteAgent)
	}
	await(t, s, task.ID, Completed)
	if run := <-runs; run.ID != task.ID || run.Agent != DefaultRouteAgent {
		t.Fatalf("top-level executor %q for task %q, want %q", run.Agent, run.ID, DefaultRouteAgent)
	}

	related := mustGet(s.CreateRelatedTask(task.ID, "change", "fix the parser", dir))(t)
	if related.Agent != DefaultRouteAgent {
		t.Fatalf("related request without a mention selected %q, want %q", related.Agent, DefaultRouteAgent)
	}

	explicit := createTask(t, s, "explicit", "add a parser", dir)
	if explicit.Agent != DefaultRouteAgent {
		t.Fatalf("draft without a mention selected %q, want %q", explicit.Agent, DefaultRouteAgent)
	}
	must(t, s.SetTaskAgent(explicit.ID, "implementer"))
	must(t, s.Start(context.Background(), explicit.ID))
	await(t, s, explicit.ID, Completed)
	if run := <-runs; run.ID != explicit.ID || run.Agent != "implementer" {
		t.Fatalf("explicit executor %q for task %q, want implementer", run.Agent, run.ID)
	}
}

func TestSubmitRequestSchedulesAtCapacity(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	must(t, s.SetConcurrencyLimit(1))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	first := mustGet(s.SubmitRequest("first request", dir))(t)
	<-started
	second := mustGet(s.SubmitRequest("second request", dir))(t)
	if second.Status != Queued || !second.ScheduleRequested || second.Decision.PolicyVersion != LocalPolicyVersion {
		t.Fatalf("second request was not routed into the waiting queue: %+v", second)
	}
	close(release)
	await(t, s, first.ID, Completed)
	await(t, s, second.ID, Completed)
}

func TestSubmitRequestRejectsEmptyOrInvalid(t *testing.T) {
	s := newTestService(t)
	dir := t.TempDir()
	if _, err := s.SubmitRequest("  \n ", dir); err == nil {
		t.Fatal("empty request accepted")
	}
	if _, err := s.SubmitRequest("work", filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if tasks := s.ListTasks(); len(tasks) != 0 {
		t.Fatalf("rejected request created tasks: %+v", tasks)
	}
}

func TestSubmitRequestUsesExplicitAgentMention(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	runs := make(chan Task, 4)
	for _, name := range []string{DefaultRouteAgent, "reviewer"} {
		s.RegisterRunner(name, runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
			runs <- task
			return nil
		}))
	}
	task := mustGet(s.SubmitRequest("@reviewer review the parser", dir))(t)
	if task.Agent != "reviewer" || task.Prompt != "review the parser" || task.Title != "review the parser" {
		t.Fatalf("explicit mention was not selected and removed from the mandate: %+v", task)
	}
	await(t, s, task.ID, Completed)
	if run := <-runs; run.ID != task.ID || run.Agent != "reviewer" || run.Prompt != "review the parser" {
		t.Fatalf("mentioned executor did not receive the request: %+v", run)
	}

	fallback := mustGet(s.SubmitRequest("review the parser", dir))(t)
	if fallback.Agent != DefaultRouteAgent || fallback.Prompt != "review the parser" {
		t.Fatalf("request without a mention did not use general: %+v", fallback)
	}
	await(t, s, fallback.ID, Completed)
	if run := <-runs; run.ID != fallback.ID || run.Agent != DefaultRouteAgent {
		t.Fatalf("fallback executor did not receive the request: %+v", run)
	}
}

func TestSubmitRequestRejectsInvalidAgentMention(t *testing.T) {
	s := newTestService(t)
	dir := t.TempDir()
	for _, text := range []string{"@missing do work", "@reviewer", "@"} {
		if _, err := s.SubmitRequest(text, dir); err == nil {
			t.Fatalf("accepted invalid explicit executor %q", text)
		}
	}
	if tasks := s.ListTasks(); len(tasks) != 0 {
		t.Fatalf("invalid mention created tasks: %+v", tasks)
	}
}

func TestDraftAndRelatedRequestsUseAgentMention(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	dir := t.TempDir()
	parent := createTask(t, s, "parent", "write a parser", dir)
	if parent.Agent != DefaultRouteAgent {
		t.Fatalf("unmentioned draft selected %q, want %q", parent.Agent, DefaultRouteAgent)
	}
	draft := createTask(t, s, "draft", "@tester add regression tests", dir)
	if draft.Agent != "tester" || draft.Prompt != "add regression tests" {
		t.Fatalf("draft mention was not applied: %+v", draft)
	}
	related := mustGet(s.CreateRelatedTask(parent.ID, "related", "@explorer trace the parser", dir))(t)
	if related.Agent != "explorer" || related.Prompt != "trace the parser" {
		t.Fatalf("related mention was not applied: %+v", related)
	}
}

func TestSplitAgentMention(t *testing.T) {
	cases := []struct {
		text, agent, request string
		explicit             bool
	}{
		{" @reviewer   review the parser ", "reviewer", "review the parser", true},
		{"@tester\nadd tests", "tester", "add tests", true},
		{"@general ", "general", "", true},
		{"@", "", "", true},
		{"review the parser", "", "review the parser", false},
		{"please @reviewer review", "", "please @reviewer review", false},
	}
	for _, tc := range cases {
		agent, request, explicit := SplitAgentMention(tc.text)
		if agent != tc.agent || request != tc.request || explicit != tc.explicit {
			t.Errorf("SplitAgentMention(%q) = %q, %q, %v; want %q, %q, %v", tc.text, agent, request, explicit, tc.agent, tc.request, tc.explicit)
		}
	}
}
