package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakePi(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestPiStream(t *testing.T) {
	path := fakePi(t, `printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Working"}}' '{"type":"tool_execution_start","toolName":"read"}' '{"type":"tool_execution_end","toolName":"read","isError":false}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}'`)
	var output bytes.Buffer
	if err := (PiRunner{Command: path}).Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Working") || !strings.Contains(output.String(), "tool started: read") {
		t.Fatal(output.String())
	}
}
func TestPiStreamCapturesThinkingBlocks(t *testing.T) {
	path := fakePi(t, `printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_start","contentIndex":0}}' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"private "}}' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"reasoning"}}' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_end","contentIndex":0,"content":"private reasoning"}}' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"answer"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}'`)
	var output bytes.Buffer
	if err := (PiRunner{Command: path}).Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, thinkingStartMarker) || !strings.Contains(got, thinkingEndMarker) {
		t.Fatalf("missing thinking markers: %q", got)
	}
	if strings.Count(got, "private reasoning") != 1 || !strings.Contains(got, "answer") {
		t.Fatalf("thinking or answer duplicated/lost: %q", got)
	}
}

func TestPiStreamClosesThinkingBeforeDelayedText(t *testing.T) {
	path := fakePi(t, `printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_start","contentIndex":0}}' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"private reasoning"}}' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"answer"}}' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_end","contentIndex":0,"content":"private reasoning"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}'`)
	var output bytes.Buffer
	if err := (PiRunner{Command: path}).Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	closeAt := strings.LastIndex(got, thinkingEndMarker)
	answerAt := strings.LastIndex(got, "answer")
	if closeAt < 0 || answerAt < 0 || closeAt > answerAt {
		t.Fatalf("text was left inside thinking: %q", got)
	}
	if strings.Count(got, thinkingStartMarker) != 1 || strings.Count(got, thinkingEndMarker) != 1 || strings.Count(got, "private reasoning") != 1 {
		t.Fatalf("unexpected thinking markers: %q", got)
	}
}

func TestPiStreamUsesEndContentWhenThinkingHasNoDeltas(t *testing.T) {
	path := fakePi(t, `printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_start","contentIndex":0}}' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_end","contentIndex":0,"content":"[Reasoning redacted]"}}' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"answer"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}'`)
	var output bytes.Buffer
	if err := (PiRunner{Command: path}).Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Reasoning redacted") {
		t.Fatalf("thinking content lost: %q", output.String())
	}
}

func TestPiRunnersRequestDedicatedFileTools(t *testing.T) {
	check := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		args := string(b)
		for _, want := range []string{"--mode", "--tools", piTools, "--append-system-prompt", "Orchestrator execution policy"} {
			if !strings.Contains(args, want) {
				t.Fatalf("missing %q in args:\n%s", want, args)
			}
		}
		return args
	}
	t.Run("json", func(t *testing.T) {
		argsPath := filepath.Join(t.TempDir(), "args")
		script := "printf '%s\\n' \"$@\" > '" + argsPath + "'\n" + `printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"ok"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}'` + "\n"
		var output bytes.Buffer
		if err := (PiRunner{Command: fakePi(t, script)}).Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(check(t, argsPath), "Original mandate") {
			t.Fatal("JSON mode did not pass the prompt as an argument")
		}
	})
	t.Run("rpc", func(t *testing.T) {
		argsPath := filepath.Join(t.TempDir(), "args")
		script := "printf '%s\\n' \"$@\" > '" + argsPath + "'\n" + `read initial
printf '%s\n' '{"type":"response","id":"initial","success":true,"data":{"disposition":"started"}}' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"ok"}}' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}' '{"type":"agent_settled"}'
while read line; do :; done
`
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var output bytes.Buffer
		follow := make(chan Trigger)
		err := (PiRunner{Command: fakePi(t, script)}).RunLive(ctx, Task{Prompt: "do work", Workspace: t.TempDir()}, &output, follow, func(string, bool, string) {})
		if err != nil {
			t.Fatal(err)
		}
		check(t, argsPath)
	})
}

func TestPiProtocolFailures(t *testing.T) {
	for _, test := range []struct{ name, script, want string }{
		{"bad json", `printf '%s\n' 'bad'`, "invalid Pi JSON"},
		{"agent error", `printf '%s\n' '{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"provider failed"}}'`, "provider failed"},
		{"no response", `printf '%s\n' '{"type":"session"}'`, "without an assistant response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (PiRunner{Command: fakePi(t, test.script)}).Run(context.Background(), Task{Prompt: "x", Workspace: t.TempDir()}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
