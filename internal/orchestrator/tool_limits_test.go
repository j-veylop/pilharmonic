package orchestrator

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSpecialistLimitsAcrossDelegation(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read","grep","ls"]}`)))
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
			if strings.Contains(composeInput(run), lead) {
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
	must(t, s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read"]}`)))
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
