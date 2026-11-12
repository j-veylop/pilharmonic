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
	if err := s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read"]}`)); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("review", "@reviewer review the parser", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	if err := restarted.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
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
