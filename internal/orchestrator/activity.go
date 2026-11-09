package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolActivity describes observed tool execution, not inferred agent intent.
type ToolActivity struct {
	ID         string          `json:"id"`
	AttemptID  string          `json:"attempt_id"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	DurationMS *float64        `json:"duration_ms,omitempty"`
}

func (a ToolActivity) Summary() string {
	var args map[string]any
	_ = json.Unmarshal(a.Args, &args)
	for _, key := range []string{"command", "path", "file_path", "query", "pattern", "url"} {
		if value, ok := args[key].(string); ok && value != "" {
			return a.Name + ": " + strings.Join(strings.Fields(value), " ")
		}
	}
	return a.Name
}

// ToolActivityRecorder is an optional capability of a runner's output sink.
type ToolActivityRecorder interface{ RecordToolActivity(ToolActivity) error }

const maxActivities = 200
const maxActivityPayload = 16 * 1024

func boundedPayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) <= maxActivityPayload && json.Valid(raw) {
		return append(json.RawMessage(nil), raw...)
	}
	preview := string(raw)
	if len(preview) > maxActivityPayload {
		preview = preview[:maxActivityPayload]
	}
	for {
		result, _ := json.Marshal(map[string]any{"truncated": true, "preview": preview})
		if len(result) <= maxActivityPayload {
			return result
		}
		preview = preview[:len(preview)/2]
	}
}
func cloneActivities(items []ToolActivity) []ToolActivity {
	result := append([]ToolActivity(nil), items...)
	for i := range result {
		result[i].Args = append(json.RawMessage(nil), result[i].Args...)
		result[i].Result = append(json.RawMessage(nil), result[i].Result...)
		if result[i].DurationMS != nil {
			n := *result[i].DurationMS
			result[i].DurationMS = &n
		}
	}
	return result
}
func (w outputWriter) RecordToolActivity(activity ToolActivity) error {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	for i := range w.s.data.Tasks {
		task := &w.s.data.Tasks[i]
		if task.ID != w.id {
			continue
		}
		old := clone(*task)
		if len(task.Attempts) > 0 {
			activity.AttemptID = task.Attempts[len(task.Attempts)-1].ID
		}
		index := -1
		for j := range task.Activities {
			a := &task.Activities[j]
			if a.ID == activity.ID && a.AttemptID == activity.AttemptID {
				index = j
				break
			}
		}
		if index < 0 {
			task.Activities = append(task.Activities, ToolActivity{ID: activity.ID, AttemptID: activity.AttemptID, Name: activity.Name, StartedAt: time.Now().UTC()})
			index = len(task.Activities) - 1
		}
		a := &task.Activities[index]
		if activity.Name != "" {
			a.Name = activity.Name
		}
		if len(activity.Args) > 0 {
			a.Args = boundedPayload(activity.Args)
		}
		if len(activity.Result) > 0 {
			a.Result = boundedPayload(activity.Result)
		}
		if activity.Status != "" {
			a.Status = activity.Status
		}
		if activity.DurationMS != nil {
			n := *activity.DurationMS
			a.DurationMS = &n
		}
		if a.Status == "finished" || a.Status == "failed" {
			a.FinishedAt = time.Now().UTC()
		}
		if len(task.Activities) > maxActivities {
			task.Activities = append([]ToolActivity(nil), task.Activities[len(task.Activities)-maxActivities:]...)
		}
		if err := w.s.save(); err != nil {
			*task = old
			return err
		}
		return nil
	}
	return fmt.Errorf("task not found")
}
func interruptActivities(task *Task) {
	for i := range task.Activities {
		a := &task.Activities[i]
		if a.Status == "running" {
			a.Status = "interrupted"
			a.FinishedAt = time.Now().UTC()
		}
	}
}
