package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// Each workspace and merge state has its own badge on the board, also under
// the request box, and the OUTPUT and INFO tabs explain what the state means.
func TestWorkspaceBadgeDistinguishesWorkspaceAndMergeState(t *testing.T) {
	conflict := "CONFLICT (content): Merge conflict in tracked.txt"
	rejected := orchestrator.Task{Isolated: true, Status: orchestrator.Completed, ExecutionWorkspace: "/state/workspaces/task/tree", ReviewStatus: orchestrator.ReviewRejected,
		ReviewedBy: orchestrator.LocalActor, ReviewedAt: time.Now(), ReviewNote: "not needed"}
	cases := []struct {
		name   string
		task   orchestrator.Task
		glyph  string
		label  string
		detail []string
	}{
		{"shared", orchestrator.Task{}, glyphShared, "shared", nil},
		{"isolated", orchestrator.Task{Isolated: true, Status: orchestrator.Running, ExecutionWorkspace: "/state/workspaces/task/tree", IsolationCommit: "abc123"}, glyphWorktree, "isolated", nil},
		{"inherited", orchestrator.Task{ParentID: "parent"}, glyphInherited, "inherited", nil},
		{"merged", orchestrator.Task{Isolated: true, Status: orchestrator.Completed, MergeStatus: orchestrator.MergeMerged, MergeCommit: "def456"}, glyphMerged, "merged", nil},
		{"up to date", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeUpToDate}, glyphCheck, "up to date", nil},
		{"merge failed", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeFailed, MergeError: "source repository already has an in-progress merge"}, glyphMergeError, "merge error", nil},
		{"merge conflict", orchestrator.Task{Isolated: true, Status: orchestrator.Completed, MergeStatus: orchestrator.MergeFailed, MergeError: conflict}, glyphConflict, "conflict", []string{"MERGE FAILED (conflict)"}},
		{"review pending", orchestrator.Task{Isolated: true, Status: orchestrator.Completed, ExecutionWorkspace: "/state/workspaces/task/tree", ReviewStatus: orchestrator.ReviewPending, ReviewCommit: "abc123"}, glyphReview, "review",
			[]string{"REVIEW PENDING", "abc123", "merge blocked until approval", "v to review"}},
		{"review rejected", rejected, glyphMergeError, "rejected",
			[]string{"REVIEW REJECTED", orchestrator.LocalActor, "not needed", "Worktree retained", "f adds follow-up"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			badge := workspaceBadgeFor(tc.task)
			if badge.glyph != tc.glyph || badge.label != tc.label {
				t.Fatalf("got %q %q, want %q %q", badge.glyph, badge.label, tc.glyph, tc.label)
			}
			tc.task.ID, tc.task.Title, tc.task.Prompt, tc.task.Agent, tc.task.CreatedAt = "task", "work", "work", "general", time.Now()
			board := ansi.Strip(model{width: 120, height: 35, tasks: []orchestrator.Task{tc.task}}.View() +
				model{width: 120, height: 35, tasks: []orchestrator.Task{tc.task}, tab: infoTab, detailFocus: true}.View() +
				model{width: 120, height: 35, tasks: []orchestrator.Task{tc.task}, detailFocus: true}.View())
			for _, want := range append([]string{tc.glyph + " " + tc.label}, tc.detail...) {
				if !strings.Contains(board, want) {
					t.Fatalf("board missing %q: %s", want, board)
				}
			}
			recent := ansi.Strip(model{router: routerState{open: true}, width: 120, height: 35, tasks: []orchestrator.Task{tc.task}}.View())
			if !strings.Contains(recent, tc.glyph+" "+tc.label) {
				t.Fatalf("recent task missing workspace badge: %s", recent)
			}
		})
	}
}
