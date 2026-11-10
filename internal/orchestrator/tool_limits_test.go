package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSpecialistLimitsAcrossDelegation(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.LoadBuiltinSpecialists()
	if err := s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read","grep","ls"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConcurrencyLimit(3); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableSubagentControl(filepath.Join(t.TempDir(), "control.sock")); err != nil {
		t.Fatal(err)
	}
	const lead = "Delegate repository inspection to explorer."
	if err := s.RegisterProfile(Profile{Name: "project", CoordinatorInstructions: lead, Tools: []string{"read", "bash", "grep"}}); err != nil {
		t.Fatal(err)
	}
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
			if strings.Contains(piPrompt(run), lead) {
				t.Fatal("specialist received parent coordinator instructions")
			}
			args := (PiRunner{}).piArgs("rpc", run.ProfileSpec)
			i := slices.Index(args, "--tools")
			if run.ID != id || run.Agent != agent || i < 0 || i+1 >= len(args) || args[i+1] != tools {
				t.Fatalf("unexpected worker %s, executor %q, arguments %v", run.ID, run.Agent, args)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not start")
		}
	}
	parent, err := s.CreateTaskWithProfile("parent", "implement a parser", t.TempDir(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if parent.Agent != DefaultRouteAgent {
		t.Fatalf("top-level request without a mention selected %q, want %q", parent.Agent, DefaultRouteAgent)
	}
	// This test exercises implementer limits, so select it explicitly.
	if err := s.SetTaskAgent(parent.ID, "implementer"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	check(parent.ID, "implementer", "read,bash,grep")
	child, err := s.SpawnSubagent(parent.ID, "", "review the parser", "")
	if err != nil {
		t.Fatal(err)
	}
	check(child.ID, "reviewer", "read,grep")
	grandchild, err := s.SpawnSubagent(child.ID, "", "trace the parser", "implementer")
	if err != nil {
		t.Fatal(err)
	}
	check(grandchild.ID, "implementer", "read,grep")
}

func TestDisjointSpecialistToolsPreventLaunch(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.LoadBuiltinSpecialists()
	if err := s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterProfile(Profile{Name: "shell", Tools: []string{"bash"}}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTaskWithProfile("review", "@reviewer review the changes", t.TempDir(), "shell")
	if err != nil {
		t.Fatal(err)
	}
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
