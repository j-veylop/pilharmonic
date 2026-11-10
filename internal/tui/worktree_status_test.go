package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestWorkspaceBadgeDistinguishesWorkspaceAndMergeState(t *testing.T) {
	cases := []struct {
		name  string
		task  orchestrator.Task
		glyph string
		label string
	}{
		{"shared", orchestrator.Task{}, glyphShared, "shared"},
		{"isolated", orchestrator.Task{Isolated: true}, glyphWorktree, "isolated"},
		{"inherited", orchestrator.Task{ParentID: "parent"}, glyphInherited, "inherited"},
		{"merged", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeMerged}, glyphMerged, "merged"},
		{"up to date", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeUpToDate}, glyphCheck, "up to date"},
		{"merge failed", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeFailed, MergeError: "source repository already has an in-progress merge"}, glyphMergeError, "merge error"},
		{"merge conflict", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeFailed, MergeError: "CONFLICT (content): Merge conflict in tracked.txt"}, glyphConflict, "conflict"},
		{"review pending", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeMerged, ReviewStatus: orchestrator.ReviewPending}, glyphReview, "review"},
		{"review rejected", orchestrator.Task{Isolated: true, MergeStatus: orchestrator.MergeMerged, ReviewStatus: orchestrator.ReviewRejected}, glyphMergeError, "rejected"},
	}
	for _, tc := range cases {
		badge := workspaceBadgeFor(tc.task)
		if badge.glyph != tc.glyph || badge.label != tc.label {
			t.Fatalf("%s: got %q %q, want %q %q", tc.name, badge.glyph, badge.label, tc.glyph, tc.label)
		}
		if rendered := ansi.Strip(badge.render()); !strings.Contains(rendered, tc.glyph) || !strings.Contains(rendered, tc.label) {
			t.Fatalf("%s: rendered badge %q", tc.name, rendered)
		}
	}
}

func TestTaskBoardShowsWorkspaceAndMergeBadge(t *testing.T) {
	task := orchestrator.Task{
		ID: "task", Title: "isolated work", Prompt: "work", Status: orchestrator.Running,
		Isolated: true, ExecutionWorkspace: "/state/workspaces/task/tree", IsolationCommit: "abc123",
	}
	m := model{width: 120, height: 35, tasks: []orchestrator.Task{task}}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, glyphWorktree) || !strings.Contains(view, "isolated") {
		t.Fatalf("isolated task badge missing: %s", view)
	}

	task.Status = orchestrator.Completed
	task.MergeStatus = orchestrator.MergeMerged
	task.MergeCommit = "def456"
	m.tasks[0] = task
	view = ansi.Strip(m.View())
	if !strings.Contains(view, glyphMerged) || !strings.Contains(view, "merged") {
		t.Fatalf("merged task badge missing: %s", view)
	}

	task.MergeStatus = orchestrator.MergeFailed
	task.MergeError = "CONFLICT (content): Merge conflict in tracked.txt"
	m.tasks[0] = task
	view = ansi.Strip(m.View())
	if !strings.Contains(view, glyphConflict) || !strings.Contains(view, "conflict") || !strings.Contains(view, "MERGE FAILED (conflict)") {
		t.Fatalf("merge conflict badge missing: %s", view)
	}
}

func TestRouterRecentRoutesShowWorkspaceBadge(t *testing.T) {
	m := model{router: true, width: 120, height: 35, tasks: []orchestrator.Task{{
		ID: "task", Title: "merged work", Status: orchestrator.Completed, Agent: "general",
		CreatedAt: time.Now(), Isolated: true, MergeStatus: orchestrator.MergeMerged, MergeCommit: "def456",
	}}}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, glyphMerged+" merged") {
		t.Fatalf("recent route missing workspace badge: %s", view)
	}
}

func TestTaskDetailShowsReviewState(t *testing.T) {
	task := orchestrator.Task{
		ID: "task", Title: "reviewed work", Prompt: "work", Status: orchestrator.Completed,
		Isolated: true, MergeStatus: orchestrator.MergeMerged, MergeCommit: "def456",
		ReviewStatus: orchestrator.ReviewPending, ReviewCommit: "abc123",
	}
	m := model{width: 120, height: 35, tasks: []orchestrator.Task{task}}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "REVIEW PENDING") || !strings.Contains(view, "abc123") || !strings.Contains(view, "v approve / V reject") {
		t.Fatalf("pending review not shown: %s", view)
	}

	task.ReviewStatus = orchestrator.ReviewRejected
	task.ReviewedBy = orchestrator.LocalActor
	task.ReviewedAt = time.Now()
	task.ReviewNote = "not needed"
	m.tasks[0] = task
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "REVIEW REJECTED") || !strings.Contains(view, orchestrator.LocalActor) || !strings.Contains(view, "not needed") {
		t.Fatalf("rejection not shown: %s", view)
	}
}
