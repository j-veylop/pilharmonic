package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pilharmonic/internal/agenttools"
)

// supportingRunner is a fake adapter that can translate profile parameters.
type supportingRunner struct{ runnerFunc }

func (supportingRunner) SupportsProfile(Profile) error { return nil }

func writeProfileConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	must(t, os.WriteFile(path, []byte(text), 0600))
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

func profileNamed(profiles []Profile, name string) (Profile, bool) {
	for _, profile := range profiles {
		if profile.Name == name {
			return profile, true
		}
	}
	return Profile{}, false
}

// registerProfileForTest installs a profile with the same validation the
// config loader applies. Production registers profiles only through
// LoadProfiles.
func registerProfileForTest(t *testing.T, s *Service, p Profile) {
	t.Helper()
	must(t, p.validate())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profiles == nil {
		s.profiles = builtinProfiles()
	}
	s.profiles[p.Name] = cloneProfile(p)
}

// spawnSubagentWithProfileForTest drives the operator path directly with a
// named profile, which is what the CLI and terminal entry points use.
func spawnSubagentWithProfileForTest(s *Service, parentID, title, prompt, agent, profile string) (Task, error) {
	return s.spawnSubagentWithCredential(parentID, title, prompt, agent, profile, "", true)
}

func TestBuiltinProfileDefaultsAndConfigOverlay(t *testing.T) {
	s := newTestService(t)
	profiles := s.ListProfiles()
	defaultProfile, ok := profileNamed(profiles, DefaultProfileName)
	if !ok || !defaultProfile.UsesExecutorDefaults() {
		t.Fatalf("missing built-in default profile: %+v", profiles)
	}
	coordinator, ok := profileNamed(profiles, CoordinatorProfileName)
	if !ok || coordinator.CoordinatorInstructions != "" || len(coordinator.Tools) != 0 || coordinator.Model != "" {
		t.Fatalf("bad built-in coordinator profile: %+v", coordinator)
	}
	config := `{"default_profile":"coordinator","profiles":[{"name":"coordinator","model":"provider/parent"}]}`
	must(t, s.LoadProfiles(writeProfileConfig(t, config)))
	overridden, _ := profileNamed(s.ListProfiles(), CoordinatorProfileName)
	if overridden.Model != "provider/parent" || overridden.Description != coordinator.Description || overridden.CoordinatorInstructions != "" {
		t.Fatalf("partial override lost built-in values: %+v", overridden)
	}
	task := createTask(t, s, "routed", "route me", t.TempDir())
	if task.ProfileName != CoordinatorProfileName || task.ProfileSpec == nil || task.ProfileSpec.Model != "provider/parent" {
		t.Fatalf("configured default was not overlaid: %+v", task)
	}
	custom := `{"profiles":[{"name":"coordinator","coordinator_instructions":"Ask before delegating.","description":""}]}`
	must(t, s.LoadProfiles(writeProfileConfig(t, custom)))
	customized, _ := profileNamed(s.ListProfiles(), CoordinatorProfileName)
	if customized.CoordinatorInstructions != "Ask before delegating." || customized.Description != "" || customized.Model != "" {
		t.Fatalf("explicit overlay values were not applied: %+v", customized)
	}
}

func TestProfileCatalogSnapshotAndSelection(t *testing.T) {
	var err error
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	profiles := s.ListProfiles()
	if _, ok := profileNamed(profiles, DefaultProfileName); !ok {
		t.Fatalf("missing default in built-in catalog: %+v", profiles)
	}
	if _, ok := profileNamed(profiles, CoordinatorProfileName); !ok {
		t.Fatalf("missing coordinator in built-in catalog: %+v", profiles)
	}
	config := `{"profiles":[
		{"name":"reviewer","description":"Read-only review","tools":["read","grep","find","ls"],"model":"test/model","instructions":"Review only.","no_integrate":true,"no_subagents":true},
		{"name":"gitter","tools":["bash","read","grep","find","ls"]}
	]}`
	must(t, s.LoadProfiles(writeProfileConfig(t, config)))
	if got := s.ListProfiles(); len(got) != 4 {
		t.Fatalf("profiles not registered: %+v", got)
	}
	workspace := t.TempDir()
	task := mustGet(s.CreateTaskWithProfile("review", "review the tree", workspace, "reviewer"))(t)
	if task.ProfileName != "reviewer" || task.ProfileSpec == nil || task.ProfileSpec.Model != "test/model" || !task.ProfileSpec.NoIntegrate {
		t.Fatalf("profile not snapshotted: %+v", task)
	}
	// A later catalog edit must not change what the accepted task will run with.
	registerProfileForTest(t, s, Profile{Name: "reviewer", Description: "changed", Model: "other/model"})
	got, _ := s.GetTask(task.ID)
	if got.ProfileSpec == nil || got.ProfileSpec.Model != "test/model" {
		t.Fatalf("accepted snapshot changed: %+v", got.ProfileSpec)
	}
	// Selection applies only to a queued task that is not scheduled yet.
	must(t, s.SetTaskProfile(task.ID, "gitter"))
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
	must(t, s.SetRoutingProfile("gitter"))
	routed := createTask(t, s, "routed", "route me", workspace)
	if routed.ProfileName != "gitter" {
		t.Fatalf("routing profile not applied: %+v", routed)
	}
	restored := mustGet(Open(path))(t)
	defer func() { _ = restored.Shutdown() }()
	got, _ = restored.GetTask(task.ID)
	if got.ProfileName != "gitter" || got.ProfileSpec == nil {
		t.Fatalf("profile not persisted: %+v", got)
	}
	if _, ok := profileNamed(restored.ListProfiles(), "reviewer"); ok {
		t.Fatal("profiles are configuration, not durable task state")
	}
	must(t, restored.LoadProfiles(writeProfileConfig(t, config)))
	if _, ok := profileNamed(restored.ListProfiles(), "reviewer"); !ok {
		t.Fatal("profile catalog did not reload")
	}
}

