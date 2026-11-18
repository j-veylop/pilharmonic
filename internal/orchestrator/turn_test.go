package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func TestStructuredTurnsPersistAndReplay(t *testing.T) {
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
	must(t, w.RecordTurn(Turn{Role: "assistant", Text: "Checking the file"}))
	must(t, w.RecordTurn(Turn{Role: "assistant", ToolCallID: "call-1", ToolName: "read", Input: `{"path":"a.go"}`}))
	must(t, w.RecordTurn(Turn{Role: "tool", ToolCallID: "call-1", ToolName: "read", Output: "package main"}))
	close(finish)
	snapshot := await(t, s, task.ID, Completed)

	restored := mustGet(Open(path))(t)
	defer func() { _ = restored.Shutdown() }()
	persisted, _ := restored.GetTask(task.ID)
	if len(persisted.Attempts) != 1 || len(persisted.Attempts[0].Turns) != 3 {
		t.Fatalf("turns not persisted: %+v", persisted.Attempts)
	}

	prompt, history := composeConversation(persisted)
	if !strings.Contains(prompt, "Original mandate") {
		t.Fatalf("prompt lost the mandate: %q", prompt)
	}
	if strings.Contains(prompt, "Previous assistant output") {
		t.Fatalf("structured turns were replayed as prose: %q", prompt)
	}
	var sawCall, sawResult bool
	for _, message := range history {
		for _, part := range message.Content {
			switch value := part.(type) {
			case fantasy.ToolCallPart:
				sawCall = value.ToolCallID == "call-1" && value.ToolName == "read"
			case fantasy.ToolResultPart:
				output, ok := value.Output.(fantasy.ToolResultOutputContentText)
				sawResult = ok && value.ToolCallID == "call-1" && strings.Contains(output.Text, "package main")
			}
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("tool call and result not replayed: %+v", snapshot.Attempts[0].Turns)
	}
}

func TestAttemptRetentionDropsTurnsWithOutput(t *testing.T) {
	task := Task{Attempts: []Attempt{
		{Turns: []Turn{{Role: "assistant", Text: "old"}}},
		{Turns: []Turn{{Role: "assistant", Text: "kept"}}},
	}}
	if dropped := retainAttemptOutputs(&task, 1); dropped != 1 {
		t.Fatalf("dropped %d, want 1", dropped)
	}
	if task.Attempts[0].Turns != nil || !task.Attempts[0].OutputTruncated || len(task.Attempts[1].Turns) != 1 {
		t.Fatalf("retention touched the wrong attempt: %+v", task.Attempts)
	}
}

func TestTrimConversationDropsLeadingToolResults(t *testing.T) {
	messages := []fantasy.Message{
		{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: strings.Repeat("x", 100)}}},
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: "c", ToolName: "read"}}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "c", Output: fantasy.ToolResultOutputContentText{Text: strings.Repeat("y", 100)}}}},
	}
	trimmed := trimConversation(messages, 120)
	if len(trimmed) == 0 || trimmed[0].Role == fantasy.MessageRoleTool {
		t.Fatalf("trim left a result without its call: %+v", trimmed)
	}
}
