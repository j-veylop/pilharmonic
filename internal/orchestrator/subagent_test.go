package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpawnSubagentCreatesIndependentTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	started := make(chan Task, 2)
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, out io.Writer) error {
		started <- task
		if task.ParentID == "" {
			<-release
			return nil
		}
		_, err := io.WriteString(out, "child result")
		return err
	}))
	parent, err := s.CreateTask("parent", "parent mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	parentRun := <-started
	if parentRun.ID != parent.ID {
		t.Fatalf("unexpected start: %+v", parentRun)
	}
	child, err := s.SpawnSubagent(parent.ID, "child title", "child mandate only", "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	done := await(t, s, child.ID, Completed)
	close(release)
	await(t, s, parent.ID, Completed)

	if done.ParentID != parent.ID || done.Depth != 1 || done.Prompt != "child mandate only" {
		t.Fatalf("bad child identity: %+v", done)
	}
	if done.ThreadID == parent.ThreadID {
		t.Fatal("subagent reused the parent's thread")
	}
	if done.Decision.Outcome != "accept" || done.Decision.PolicyVersion != SubagentPolicyVersion || !strings.Contains(done.Decision.Reason, parent.ID) {
		t.Fatalf("bad child decision: %+v", done.Decision)
	}
	if done.Decision.PolicyID != SubagentPolicyID || done.Decision.Source != SubagentPolicySource ||
		done.Decision.Actor != "task:"+parent.ID || !strings.HasPrefix(done.Decision.InputDigest, "sha256:") {
		t.Fatalf("child audit evidence missing: %+v", done.Decision)
	}
	if done.Trigger.Source != "subagent" || done.Trigger.Text != "child mandate only" || done.Output != "child result" {
		t.Fatalf("bad child record: %+v", done)
	}
	gotParent, _ := s.GetTask(parent.ID)
	if gotParent.Prompt != "parent mandate" || len(gotParent.Context) != 0 {
		t.Fatalf("parent mandate or context changed: %+v", gotParent)
	}
	foundEvent := false
	for _, event := range gotParent.Events {
		if event.Kind == "subagent" && strings.Contains(event.Message, child.ID) {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("parent did not record spawn: %+v", gotParent.Events)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown()
	reloaded, ok := restored.GetTask(child.ID)
	if !ok || reloaded.ParentID != parent.ID || reloaded.Depth != 1 || reloaded.Decision.PolicyVersion != SubagentPolicyVersion || reloaded.Prompt != "child mandate only" {
		t.Fatalf("subagent relationship was not durable: %+v", reloaded)
	}
}

func TestSpawnSubagentDuringWorkspacePreparation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	// startLocked marks the attempt running before the worker has prepared
	// the worktree. Reproduce that window without racing a real git process.
	parent := Task{ID: "parent", Status: Running, Isolated: true, Workspace: t.TempDir(), Agent: DefaultRouteAgent}
	s.data.Tasks = []Task{parent}
	if _, err := s.SpawnSubagent(parent.ID, "child", "delegated work", ""); err == nil {
		t.Fatal("spawned a child in the source workspace during preparation")
	}
	if got := s.ListTasks(); len(got) != 1 || len(got[0].Events) != 0 {
		t.Fatalf("spawn changed task history: %+v", got)
	}
}

func TestSpawnSubagentRequiresRunningParentAndHonorsLimits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	started := make(chan Task, 4)
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- task
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	parent, err := s.CreateTask("parent", "parent mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SpawnSubagent(parent.ID, "", "too early", ""); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("spawned from queued parent: %v", err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.SetSubagentLimits(1, 1); err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnSubagent(parent.ID, "", "first child", "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = s.SpawnSubagent(parent.ID, "", "second child", ""); err == nil || !strings.Contains(err.Error(), "subagent limit") {
		t.Fatalf("child limit not enforced: %v", err)
	}
	if _, err = s.SpawnSubagent(child.ID, "", "grandchild", ""); err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("depth limit not enforced: %v", err)
	}
	if err = s.SetSubagentLimits(-1, 1); err == nil {
		t.Fatal("negative limits accepted")
	}
	close(release)
	await(t, s, child.ID, Completed)
	await(t, s, parent.ID, Completed)
}

func TestSubagentControlRoundTripAndCredentials(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, out io.Writer) error {
		if task.ParentID == "" {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}
		_, err := io.WriteString(out, "delegated output")
		return err
	}))
	parent, err := s.CreateTask("parent", "parent mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "control.sock")
	if err = s.EnableSubagentControl(socket); err != nil {
		t.Fatal(err)
	}
	// The parent started before control was enabled, so restart it to receive
	// credentials in this test.
	if err = s.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, parent.ID, Canceled)
	if err = s.Retry(parent.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var token string
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for i := range s.data.Tasks {
			if s.data.Tasks[i].ID == parent.ID {
				token = s.data.Tasks[i].controlToken
			}
		}
		s.mu.Unlock()
		if token != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if token == "" {
		t.Fatal("running parent received no control token")
	}
	if _, err = s.spawnSubagent(parent.ID, "", "bad credential", "", "", "wrong"); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("bad token accepted: %v", err)
	}

	t.Setenv("ORCHESTRATOR_CONTROL_SOCKET", socket)
	t.Setenv("ORCHESTRATOR_TASK_ID", parent.ID)
	t.Setenv("ORCHESTRATOR_SUBAGENT_TOKEN", token)
	var stdout, stderr strings.Builder
	code := RunSubagentCommand([]string{"--title", "delegated"}, strings.NewReader("delegated mandate"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("subagent command failed (%d): %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "delegated output") {
		t.Fatalf("missing child output: %q", stdout.String())
	}
	tasks := s.ListTasks()
	var child Task
	for _, task := range tasks {
		if task.ParentID == parent.ID {
			child = task
		}
	}
	if child.ID == "" || child.Prompt != "delegated mandate" || child.Title != "delegated" || child.Status != Completed {
		t.Fatalf("bad delegated task: %+v", child)
	}
	close(release)
	await(t, s, parent.ID, Completed)
}

func TestSubagentControlDisabledWithoutCredentials(t *testing.T) {
	t.Setenv("ORCHESTRATOR_CONTROL_SOCKET", "")
	t.Setenv("ORCHESTRATOR_TASK_ID", "")
	t.Setenv("ORCHESTRATOR_SUBAGENT_TOKEN", "")
	var stdout, stderr strings.Builder
	if code := RunSubagentCommand(nil, strings.NewReader("work"), &stdout, &stderr); code == 0 {
		t.Fatal("command succeeded outside a task")
	}
	if !strings.Contains(stderr.String(), "orchestrator task") {
		t.Fatalf("unhelpful error: %s", stderr.String())
	}
}

func TestRunnerEnvironmentReplacesInheritedControlCredentials(t *testing.T) {
	t.Setenv("ORCHESTRATOR_SUBAGENT_TOKEN", "stale")
	t.Setenv("ORCHESTRATOR_TASK_ID", "stale-task")
	task := Task{ID: "current", controlSocket: "/tmp/current.sock", controlToken: "current-token"}
	env := strings.Join(task.runnerEnvironment(), "\n")
	if !strings.Contains(env, "ORCHESTRATOR_TASK_ID=current") || !strings.Contains(env, "ORCHESTRATOR_SUBAGENT_TOKEN=current-token") {
		t.Fatalf("current credentials missing: %s", env)
	}
	if strings.Contains(env, "stale-task") || strings.Contains(env, "stale") {
		t.Fatalf("inherited credentials leaked: %s", env)
	}
	disabled := strings.Join((Task{}).runnerEnvironment(), "\n")
	if strings.Contains(disabled, "ORCHESTRATOR_SUBAGENT_TOKEN") || strings.Contains(disabled, "ORCHESTRATOR_TASK_ID") {
		t.Fatalf("disabled task inherited credentials: %s", disabled)
	}
}

func TestSubagentGuidanceAndEnvironmentAreBoundToAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if err = s.EnableSubagentControl(filepath.Join(t.TempDir(), "control.sock")); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAgents(writeAgentConfig(t, `{"agents":[{"name":"reviewer","description":"Review Go concurrency.","command":"cat"}]}`)); err != nil {
		t.Fatal(err)
	}
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	const instruction = "Delegate repository inspection to explorer."
	if err := s.RegisterProfile(Profile{Name: "lead", CoordinatorInstructions: instruction}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTaskWithProfile("parent", "mandate", t.TempDir(), "lead")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	running := <-seen
	if !strings.Contains(piPrompt(running), instruction) {
		t.Fatal("missing coordinator instructions in Pi prompt")
	}
	if !strings.Contains(running.controlGuidance, `"reviewer" — Review Go concurrency.`) || !strings.Contains(running.controlGuidance, "--agent NAME") {
		t.Fatal("missing specialist selection guidance")
	}
	if running.controlToken == "" || running.controlSocket == "" || !strings.Contains(running.controlGuidance, "Subagent delegation") {
		t.Fatalf("missing attempt credentials: %+v", running)
	}
	env := strings.Join(running.controlEnvironment(), "\n")
	for _, want := range []string{"ORCHESTRATOR_CONTROL_SOCKET=", "ORCHESTRATOR_TASK_ID=" + task.ID, "ORCHESTRATOR_SUBAGENT_TOKEN=" + running.controlToken} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q in %q", want, env)
		}
	}
	done := await(t, s, task.ID, Completed)
	cleared, _ := s.GetTask(done.ID)
	if cleared.controlToken != "" || cleared.controlSocket != "" {
		t.Fatalf("credentials survived attempt end: %+v", cleared)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "ORCHESTRATOR_SUBAGENT_TOKEN") || strings.Contains(string(contents), running.controlToken) {
		t.Fatal("control credential was persisted")
	}
}
