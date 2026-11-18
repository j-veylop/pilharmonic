package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectContextBlockIgnoresMissingFilesAndReadsFallbacks(t *testing.T) {
	root := t.TempDir()
	if block := projectContextBlock(Task{Workspace: root}); block != "" {
		t.Fatalf("empty workspace produced project context: %q", block)
	}
	must(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("follow the house style\n"), 0o644))
	block := projectContextBlock(Task{Workspace: root})
	if !strings.Contains(block, "CLAUDE.md") || !strings.Contains(block, "follow the house style") {
		t.Fatalf("fallback context file missing: %q", block)
	}
}

func TestProjectContextBlockIsBounded(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", maxContextFileBytes+2<<10)
	must(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(big), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(big), 0o644))
	block := projectContextBlock(Task{Workspace: root})
	if len(block) > maxContextTotalBytes+4<<10 {
		t.Fatalf("project context is %d bytes, budget is %d", len(block), maxContextTotalBytes)
	}
	if !strings.Contains(block, "[truncated]") {
		t.Fatalf("oversized context is not marked as truncated: %q", block[:min(len(block), 200)])
	}
	if !strings.Contains(block, "AGENTS.md") || !strings.Contains(block, "CLAUDE.md") {
		t.Fatalf("context sources missing: %q", block[:min(len(block), 200)])
	}
}

func TestIsolatedTaskReadsProjectContextFromSourceCheckout(t *testing.T) {
	source := t.TempDir()
	worktree := t.TempDir()
	must(t, os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("source instructions\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(worktree, "AGENTS.md"), []byte("worktree instructions\n"), 0o644))

	block := projectContextBlock(Task{Workspace: worktree, sourceWorkspace: source, ExecutionWorkspace: worktree})
	if !strings.Contains(block, "source instructions") {
		t.Fatalf("source checkout context missing: %q", block)
	}
	if strings.Contains(block, "worktree instructions") {
		t.Fatalf("isolated task read context an agent could author: %q", block)
	}
}

func TestEnvironmentBlockDescribesExecutionRootAndIsolation(t *testing.T) {
	source := t.TempDir()
	worktree := t.TempDir()
	block := environmentBlock(Task{Workspace: worktree, sourceWorkspace: source, ExecutionWorkspace: worktree})
	for _, want := range []string{"Workspace: " + worktree, "Platform: ", "Today: ", "isolated Git worktree"} {
		if !strings.Contains(block, want) {
			t.Fatalf("environment block missing %q: %q", want, block)
		}
	}
	if strings.Contains(block, "Git repository: yes") {
		t.Fatalf("a non-repository directory was reported as a Git repository: %q", block)
	}
}