func TestRelatedTaskRunsWithAcceptedProfile(t *testing.T) {
	s := newTestService(t)
	registerProfileForTest(t, s, Profile{Name: "review", Model: "test/review"})
	workspace := t.TempDir()
	parent := createTask(t, s, "parent", "parent mandate", workspace)
	must(t, s.SetRoutingProfile("review"))
	child := mustGet(s.CreateRelatedTask(parent.ID, "related", "separate mandate", workspace))(t)
	if child.ProfileName != "review" || child.ProfileSpec == nil || child.ProfileSpec.Model != "test/review" {
		t.Fatalf("related task did not snapshot current routing default: %+v", child)
	}
	must(t, s.SetRoutingProfile(DefaultProfileName))
	registerProfileForTest(t, s, Profile{Name: "review", Model: "test/changed"})
	run := make(chan Task, 1)
	s.RegisterRunner("general", supportingRunner{runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		run <- task
		return nil
	})})
	must(t, s.Start(context.Background(), child.ID))
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
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadProfiles(writeProfileConfig(t, `{"profiles":[{"name":"gitter"}],"specialists":[{"name":"reviewer","tools":["read"]}]}`)))
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
		`{"profiles":[{"name":"ok"}],"specialists":[{"name":"reviewer","tools":["unknown"]}]}`,
		`{"specialists":[{"name":"tester","tools":["read"]},{"name":"tester","tools":["bash"]}]}`,
		`{"specialists":[{"name":"tester","tools":["read","read"]}]}`,
		`{"specialists":[{"name":"tester"}]}`,
		`{"specialists":[{"name":"general","tools":["read"]}]}`,
		`{"profiles":[{"name":"ok"}]}` + strings.Repeat(" ", maxConfigSize),
	} {
		if err := s.LoadProfiles(writeProfileConfig(t, text)); err == nil {
			t.Fatalf("accepted %.200s", text)
		}
		if _, ok := profileNamed(s.ListProfiles(), "ok"); ok {
			t.Fatalf("partial registration after %.200s", text)
		}
		if len(s.specialistTools) != 1 || !slices.Equal(s.specialistTools["reviewer"], []string{"read"}) {
			t.Fatalf("invalid config mutated specialist limits: %v", s.specialistTools)
		}
	}
	if _, ok := profileNamed(s.ListProfiles(), "gitter"); !ok {
		t.Fatal("failed load removed an existing profile")
	}
}

