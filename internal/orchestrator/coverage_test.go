package orchestrator

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPolicyIdentities(t *testing.T) {
	if (localPolicy{}).ID() != LocalPolicyID || (localPolicy{}).Version() != LocalPolicyVersion {
		t.Fatal("local policy identity drifted from its recorded constants")
	}
	if (subagentPolicy{}).ID() != SubagentPolicyID || (subagentPolicy{}).Version() != SubagentPolicyVersion {
		t.Fatal("subagent policy identity drifted from its recorded constants")
	}
}

func TestProfileErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("executor cannot honor profile")
	wrapped := profileError{err: cause}
	if !errors.Is(wrapped, cause) {
		t.Fatal("profile error did not unwrap to its cause")
	}
	if wrapped.Error() != cause.Error() {
		t.Fatalf("profile error text %q", wrapped.Error())
	}
	if !isProfileError(wrapped) {
		t.Fatal("profile error not recognised by isProfileError")
	}
}

func TestAddManualContextRecordsTrigger(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("ctx", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddManualContext(task.ID, "extra context"); err != nil {
		t.Fatal(err)
	}
	updated, ok := s.GetTask(task.ID)
	if !ok || len(updated.Context) != 1 || updated.Context[0].Text != "extra context" {
		t.Fatalf("context not recorded: %+v", updated.Context)
	}
	if updated.Prompt != "original mandate" {
		t.Fatalf("mandate changed to %q", updated.Prompt)
	}
}

func TestAddManualThreadContextReachesSubscribers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("thread", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddManualThreadContext(task.ID, "shared context"); err != nil {
		t.Fatal(err)
	}
	updated, ok := s.GetTask(task.ID)
	if !ok || len(updated.Context) != 1 || updated.Context[0].Text != "shared context" {
		t.Fatalf("thread context not recorded: %+v", updated.Context)
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
