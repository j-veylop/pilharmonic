package orchestrator

import (
	"path/filepath"
	"testing"
)

func TestInstanceLockExcludesSecondHolder(t *testing.T) {
	var err error
	statePath := filepath.Join(t.TempDir(), "state.json")
	release := mustGet(AcquireInstanceLock(statePath))(t)
	if _, err = AcquireInstanceLock(statePath); err == nil {
		t.Fatal("second instance lock on the same state file succeeded")
	}
	release()
	release()
	if _, err = AcquireInstanceLock(statePath); err != nil {
		t.Fatalf("lock not reusable after release: %v", err)
	}
}

func TestInstanceLockRequiresStatePath(t *testing.T) {
	if _, err := AcquireInstanceLock(""); err == nil {
		t.Fatal("expected error for empty state path")
	}
}
