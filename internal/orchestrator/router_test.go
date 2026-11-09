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
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	task, err := s.SubmitRequest("Fix the login bug\nPlease add coverage.", dir)
	if err != nil {
		t.Fatal(err)
	}
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
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown()
	reloaded, _ := restored.GetTask(task.ID)
	if reloaded.Decision.PolicyVersion != LocalPolicyVersion || reloaded.Status != Completed {
		t.Fatalf("routing decision was not durable: %+v", reloaded.Decision)
	}
}

func TestSubmitRequestSchedulesAtCapacity(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if err = s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	first, err := s.SubmitRequest("first request", dir)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := s.SubmitRequest("second request", dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != Queued || !second.ScheduleRequested || second.Decision.PolicyVersion != LocalPolicyVersion {
		t.Fatalf("second request was not routed into the waiting queue: %+v", second)
	}
	close(release)
	await(t, s, first.ID, Completed)
	await(t, s, second.ID, Completed)
}

func TestSubmitRequestRejectsEmptyOrInvalid(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
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

func TestRequestTitle(t *testing.T) {
	cases := map[string]string{
		"Fix the bug\nmore details": "Fix the bug",
		"  spaced   words  ":        "spaced words",
		"\n\n":                      "Untitled request",
	}
	for input, want := range cases {
		if got := requestTitle(input); got != want {
			t.Fatalf("requestTitle(%q) = %q, want %q", input, got, want)
		}
	}
	long := strings.Repeat("x", 100)
	if got := requestTitle(long); len([]rune(got)) != 60 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long title %q (%d runes)", got, len([]rune(got)))
	}
}

func TestSubmitRequestDefaultsToIsolatedWorktree(t *testing.T) {
	repo := committedRepo(t)
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	seen := make(chan Task, 1)
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.SubmitRequest("isolated request", repo)
	if err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if !task.Isolated || !done.Isolated {
		t.Fatalf("routed request did not default to isolation: task=%+v done=%+v", task, done)
	}
	routed := false
	for _, event := range done.Events {
		if event.Kind == "routed" && strings.Contains(event.Message, "isolated committed-HEAD worktree") {
			routed = true
		}
	}
	if !routed {
		t.Fatalf("routed assignment did not record isolation: %+v", done.Events)
	}
	if run := <-seen; run.Workspace == repo || !strings.Contains(run.Workspace, filepath.Join("workspaces", task.ID, "tree")) {
		t.Fatalf("routed request ran in %q instead of a worktree", run.Workspace)
	}
}
