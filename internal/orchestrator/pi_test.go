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
