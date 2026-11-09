package tui

import (
	"encoding/json"
	"orchestrator/internal/orchestrator"
	"testing"
)

func TestActivityFiltersAndCurrentAction(t *testing.T) {
	task := orchestrator.Task{Status: orchestrator.Running, Attempts: []orchestrator.Attempt{{ID: "old"}, {ID: "new"}}, Activities: []orchestrator.ToolActivity{
		{ID: "same", AttemptID: "old", Status: "failed"},
		{ID: "same", AttemptID: "new", Name: "bash", Status: "running", Args: json.RawMessage(`{"command":"go test ./..."}`)},
	}}
	m := model{tasks: []orchestrator.Task{task}, activity: true}
	if currentAction(task) != "▶ bash: go test ./..." {
		t.Fatal(currentAction(task))
	}
	m.activityKey("v")
	if len(m.visibleActivities()) != 1 || m.visibleActivities()[0].AttemptID != "new" {
		t.Fatal("latest filter")
	}
	m.activityKey("enter")
	if !m.activityExpanded || m.activityAttempt != "new" {
		t.Fatal("wrong expanded attempt")
	}
	m.activityKey("v")
	if m.activityExpanded || len(m.visibleActivities()) != 1 || m.visibleActivities()[0].Status != "failed" {
		t.Fatal("error filter")
	}
	m.activityKey("v")
	if len(m.visibleActivities()) != 2 {
		t.Fatal("all filter")
	}
	task.Status = orchestrator.Completed
	if currentAction(task) != "" {
		t.Fatal("finished task showed current action")
	}
}
