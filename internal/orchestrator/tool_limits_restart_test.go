package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"testing"
)

func TestAcceptedSpecialistCeilingSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read"]}`)))
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