func TestProfileAdapterSupportIsEnforced(t *testing.T) {
	var err error
	s := newTestService(t)
	registerProfileForTest(t, s, Profile{Name: "explorer", Tools: []string{"read", "grep"}})
	if err := validateProfileForRunner(FantasyRunner{}, Profile{Name: "explorer", Tools: []string{"read"}, Model: "provider/model"}); err != nil {
		t.Fatalf("general executor rejected a translatable profile: %v", err)
	}
	if err := validateProfileForRunner(CommandRunner{Command: "cat"}, Profile{Name: "note", Instructions: "Be brief."}); err != nil {
		t.Fatalf("command runner rejected trusted instructions: %v", err)
	}
	workspace := t.TempDir()
	task := mustGet(s.CreateTaskWithProfile("explore", "look around", workspace, "explorer"))(t)
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
	s := newTestService(t)
	must(t, s.SetConcurrencyLimit(1))
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
	registerProfileForTest(t, s, Profile{Name: "explorer", Tools: []string{"read"}})
	first := createTask(t, s, "first", "hold the slot", workspace)
	second := mustGet(s.CreateTaskWithProfile("second", "queued behind", workspace, "explorer"))(t)
	must(t, s.Start(context.Background(), first.ID))
	must(t, s.Start(context.Background(), second.ID))
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
	s := newTestService(t)
	registerProfileForTest(t, s, Profile{Name: "reviewer", Description: "Review only", NoIntegrate: true, NoSubagents: true})
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "review.txt"), []byte("reviewed\n"), 0600)
	}))
	task := mustGet(s.CreateTaskWithProfile("review", "review the tree", repo, "reviewer"))(t)
	must(t, s.SetTaskIsolation(task.ID, true))
	task, _ = s.GetTask(task.ID)
	if !task.Isolated {
		t.Fatal("expected an isolated worktree")
	}
	must(t, s.Start(context.Background(), task.ID))
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
	var err error
	s := newTestService(t)
	for _, profile := range []Profile{
		{Name: "explorer", Tools: []string{"read", "grep", "find", "ls"}, NoIntegrate: true},
		{Name: "narrow", Tools: []string{"read"}, NoIntegrate: true},
	} {
		registerProfileForTest(t, s, profile)
	}
	must(t, s.SetConcurrencyLimit(4))
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
	parent := mustGet(s.CreateTaskWithProfile("parent", "parent mandate", t.TempDir(), "explorer"))(t)
	must(t, s.Start(context.Background(), parent.ID))
	<-started
	child := mustGet(s.SpawnSubagent(parent.ID, "", "inherited work", ""))(t)
	if child.ProfileName != "explorer" || child.ProfileSpec == nil || !child.ProfileSpec.NoIntegrate {
		t.Fatalf("child did not inherit the parent profile: %+v", child)
	}
	<-started
	if _, err = spawnSubagentWithProfileForTest(s, parent.ID, "", "widen", "", DefaultProfileName); err == nil {
		t.Fatal("child widened the parent profile")
	}
	narrow := mustGet(spawnSubagentWithProfileForTest(s, parent.ID, "", "narrow", "", "narrow"))(t)
	if narrow.ProfileName != "narrow" || narrow.ProfileSpec == nil || len(narrow.ProfileSpec.Tools) != 1 {
		t.Fatalf("narrow child profile not recorded: %+v", narrow)
	}
	<-started
	if _, err = spawnSubagentWithProfileForTest(s, parent.ID, "", "other executor", "other", "narrow"); err == nil {
		t.Fatal("child changed executor with a different profile")
	}
	close(release)
	await(t, s, child.ID, Completed)
	await(t, s, narrow.ID, Completed)
	await(t, s, parent.ID, Completed)
}

func TestNoSubagentsProfileWithholdsCredentials(t *testing.T) {
	var err error
	s := newTestService(t)
	registerProfileForTest(t, s, Profile{Name: "lone", NoSubagents: true})
	must(t, s.EnableSubagentControl(testSocketPath(t)))
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
	parent := mustGet(s.CreateTaskWithProfile("parent", "parent mandate", t.TempDir(), "lone"))(t)
	must(t, s.Start(context.Background(), parent.ID))
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

func TestFantasyToolPaletteAndModel(t *testing.T) {
	profile := &Profile{Name: "explorer", Tools: []string{"read", "grep"}, Model: "provider/model"}
	if got := (FantasyRunner{}).toolPalette(profile); !slices.Equal(got, []string{"read", "grep"}) {
		t.Fatalf("profile tool palette = %v", got)
	}
	if got := requestedModel(newTestService(t), "explorer", profile); got != "provider/model" {
		t.Fatalf("profile model = %q, want provider/model", got)
	}
	if got := (FantasyRunner{}).toolPalette(nil); !slices.Equal(got, agenttools.Names()) {
		t.Fatalf("default tool palette = %v, want %v", got, agenttools.Names())
	}
	if prompt := (FantasyRunner{}).systemPrompt(Task{ProfileSpec: &Profile{Name: "explorer", Instructions: "Read only; never edit."}}); !strings.Contains(prompt, "Read only; never edit.") {
		t.Fatalf("profile guidance missing from system prompt: %q", prompt)
	}
}

func TestSpecialistAndProfileCompose(t *testing.T) {
	runner := FantasyRunner{instructions: "Locate the regression."}
	prompt := runner.systemPrompt(Task{ProfileSpec: &Profile{Name: "audit", Instructions: "Record evidence."}})
	for _, want := range []string{"Locate the regression.", "Record evidence.", toolGuidance} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q in system prompt %q", want, prompt)
		}
	}
	s := newTestService(t)
	s.SetDefaultModel("default/model")
	if got := requestedModel(s, "audit", &Profile{Name: "audit", Model: "profile/model"}); got != "profile/model" {
		t.Fatalf("configured model = %q, want profile/model", got)
	}
}

