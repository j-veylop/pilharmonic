package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiRPCAcknowledgesFollowup(t *testing.T) {
	script := `read initial
printf '%s\n' '{"type":"response","id":"initial","success":true,"data":{"disposition":"started"}}'
read follow
printf '%s\n' '{"type":"response","id":"f1","success":true,"data":{"disposition":"queued"}}' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Done"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}' '{"type":"agent_settled"}'
while read line; do :; done
`
	follow := make(chan Trigger, 1)
	follow <- Trigger{ID: "f1", Text: "existing style"}
	var output bytes.Buffer
	acknowledgements := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := (PiRunner{Command: fakePi(t, script)}).RunLive(ctx, Task{Prompt: "do work", Workspace: t.TempDir()}, &output, follow, func(id string, ok bool, reason string) {
		if ok {
			acknowledgements <- id + ":" + reason
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-acknowledgements; got != "f1:queued" {
		t.Fatal(got)
	}
	if !strings.Contains(output.String(), "Done") {
		t.Fatal(output.String())
	}
}

type liveTestRunner struct {
	started  chan struct{}
	received chan Trigger
	release  chan struct{}
}

func (r liveTestRunner) Run(context.Context, Task, io.Writer) error { return nil }
func (r liveTestRunner) RunLive(ctx context.Context, _ Task, _ io.Writer, follow <-chan Trigger, ack func(string, bool, string)) error {
	close(r.started)
	select {
	case trigger := <-follow:
		r.received <- trigger
		ack(trigger.ID, true, "queued")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestLiveContextRecordedAndAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runner := liveTestRunner{make(chan struct{}), make(chan Trigger, 1), make(chan struct{})}
	s.RegisterRunner("pi", runner)
	task, err := s.CreateTask("work", "original", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err = s.AddContext(task.ID, "follow-id", "context"); err != nil {
		t.Fatal(err)
	}
	if got := <-runner.received; got.Text != "context" {
		t.Fatal(got)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, _ := s.GetTask(task.ID)
		if current.Context[0].Delivery == "accepted" {
			if current.Prompt != "original" {
				t.Fatal(current.Prompt)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(current.Context)
		}
		time.Sleep(time.Millisecond)
	}
	close(runner.release)
	await(t, s, task.ID, Completed)
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := again.GetTask(task.ID)
	if current.Context[0].Delivery != "accepted" {
		t.Fatalf("lost acknowledgement: %+v", current.Context)
	}
}

type unackedRunner struct {
	started  chan struct{}
	received chan struct{}
}

func (r unackedRunner) Run(context.Context, Task, io.Writer) error { return nil }
func (r unackedRunner) RunLive(ctx context.Context, _ Task, _ io.Writer, follow <-chan Trigger, _ func(string, bool, string)) error {
	close(r.started)
	select {
	case <-follow:
		close(r.received)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestUnacknowledgedContextBecomesUnknown(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := unackedRunner{make(chan struct{}), make(chan struct{})}
	s.RegisterRunner("pi", r)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-r.started
	if err = s.AddContext(task.ID, "f", "context"); err != nil {
		t.Fatal(err)
	}
	<-r.received
	done := await(t, s, task.ID, Completed)
	if len(done.Context) != 1 || done.Context[0].Delivery != "unknown" {
		t.Fatalf("unexpected delivery: %+v", done.Context)
	}
}

func TestPiRPCDrainsAfterSettled(t *testing.T) {
	script := `read initial
printf '%s\n' '{"type":"response","id":"initial","success":true}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}' '{"type":"agent_settled"}'
i=0
while [ "$i" -lt 12000 ]; do printf '%s\n' '{"type":"diagnostic","padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}'; i=$((i + 1)); done
while read line; do :; done
`
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := (PiRunner{Command: fakePi(t, script)}).RunLive(ctx, Task{Prompt: "test", Workspace: t.TempDir()}, io.Discard, nil, func(string, bool, string) {})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPiRPCCancelBlockedInput(t *testing.T) {
	// A shell builtin does not read stdin, keeping the initial large RPC write blocked.
	script := `while :; do :; done`
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := (PiRunner{Command: fakePi(t, script)}).RunLive(ctx, Task{Prompt: strings.Repeat("x", 1024*1024), Workspace: t.TempDir()}, io.Discard, nil, func(string, bool, string) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestPiRPCPersistsToolDetails(t *testing.T) {
	script := `read initial
printf '%s\n' '{"type":"response","id":"initial","success":true,"data":{"disposition":"started"}}' '{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"go test ./..."}}' '{"type":"tool_execution_update","toolCallId":"c1","toolName":"bash","partialResult":{"content":[{"type":"text","text":"running tests"}]}}' '{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"PASS"}]},"durationMs":123,"isError":false}' '{"type":"tool_execution_start","toolCallId":"c2","toolName":"read","args":{"path":"README.md"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}' '{"type":"agent_settled"}'
while read line; do :; done
`
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("pi", PiRunner{Command: fakePi(t, script)})
	task, err := s.CreateTask("test", "work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	got := await(t, s, task.ID, Completed)
	if len(got.Activities) != 2 || got.Activities[0].Status != "finished" || got.Activities[1].Status != "interrupted" {
		t.Fatalf("unexpected activities %+v", got.Activities)
	}
	a := got.Activities[0]
	if a.AttemptID == "" || a.DurationMS == nil || *a.DurationMS != 123 || !strings.Contains(string(a.Result), "PASS") || strings.Contains(string(a.Result), "running tests") {
		t.Fatalf("lost tool details %+v", a)
	}
	if !strings.Contains(got.Output, "bash: go test ./...") || !strings.Contains(got.Output, "read: README.md") {
		t.Fatal(got.Output)
	}
}
