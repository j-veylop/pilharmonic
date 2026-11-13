package orchestrator

import (
	"context"
	"errors"
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
	defer func() { _ = s.Shutdown() }()
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
	defer func() { _ = restored.Shutdown() }()
	reloaded, ok := restored.GetTask(child.ID)
	if !ok || reloaded.ParentID != parent.ID || reloaded.Depth != 1 || reloaded.Decision.PolicyVersion != SubagentPolicyVersion || reloaded.Prompt != "child mandate only" {
		t.Fatalf("subagent relationship was not durable: %+v", reloaded)
	}
}

func TestSubagentAutomaticSelectionCanChooseImplementer(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	started := make(chan Task, 2)
	release := make(chan struct{})
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- task
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	s.RegisterRunner("implementer", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		started <- task
		return nil
	}))

	parent, err := s.CreateTask("parent", "parent mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	if run := <-started; run.ID != parent.ID || run.Agent != DefaultRouteAgent {
		t.Fatalf("parent executor %q for task %q, want %q", run.Agent, run.ID, DefaultRouteAgent)
	}

	child, err := s.SpawnSubagent(parent.ID, "", "add a parser", "")
	if err != nil {
		t.Fatal(err)
	}
	if child.Agent != "implementer" {
		t.Fatalf("delegated automatic route selected %q, want implementer", child.Agent)
	}
	await(t, s, child.ID, Completed)
	if run := <-started; run.ID != child.ID || run.Agent != "implementer" {
		t.Fatalf("delegated executor %q for task %q, want implementer", run.Agent, run.ID)
	}

	close(release)
	await(t, s, parent.ID, Completed)
}

func TestSpawnSubagentDuringWorkspacePreparation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
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
	defer func() { _ = s.Shutdown() }()
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
	defer func() { _ = s.Shutdown() }()
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
	if _, err = s.spawnSubagent(parent.ID, "", "no credential", "", "", ""); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("empty token accepted: %v", err)
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
	if !strings.Contains(stderr.String(), "pilharmonic task") {
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

func TestRunnerEnvironmentDropsOperatorSecrets(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "operator-secret")
	t.Setenv("GITHUB_TOKEN", "operator-token")
	t.Setenv("MY_CUSTOM_ORCHESTRATOR", "kept")
	t.Setenv("ORCHESTRATOR_CUSTOM", "kept")
	t.Setenv("LC_ALL", "C")
	env := strings.Join((Task{}).runnerEnvironment(), "\n")
	if strings.Contains(env, "operator-secret") || strings.Contains(env, "operator-token") {
		t.Fatalf("operator credentials leaked to the agent: %s", env)
	}
	if strings.Contains(env, "MY_CUSTOM_ORCHESTRATOR") {
		t.Fatalf("unlisted variable crossed the trust boundary: %s", env)
	}
	for _, want := range []string{"PATH=", "LC_ALL=C", "ORCHESTRATOR_CUSTOM=kept"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing allowed variable %q in %s", want, env)
		}
	}
}

func TestSubagentBudgetGuidanceCountsExistingChildrenOnRetry(t *testing.T) {
	t.Run("partially consumed", func(t *testing.T) {
		s, started, release, parent := retryParentWithChildren(t, 3, 4)
		resumed := <-started
		if resumed.ID != parent.ID || resumed.controlToken == "" || resumed.controlSocket == "" {
			t.Fatalf("remaining child budget withheld credentials: %+v", resumed)
		}
		for _, want := range []string{
			"at most 1 child task(s) remaining for this task",
			"Coordinator delegation guidance:",
		} {
			if !strings.Contains(resumed.controlGuidance, want) {
				t.Fatalf("missing %q in %q", want, resumed.controlGuidance)
			}
		}
		if strings.Contains(resumed.controlGuidance, "at most 4 child task(s)") {
			t.Fatalf("guidance advertised the configured limit: %q", resumed.controlGuidance)
		}
		child, err := s.SpawnSubagent(parent.ID, "", "last allowed child", "")
		if err != nil {
			t.Fatalf("remaining child budget rejected a child: %v", err)
		}
		await(t, s, child.ID, Completed)
		if _, err = s.SpawnSubagent(parent.ID, "", "over budget", ""); err == nil || !strings.Contains(err.Error(), "subagent limit") {
			t.Fatalf("child limit not enforced after retry: %v", err)
		}
		close(release)
		await(t, s, parent.ID, Completed)
	})

	t.Run("exhausted", func(t *testing.T) {
		s, started, release, parent := retryParentWithChildren(t, 2, 2)
		resumed := <-started
		if resumed.ID != parent.ID {
			t.Fatalf("unexpected resumed task: %+v", resumed)
		}
		if resumed.controlToken != "" || resumed.controlSocket != "" || resumed.controlGuidance != "" {
			t.Fatalf("exhausted budget received delegation credentials or guidance: %+v", resumed)
		}
		if strings.Contains(composeInput(resumed), "Subagent delegation") || strings.Contains(composeInput(resumed), "Coordinator delegation guidance") {
			t.Fatalf("exhausted budget prompted impossible delegation: %q", composeInput(resumed))
		}
		if _, err := s.SpawnSubagent(parent.ID, "", "over budget", ""); err == nil || !strings.Contains(err.Error(), "subagent limit") {
			t.Fatalf("exhausted child limit not enforced after retry: %v", err)
		}
		close(release)
		await(t, s, parent.ID, Completed)
	})
}

