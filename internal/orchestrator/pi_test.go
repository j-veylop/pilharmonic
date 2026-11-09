package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
