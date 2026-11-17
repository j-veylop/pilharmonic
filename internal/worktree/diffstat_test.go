package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	source := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "base"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "base"}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return source
}

func TestDiffStatCountsUncommittedAndPreparedChurn(t *testing.T) {
	source := gitTestRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	spec := Spec{StatePath: statePath, TaskID: "stat", Workspace: source, Title: "stat"}
	execution, _, err := Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Remove(context.Background(), spec) })
	spec.Execution = execution
	if err := os.WriteFile(filepath.Join(execution, "base"), []byte("base\nmore\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(execution, "untracked"), []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}

	added, removed, err := DiffStat(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if added != 3 || removed != 0 {
		t.Fatalf("working-tree stat = +%d -%d, want +3 -0", added, removed)
	}

	prepared, err := Commit(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	added, removed, err = DiffStat(context.Background(), spec, prepared.WorktreeCommit)
	if err != nil {
		t.Fatal(err)
	}
	if added != 3 || removed != 0 {
		t.Fatalf("prepared stat = +%d -%d, want +3 -0", added, removed)
	}
}