// retryParentWithChildren starts a parent, creates children that share its
// configured budget, then retries it so the next attempt starts with the same
// durable children. The returned started channel receives top-level attempts.
func retryParentWithChildren(t *testing.T, children, maxChildren int) (*Service, chan Task, chan struct{}, Task) {
	t.Helper()
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetSubagentLimits(DefaultMaxSubagentDepth, maxChildren); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableSubagentControl(filepath.Join(t.TempDir(), "control.sock")); err != nil {
		t.Fatal(err)
	}
	started := make(chan Task, 4)
	release := make(chan struct{})
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		if task.ParentID != "" {
			return nil
		}
		started <- task
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	parent, err := s.CreateTask("parent", "inspect and fix the parser", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	for i := 0; i < children; i++ {
		child, spawnErr := s.SpawnSubagent(parent.ID, "", "child mandate", "")
		if spawnErr != nil {
			t.Fatal(spawnErr)
		}
		await(t, s, child.ID, Completed)
	}
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
	return s, started, release, parent
}

func TestDefaultGeneralProfileReceivesCoordinatorDelegationGuidance(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.EnableSubagentControl(filepath.Join(t.TempDir(), "control.sock")); err != nil {
		t.Fatal(err)
	}
	started := make(chan Task, 2)
	release := make(chan struct{})
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- task
		if task.ParentID == "" {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}))
	parent, err := s.CreateTask("parent", "inspect and fix the parser", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	running := <-started
	prompt := composeInput(running)
	for _, want := range []string{
		"Answer simple questions directly",
		"delegate multiple focused subtasks",
		"instead of handing the whole task to one child",
		"self-contained scope",
		"Remaining depth: 2; at most 4 child task(s) remaining for this task",
		"Delegation cannot widen the original mandate or this task's profile",
		"parallel only for independent scopes",
		"sequentially",
		"do not assume children coordinated their edits",
		"does not expand the original mandate",
		"router does not split work automatically",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q in coordinator prompt", want)
		}
	}
	child, err := s.SpawnSubagent(parent.ID, "", "inspect the parser", "")
	if err != nil {
		t.Fatal(err)
	}
	childRunning := <-started
	if strings.Contains(composeInput(childRunning), "Coordinator delegation guidance") {
		t.Fatal("delegated child received coordinator guidance")
	}
	if got := s.ListTasks(); len(got) != 2 {
		t.Fatalf("automatic splitting created %d tasks, want 2", len(got))
	}
	close(release)
	await(t, s, child.ID, Completed)
	await(t, s, parent.ID, Completed)
}

func TestCoordinatorGuidanceRequiresShellAccess(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.EnableSubagentControl(filepath.Join(t.TempDir(), "control.sock")); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterProfile(Profile{Name: "inspect", Tools: []string{"read"}, CoordinatorInstructions: "Use the helper."}); err != nil {
		t.Fatal(err)
	}
	started := make(chan Task, 1)
	s.RegisterRunner(DefaultRouteAgent, supportingRunner{runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		started <- task
		return nil
	})})
	task, err := s.CreateTaskWithProfile("parent", "inspect the parser", t.TempDir(), "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	running := <-started
	if strings.Contains(running.controlGuidance, "Coordinator delegation guidance") || strings.Contains(running.controlGuidance, "Coordinator instructions") {
		t.Fatalf("coordinator guidance appended without shell access: %q", running.controlGuidance)
	}
	await(t, s, task.ID, Completed)
}

func TestSubagentGuidanceAndEnvironmentAreBoundToAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
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
	for _, want := range []string{instruction, "Coordinator delegation guidance:", "delegate multiple focused subtasks"} {
		if !strings.Contains(composeInput(running), want) {
			t.Fatalf("missing %q in Pi prompt", want)
		}
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

func TestNoteSubagentStartFailureRecordsEvent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("child", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.noteSubagentStartFailure(task.ID, errors.New("no capacity"))
	updated, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatal("task disappeared")
	}
	found := false
	for _, event := range updated.Events {
		if event.Kind == "start_failed" && event.Message == "no capacity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("start failure not recorded: %+v", updated.Events)
	}
}
