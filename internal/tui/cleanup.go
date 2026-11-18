package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

type cleanupKind string

const (
	deleteTask      cleanupKind = "d"
	deleteCompleted cleanupKind = "D"
	discardTask     cleanupKind = "x"
	discardMerged   cleanupKind = "X"
	forceDiscard    cleanupKind = "ctrl+x"
)

type cleanupRequest struct {
	kind  cleanupKind
	ids   []string
	title string
}

type cleanupResultMsg string

func (m *model) prepareCleanup(kind cleanupKind) {
	if m.cleanupBusy {
		m.notice.text = "Workspace cleanup is in progress."
		return
	}
	request := cleanupRequest{kind: kind}
	switch request.kind {
	case deleteCompleted:
		request.ids = m.service.CompletedDeletionCandidates()
		if len(request.ids) == 0 {
			m.notice.text = "No completed tasks to delete."
			return
		}
	case discardMerged:
		request.ids = m.service.WorktreeDiscardCandidates()
		if len(request.ids) == 0 {
			m.notice.text = "No merged worktrees to discard."
			return
		}
	case deleteTask, discardTask, forceDiscard:
		task, ok := m.selectedTask()
		if !ok {
			return
		}
		if (request.kind == discardTask || request.kind == forceDiscard) && (!task.Isolated || task.ExecutionWorkspace == "") {
			m.notice.text = "Selected task has no retained worktree."
			return
		}
		if task.Status == orchestrator.Running {
			m.notice.text = "Cancel and stop the running task before cleanup."
			return
		}
		request.ids = []string{task.ID}
		request.title = task.Title
	}
	m.cleanup = request
	m.notice.text = ""
}

func (m model) cleanupKey(key string) (model, tea.Cmd) {
	request := m.cleanup
	switch key {
	case "y":
		m.cleanup = cleanupRequest{}
		m.cleanupBusy = true
		m.notice.text = "Workspace cleanup is in progress."
		service := m.service
		return m, func() tea.Msg { return cleanupResultMsg(request.run(service)) }
	case "n", "esc":
		m.cleanup = cleanupRequest{}
		m.notice.text = "Cleanup canceled."
	case "ctrl+c":
		return m.requestQuit(), nil
	}
	return m, nil
}

// prompt is the confirmation shown in the action dialog.
func (r cleanupRequest) prompt() (title, answer, note string) {
	switch r.kind {
	case deleteTask:
		return "DELETE TASK + HISTORY  › " + singleLine(r.title), "y delete", "Removes its worktree; source workspace is untouched."
	case deleteCompleted:
		return fmt.Sprintf("DELETE %d COMPLETED TASK(S) + HISTORY", len(r.ids)), "y delete", "Tasks with unfinished children are kept; worktrees are removed."
	case discardTask:
		return "DISCARD RETAINED WORKTREE  › " + singleLine(r.title), "y discard", "Refuses uncommitted or unmerged work; task history is kept."
	case forceDiscard:
		return "PERMANENTLY REMOVE WORKTREE  › " + singleLine(r.title), "y remove", "Permanently deletes all uncommitted and unmerged work. Task history is kept."
	}
	return fmt.Sprintf("DISCARD %d MERGED WORKTREE(S)", len(r.ids)), "y discard", "Skips uncommitted or unmerged work; task history is kept."
}

func (r cleanupRequest) run(service Service) string {
	switch r.kind {
	case deleteTask:
		if err := service.DeleteTask(r.ids[0]); err != nil {
			return err.Error()
		}
		return "Task deleted with history and worktree."
	case forceDiscard:
		if err := service.ForceDiscardWorktree(r.ids[0]); err != nil {
			return err.Error()
		}
		return "Worktree removed; task history kept."
	case discardTask:
		if err := service.DiscardWorktree(r.ids[0]); err != nil {
			return err.Error()
		}
		return "Worktree discarded; task history kept."
	case deleteCompleted:
		count, skipped, err := service.DeleteCompletedTasksDetailed(r.ids)
		if err != nil {
			return err.Error()
		}
		if len(skipped) == 0 {
			return fmt.Sprintf("Deleted %d completed task(s) and their history.", count)
		}
		return fmt.Sprintf("Deleted %d completed task(s); kept %d (%s).", count, len(skipped), skipped[0].Reason)
	case discardMerged:
		count, err := service.DiscardMergedWorktrees(r.ids)
		if err != nil {
			return fmt.Sprintf("Discarded %d merged worktree(s); %v", count, err)
		}
		return fmt.Sprintf("Discarded %d merged worktree(s).", count)
	}
	return "Unknown cleanup request."
}

// reviewPrompt asks how to decide a review. The task and the displayed commit
// are captured when it opens, so a newer attempt is never approved by mistake.
type reviewPrompt struct {
	target, commit, title string
}

type reviewDiffState struct {
	target, commit, title, diff string
	loading                     bool
}

func (m model) loadReviewDiff(id, commit, title string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		diff, err := m.service.ReviewDiff(ctx, id, commit)
		return reviewDiffMsg{target: id, commit: commit, title: title, diff: diff, err: err}
	}
}

func (m model) reviewKey(key string) (model, tea.Cmd) {
	r := m.review
	switch key {
	case "y":
		m.review = reviewPrompt{}
		return m, serviceCmd("Review approved for commit "+r.commit+"; merge scheduled.", func(s Service) error { return s.ApproveReview(r.target, r.commit, "") }, m.service)
	case "n":
		m.review = reviewPrompt{}
		m.openComposer(rejectReviewInput, r.target, "Enter a reason to reject; the worktree is kept for f follow-up.")
		m.input.reviewCommit = r.commit
	case "esc":
		m.review = reviewPrompt{}
		m.notice.text = "Review canceled."
	case "ctrl+c":
		return m.requestQuit(), nil
	}
	return m, nil
}
