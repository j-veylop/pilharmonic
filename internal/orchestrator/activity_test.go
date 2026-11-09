package orchestrator

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivityPersistenceAndSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("test", "work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := outputWriter{s, task.ID}
	if err = w.RecordToolActivity(ToolActivity{ID: "call", Name: "bash", Status: "running", Args: json.RawMessage(`{"command":"go test ./..."}`)}); err != nil {
		t.Fatal(err)
	}
	if err = w.RecordToolActivity(ToolActivity{ID: "call", Status: "finished", Result: json.RawMessage(`{"content":[{"type":"text","text":"PASS"}]}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.GetTask(task.ID)
	if len(snapshot.Activities) != 1 || snapshot.Activities[0].Summary() != "bash: go test ./..." {
		t.Fatalf("bad activities %+v", snapshot.Activities)
	}
	snapshot.Activities[0].Args[0] = 'x'
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := restored.GetTask(task.ID)
	if !json.Valid(got.Activities[0].Args) || got.Activities[0].Status != "finished" || !strings.Contains(string(got.Activities[0].Result), "PASS") {
		t.Fatal(got.Activities)
	}
	inMemory, _ := s.GetTask(task.ID)
	if !json.Valid(inMemory.Activities[0].Args) {
		t.Fatal("snapshot mutation escaped")
	}
	large := boundedPayload(json.RawMessage(`"` + strings.Repeat("x", 20000) + `"`))
	if !json.Valid(large) || !strings.Contains(string(large), `"truncated":true`) {
		t.Fatal("invalid truncation")
	}
}
