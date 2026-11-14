package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivityPersistenceAndSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := mustGet(Open(path))(t)
	task := createTask(t, s, "test", "work", t.TempDir())
	defer func() { _ = s.Shutdown() }()
	writers := make(chan outputWriter, 1)
	finish := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, out io.Writer) error {
		writers <- out.(outputWriter)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	must(t, s.Start(context.Background(), task.ID))
	w := <-writers
	must(t, w.RecordToolActivity(ToolActivity{ID: "call", Name: "bash", Status: "running", Args: json.RawMessage(`{"command":"go test ./..."}`)}))
	must(t, w.RecordToolActivity(ToolActivity{ID: "call", Status: "finished", Result: json.RawMessage(`{"content":[{"type":"text","text":"PASS"}]}`)}))
	close(finish)
	snapshot := await(t, s, task.ID, Completed)
	if len(snapshot.Activities) != 1 || snapshot.Activities[0].Summary() != "bash: go test ./..." {
		t.Fatalf("bad activities %+v", snapshot.Activities)
	}
	snapshot.Activities[0].Args[0] = 'x'
	restored := mustGet(Open(path))(t)
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
