package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"orchestrator/internal/orchestrator"
)

type cleanupKind string

const (
	deleteTask      cleanupKind = "d"
	deleteCompleted cleanupKind = "D"
	discardTask     cleanupKind = "x"
	discardMerged   cleanupKind = "X"
)

type cleanupRequest struct {
	kind  cleanupKind
	ids   []string
	title string
}

type cleanupResultMsg string

func (m *model) prepareCleanup(key string) {
	if m.cleanupBusy {
		m.notice = "Workspace cleanup is in progress."
		return
	}
	request := cleanupRequest{kind: cleanupKind(key)}
	switch request.kind {
	case deleteCompleted:
		request.ids = m.service.CompletedDeletionCandidates()
		if len(request.ids) == 0 {
			m.notice = "No completed tasks to delete."
			return
		}
	case discardMerged:
		request.ids = m.service.WorktreeDiscardCandidates()
		if len(request.ids) == 0 {
			m.notice = "No merged worktrees to discard."
			return
		}
	case deleteTask, discardTask:
		if len(m.tasks) == 0 {
			return
		}
		task := m.tasks[m.selected]
		if request.kind == discardTask && (!task.Isolated || task.ExecutionWorkspace == "") {
			m.notice = "Selected task has no retained worktree."
			return
		}
		if task.Status == orchestrator.Running {
			m.notice = "Cancel and stop the running task before cleanup."
			return
		}
		request.ids = []string{task.ID}
		request.title = task.Title
	}
	m.cleanup = request
	m.notice = ""
}

func (m model) cleanupKey(key string) (model, tea.Cmd) {
	request := m.cleanup
	switch key {
	case "y":
		m.cleanup = cleanupRequest{}
		m.cleanupBusy = true
		m.notice = "Workspace cleanup is in progress."
		service := m.service
		return m, func() tea.Msg { return cleanupResultMsg(request.run(service)) }
	case "n", "esc":
		m.cleanup = cleanupRequest{}
		m.notice = "Cleanup canceled."
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (r cleanupRequest) run(service *orchestrator.Service) string {
	switch r.kind {
	case deleteTask:
		if err := service.DeleteTask(r.ids[0]); err != nil {
			return err.Error()
		}
		return "Task deleted with history and worktree."
	case discardTask:
		if err := service.DiscardWorktree(r.ids[0]); err != nil {
			return err.Error()
		}
		return "Worktree discarded; task history kept."
	case deleteCompleted:
		count, err := service.DeleteCompletedTasks(r.ids)
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("Deleted %d completed task(s) and their history.", count)
	case discardMerged:
		count, err := service.DiscardMergedWorktrees(r.ids)
		if err != nil {
			return fmt.Sprintf("Discarded %d merged worktree(s); %v", count, err)
		}
		return fmt.Sprintf("Discarded %d merged worktree(s).", count)
	}
	return "Unknown cleanup request."
}