func TestSpecialistLimitsAcrossDelegation(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadProfiles(writeProfileConfig(t, `{"specialists":[{"name":"reviewer","tools":["read","grep","ls"]}]}`)))
	must(t, s.SetConcurrencyLimit(3))
	must(t, s.EnableSubagentControl(testSocketPath(t)))
	const lead = "Delegate repository inspection to explorer."
	registerProfileForTest(t, s, Profile{Name: "project", CoordinatorInstructions: lead, Tools: []string{"read", "bash", "grep"}})
	started := make(chan Task, 3)
	release := make(chan struct{})
	defer close(release)
	fake := supportingRunner{runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- task
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})}
	s.RegisterRunner("implementer", fake)
	s.RegisterRunner("reviewer", fake)
	check := func(id, agent, tools string) {
		t.Helper()
		select {
		case run := <-started:
			if strings.Contains(composeInput(run, false), lead) {
				t.Fatal("specialist received parent coordinator instructions")
			}
			got := strings.Join((FantasyRunner{}).toolPalette(run.ProfileSpec), ",")
			if run.ID != id || run.Agent != agent || got != tools {
				t.Fatalf("unexpected worker %s, executor %q, tools %q", run.ID, run.Agent, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not start")
		}
	}
	parent := mustGet(s.CreateTaskWithProfile("parent", "implement a parser", t.TempDir(), "project"))(t)
	if parent.Agent != DefaultRouteAgent {
		t.Fatalf("top-level request without a mention selected %q, want %q", parent.Agent, DefaultRouteAgent)
	}
	// This test exercises implementer limits, so select it explicitly.
	must(t, s.SetTaskAgent(parent.ID, "implementer"))
	must(t, s.Start(context.Background(), parent.ID))
	check(parent.ID, "implementer", "read,bash,grep")
	child := mustGet(s.SpawnSubagent(parent.ID, "", "review the parser", ""))(t)
	check(child.ID, "reviewer", "read,grep")
	grandchild := mustGet(s.SpawnSubagent(child.ID, "", "trace the parser", "implementer"))(t)
	check(grandchild.ID, "implementer", "read,grep")
}

func TestDisjointSpecialistToolsPreventLaunch(t *testing.T) {
	var err error
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadProfiles(writeProfileConfig(t, `{"specialists":[{"name":"reviewer","tools":["read"]}]}`)))
	registerProfileForTest(t, s, Profile{Name: "shell", Tools: []string{"bash"}})
	task := mustGet(s.CreateTaskWithProfile("review", "@reviewer review the changes", t.TempDir(), "shell"))(t)
	s.RegisterRunner("reviewer", supportingRunner{runnerFunc(func(context.Context, Task, io.Writer) error {
		t.Error("runner launched with disjoint limits")
		return nil
	})})
	if err = s.Start(context.Background(), task.ID); err == nil || !strings.Contains(err.Error(), "no tools in common") {
		t.Fatalf("disjoint tools: %v", err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Status != Queued || len(got.Attempts) != 0 {
		t.Fatalf("incompatible task started: %+v", got)
	}
}

func TestAcceptedSpecialistCeilingSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadProfiles(writeProfileConfig(t, `{"specialists":[{"name":"reviewer","tools":["read"]}]}`)))
	task := createTask(t, s, "review", "@reviewer review the parser", t.TempDir())
	if task.Agent != "reviewer" || !slices.Equal(task.SpecialistTools, []string{"read"}) {
		t.Fatalf("unexpected accepted limits: %+v", task)
	}
	task.SpecialistTools[0] = "bash"
	_ = s.Shutdown()

	restarted := openTestService(t, path)
	started := make(chan Task, 1)
	restarted.RegisterRunner("reviewer", supportingRunner{runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		started <- task
		return nil
	})})
	must(t, restarted.Start(context.Background(), task.ID))
	run := <-started
	if !slices.Equal(run.ProfileSpec.Tools, []string{"read"}) {
		t.Fatalf("restarted worker received %v", run.ProfileSpec.Tools)
	}
	run.ProfileSpec.Tools[0] = "edit"
	done := await(t, restarted, task.ID, Completed)
	if !slices.Equal(done.Attempts[0].Tools, []string{"read"}) {
		t.Fatalf("attempt history changed: %v", done.Attempts[0].Tools)
	}
}
