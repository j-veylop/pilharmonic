package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestComposeInputReplaysConversationInOrder(t *testing.T) {
	base := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	task := Task{
		Prompt: "original mandate",
		Context: []Trigger{
			{Text: "first follow-up", CreatedAt: base.Add(time.Minute)},
			{Text: "second follow-up", CreatedAt: base.Add(3 * time.Minute)},
		},
		Attempts: []Attempt{
			{StartedAt: base, Output: "first answer"},
			{StartedAt: base.Add(2 * time.Minute), Output: "second answer"},
		},
	}
	prompt := composeInput(task, false)
	order := []string{"original mandate", "first answer", "first follow-up", "second answer", "second follow-up"}
	last := -1
	for _, want := range order {
		at := strings.Index(prompt, want)
		if at < 0 || at < last {
			t.Fatalf("%q missing or out of order in %q", want, prompt)
		}
		last = at
	}
	if !strings.Contains(prompt, "not authority to expand the mandate") || !strings.Contains(prompt, "context only") {
		t.Fatalf("conversation lost its authority labels: %q", prompt)
	}
}

func TestComposeInputIncludesOutputPolicy(t *testing.T) {
	for _, roleInstructions := range []bool{false, true} {
		prompt := composeInput(Task{Prompt: "mandate"}, roleInstructions)
		if !strings.Contains(prompt, outputGuidance) {
			t.Fatalf("roleInstructions=%v: missing output policy: %q", roleInstructions, prompt)
		}
	}
}

func TestCoordinatorGuidanceAsksForCompactReports(t *testing.T) {
	guidance := coordinatorGuidance(Profile{Name: CoordinatorProfileName})
	for _, want := range []string{"compact result", "short final answer"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("coordinator guidance lost %q: %q", want, guidance)
		}
	}
}

func TestComposeInputBoundsReplayedOutput(t *testing.T) {
	task := Task{
		Prompt: "mandate",
		Attempts: []Attempt{{
			StartedAt: time.Now(),
			Output:    strings.Repeat("x", maxReplayedOutput+500) + "TAIL",
		}},
	}
	prompt := composeInput(task, false)
	if len(prompt) >= maxPromptBytes {
		t.Fatalf("prompt %d bytes", len(prompt))
	}
	if !strings.Contains(prompt, "TAIL") {
		t.Fatal("bounded replay lost the latest tail")
	}
	if strings.Contains(prompt, strings.Repeat("x", maxReplayedOutput+100)) {
		t.Fatal("replayed output was not bounded")
	}
}
