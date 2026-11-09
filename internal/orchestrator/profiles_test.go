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

// supportingRunner is a fake adapter that can translate profile parameters.
type supportingRunner struct{ runnerFunc }

func (supportingRunner) SupportsProfile(Profile) error { return nil }

func writeProfileConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hasEvent(t *testing.T, task Task, kind string) bool {
	t.Helper()
	for _, event := range task.Events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func TestProfileCatalogSnapshotAndSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	profiles := s.ListProfiles()
	if len(profiles) != 1 || profiles[0].Name != DefaultProfileName {
		t.Fatalf("bad built-in catalog: %+v", profiles)
	}
	config := `{"profiles":[
		{"name":"reviewer","description":"Read-only review","tools":["read","grep","find","ls"],"model":"test/model","instructions":"Review only.","no_integrate":true,"no_subagents":true},
		{"name":"gitter","tools":["bash","read","grep","find","ls"]}
	]}`
	if err := s.LoadProfiles(writeProfileConfig(t, config)); err != nil {
		t.Fatal(err)
	}
	if got := s.ListProfiles(); len(got) != 3 {
		t.Fatalf("profiles not registered: %+v", got)
	}
	workspace := t.TempDir()
	task, err := s.CreateTaskWithProfile("review", "review the tree", workspace, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if task.ProfileName != "reviewer" || task.ProfileSpec == nil || task.ProfileSpec.Model != "test/model" || !task.ProfileSpec.NoIntegrate {
		t.Fatalf("profile not snapshotted: %+v", task)
	}
	// A later catalog edit must not change what the accepted task will run with.
	if err = s.RegisterProfile(Profile{Name: "reviewer", Description: "changed", Model: "other/model"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(task.ID)
	if got.ProfileSpec == nil || got.ProfileSpec.Model != "test/model" {
		t.Fatalf("accepted snapshot changed: %+v", got.ProfileSpec)
	}
	// Selection applies only to a queued task that is not scheduled yet.
	if err = s.SetTaskProfile(task.ID, "gitter"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(task.ID)
	if got.ProfileName != "gitter" || got.ProfileSpec == nil || got.ProfileSpec.NoIntegrate || !hasEvent(t, got, "profile") {
		t.Fatalf("selection not recorded: %+v", got)
	}
	if err = s.SetTaskProfile(task.ID, "missing"); err == nil {
		t.Fatal("accepted unknown profile")
	}
	if _, err = s.CreateTaskWithProfile("x", "mandate", workspace, "missing"); err == nil {
		t.Fatal("created a task with an unknown profile")
	}
	// The routing default applies to tasks created without an explicit choice.
	if err = s.SetRoutingProfile("gitter"); err != nil {
		t.Fatal(err)
	}
	routed, err := s.CreateTask("routed", "route me", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if routed.ProfileName != "gitter" {
		t.Fatalf("routing profile not applied: %+v", routed)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown()
	got, _ = restored.GetTask(task.ID)
	if got.ProfileName != "gitter" || got.ProfileSpec == nil {
		t.Fatalf("profile not persisted: %+v", got)
	}
	if _, ok := restored.GetProfile("reviewer"); ok {
		t.Fatal("profiles are configuration, not durable task state")
	}
	if err = restored.LoadProfiles(writeProfileConfig(t, config)); err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.GetProfile("reviewer"); !ok {
		t.Fatal("profile catalog did not reload")
	}
}

func TestRelatedTaskRunsWithAcceptedProfile(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.RegisterProfile(Profile{Name: "review", Model: "test/review"}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	parent, err := s.CreateTask("parent", "parent mandate", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetRoutingProfile("review"); err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateRelatedTask(parent.ID, "related", "separate mandate", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if child.ProfileName != "review" || child.ProfileSpec == nil || child.ProfileSpec.Model != "test/review" {
		t.Fatalf("related task did not snapshot current routing default: %+v", child)
	}
	if err = s.SetRoutingProfile(DefaultProfileName); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterProfile(Profile{Name: "review", Model: "test/changed"}); err != nil {
		t.Fatal(err)
	}
	run := make(chan Task, 1)
	s.RegisterRunner("general", supportingRunner{runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		run <- task
		return nil
	})})
	if err = s.Start(context.Background(), child.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case executed := <-run:
		if executed.ProfileSpec == nil || executed.ProfileSpec.Model != "test/review" {
			t.Fatalf("runner received changed profile instead of accepted snapshot: %+v", executed.ProfileSpec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake runner did not receive related task")
	}
}

func TestProfileConfigValidationIsAtomic(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.LoadProfiles(writeProfileConfig(t, `{"profiles":[{"name":"gitter"}]}`)); err != nil {
		t.Fatal(err)
	}
	tooLong := strings.Repeat("x", maxProfileInstructions+1)
	for _, text := range []string{
		`{"profiles":[]}`,
		`{"profiles":[{"name":""}]}`,
		`{"profiles":[{"name":"two words"}]}`,
		`{"profiles":[{"name":" padded "}]}`,
		`{"profiles":[{"name":"ok","tools":["read","read"]}]}`,
		`{"profiles":[{"name":"ok","tools":["re ad"]}]}`,
		`{"profiles":[{"name":"ok","model":"two words"}]}`,
		`{"profiles":[{"name":"ok"},{"name":"ok"}]}`,
		`{"profiles":[{"name":"ok","bogus":true}]}`,
		`{"profiles":[{"name":"ok","instructions":"` + tooLong + `"}]}`,
		`{"profiles":[]} {}`,
	} {
		if err := s.LoadProfiles(writeProfileConfig(t, text)); err == nil {
			t.Fatalf("accepted %s", text)
		}
		if _, ok := s.GetProfile("ok"); ok {
			t.Fatalf("partial registration after %s", text)
		}
	}
	if _, ok := s.GetProfile("gitter"); !ok {
		t.Fatal("failed load removed an existing profile")
	}
}

func TestProfileAdapterSupportIsEnforced(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.RegisterProfile(Profile{Name: "explorer", Tools: []string{"read", "grep"}}); err != nil {
		t.Fatal(err)
	}
	if err := validateProfileForRunner(PiRunner{}, Profile{Name: "explorer", Tools: []string{"read"}, Model: "provider/model"}); err != nil {
		t.Fatalf("general executor rejected a translatable profile: %v", err)
	}
	if err := validateProfileForRunner(CommandRunner{Command: "cat"}, Profile{Name: "note", Instructions: "Be brief."}); err != nil {
		t.Fatalf("command runner rejected trusted instructions: %v", err)
	}
	workspace := t.TempDir()
	task, err := s.CreateTaskWithProfile("explore", "look around", workspace, "explorer")
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("cat", CommandRunner{Command: "cat"})
	if err = s.SetTaskAgent(task.ID, "cat"); err == nil {
		t.Fatal("generic executor accepted a tool palette")
	}
	s.RegisterRunner("general", CommandRunner{Command: "cat"})
	if err = s.Start(context.Background(), task.ID); err == nil || !strings.Contains(err.Error(), "tool palette") {
		t.Fatalf("start with an unsupported profile: %v", err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Status != Queued || len(got.Attempts) != 0 {
		t.Fatalf("rejected start changed the task: %+v", got)
	}
}

// A queued task whose recorded profile becomes unsupported after a configuration
// change fails on its own instead of wedging the queue.
func TestQueuedTaskFailsWhenProfileBecomesUnsupported(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	s.RegisterRunner("general", supportingRunner{runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})})
	workspace := t.TempDir()
	if err := s.RegisterProfile(Profile{Name: "explorer", Tools: []string{"read"}}); err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateTask("first", "hold the slot", workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateTaskWithProfile("second", "queued behind", workspace, "explorer")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	queued, _ := s.GetTask(second.ID)
	if !queued.ScheduleRequested {
		t.Fatalf("second task was not queued: %+v", queued)
	}
	// The executor is replaced by one that cannot translate a tool palette.
	s.RegisterRunner("general", CommandRunner{Command: "cat"})
	close(release)
	await(t, s, first.ID, Completed)
	failed := await(t, s, second.ID, Failed)
	if !strings.Contains(failed.Error, "tool palette") || !hasEvent(t, failed, "failed") {
		t.Fatalf("queued task did not fail visibly: %+v", failed)
	}
}

func TestNonIntegratingProfileRetainsWorktree(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.RegisterProfile(Profile{Name: "reviewer", Description: "Review only", NoIntegrate: true, NoSubagents: true}); err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "review.txt"), []byte("reviewed\n"), 0600)
	}))
	task, err := s.CreateTaskWithProfile("review", "review the tree", repo, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	task, _ = s.GetTask(task.ID)
	if !task.Isolated {
		t.Fatal("expected an isolated worktree")
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if done.MergeStatus != "" || done.MergeCommit != "" || done.ReviewStatus != "" {
		t.Fatalf("non-integrating profile produced integration records: %+v", done)
	}
	if !hasEvent(t, done, "integration_skipped") {
		t.Fatalf("integration skip not recorded: %+v", done.Events)
	}
	if _, err := os.Stat(filepath.Join(repo, "review.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source checkout changed: %v", err)
	}
	if _, err := os.Stat(done.ExecutionWorkspace); err != nil {
		t.Fatalf("worktree not retained: %v", err)
	}
	if len(done.Attempts) != 1 || done.Attempts[0].Profile != "reviewer" {
		t.Fatalf("attempt did not record the profile: %+v", done.Attempts)
	}
}

func TestSubagentProfileInheritanceAndContainment(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	for _, profile := range []Profile{
		{Name: "explorer", Tools: []string{"read", "grep", "find", "ls"}, NoIntegrate: true},
		{Name: "narrow", Tools: []string{"read"}, NoIntegrate: true},
	} {
		if err := s.RegisterProfile(profile); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetConcurrencyLimit(4); err != nil {
		t.Fatal(err)
	}
	started := make(chan Task, 8)
	release := make(chan struct{})
	s.RegisterRunner("general", supportingRunner{runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- task
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})})
	parent, err := s.CreateTaskWithProfile("parent", "parent mandate", t.TempDir(), "explorer")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	child, err := s.SpawnSubagent(parent.ID, "", "inherited work", "")
	if err != nil {
		t.Fatal(err)
	}
	if child.ProfileName != "explorer" || child.ProfileSpec == nil || !child.ProfileSpec.NoIntegrate {
		t.Fatalf("child did not inherit the parent profile: %+v", child)
	}
	<-started
	if _, err = s.SpawnSubagentWithProfile(parent.ID, "", "widen", "", DefaultProfileName); err == nil {
		t.Fatal("child widened the parent profile")
	}
	narrow, err := s.SpawnSubagentWithProfile(parent.ID, "", "narrow", "", "narrow")
	if err != nil {
		t.Fatal(err)
	}
	if narrow.ProfileName != "narrow" || narrow.ProfileSpec == nil || len(narrow.ProfileSpec.Tools) != 1 {
		t.Fatalf("narrow child profile not recorded: %+v", narrow)
	}
	<-started
	if _, err = s.SpawnSubagentWithProfile(parent.ID, "", "other executor", "other", "narrow"); err == nil {
		t.Fatal("child changed executor with a different profile")
	}
	close(release)
	await(t, s, child.ID, Completed)
	await(t, s, narrow.ID, Completed)
	await(t, s, parent.ID, Completed)
}

func TestNoSubagentsProfileWithholdsCredentials(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.RegisterProfile(Profile{Name: "lone", NoSubagents: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableSubagentControl(filepath.Join(t.TempDir(), "control.sock")); err != nil {
		t.Fatal(err)
	}
	started := make(chan Task, 1)
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
	parent, err := s.CreateTaskWithProfile("parent", "parent mandate", t.TempDir(), "lone")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	running := <-started
	if running.controlToken != "" || running.controlSocket != "" || running.controlGuidance != "" {
		t.Fatal("a no-delegation profile received delegation credentials")
	}
	if _, err = s.SpawnSubagent(parent.ID, "", "child", ""); err == nil || !strings.Contains(err.Error(), "does not allow delegation") {
		t.Fatalf("delegation not refused: %v", err)
	}
	close(release)
	await(t, s, parent.ID, Completed)
}

func TestPiArgsCarryProfile(t *testing.T) {
	args := strings.Join((PiRunner{}).piArgs("json", &Profile{
		Name:         "explorer",
		Tools:        []string{"read", "grep"},
		Model:        "provider/model",
		Instructions: "Read only; never edit.",
	}), "\x00")
	for _, want := range []string{"--tools\x00read,grep", "--model\x00provider/model", "Read only; never edit."} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	defaults := strings.Join((PiRunner{}).piArgs("rpc", nil), "\x00")
	if !strings.Contains(defaults, "--tools\x00"+piTools) || strings.Contains(defaults, "--model") {
		t.Fatalf("default arguments changed: %v", defaults)
	}
	if strings.Contains(defaults, "Read only") {
		t.Fatalf("default arguments carry role text: %v", defaults)
	}
}

func TestSpecialistAndProfileCompose(t *testing.T) {
	runner := PiRunner{instructions: "Locate the regression.", model: "specialist/model"}
	args := strings.Join(runner.piArgs("rpc", &Profile{Name: "audit", Instructions: "Record evidence.", Model: "profile/model"}), "\x00")
	if strings.Count(args, "--append-system-prompt") != 1 || !strings.Contains(args, runner.instructions) || !strings.Contains(args, "Record evidence.") || !strings.Contains(args, "--model\x00specialist/model") || strings.Contains(args, "profile/model") {
		t.Fatalf("unexpected combined Pi arguments: %q", args)
	}
}
