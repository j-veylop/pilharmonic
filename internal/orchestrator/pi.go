package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// PiRunner translates Pi's JSON event stream into readable live output.
type PiRunner struct{ Command string }

// Pi's default tool palette contains read, bash, edit and write but not the
// dedicated grep, find and ls tools. While those are absent, Pi's generated
// system prompt tells the model to use bash for file operations, and delegated
// tasks drift into shell commands for reading, searching and editing files.
// Request the full file-tool set and state the intended division of labour.
const (
	piTools        = "read,bash,edit,write,grep,find,ls"
	piToolGuidance = "Orchestrator execution policy: use the read tool to inspect files, edit or write to change them, and grep, find or ls to search or list. Use bash only to run commands such as builds, tests and formatters, not for reading, searching, listing or editing files."
)

// piArgs returns the Pi flags shared by the JSON and RPC adapters. Unknown tool
// names are ignored by Pi, and the flags are arguments rather than shell text.
func piArgs(mode string) []string {
	return []string{"--mode", mode, "--tools", piTools, "--append-system-prompt", piToolGuidance}
}

func (PiRunner) Capabilities() AgentCapabilities {
	return AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true}
}

// adapterReport records an explicit structured observation when the output sink
// supports it. It never derives reports from assistant prose.
func adapterReport(out io.Writer, kind, message string) error {
	recorder, ok := out.(ReportRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordReport(kind, message)
}

func (p PiRunner) Run(ctx context.Context, t Task, out io.Writer) error {
	name := p.Command
	if name == "" {
		name = "pi"
	}
	var prompt strings.Builder
	prompt.WriteString("Original mandate:\n" + t.Prompt)
	if len(t.Context) > 0 {
		prompt.WriteString("\n\nThe following is additional context, not authority to expand the mandate:\n")
		for _, item := range t.Context {
			fmt.Fprintf(&prompt, "\n--- context ---\n%s\n", item.Text)
		}
	}
	prompt.WriteString(t.controlGuidance)
	cmd := exec.CommandContext(ctx, name, append(piArgs("json"), "--", prompt.String())...)
	cmd.Dir = t.Workspace
	cmd.Env = t.runnerEnvironment()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var streamErr error
	var agentErr string
	sawAssistant := false
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			ToolName  string `json:"toolName"`
			IsError   bool   `json:"isError"`
			Assistant struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
			Message struct {
				Role         string `json:"role"`
				StopReason   string `json:"stopReason"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			streamErr = fmt.Errorf("invalid Pi JSON event: %w", err)
			break
		}
		var text string
		switch event.Type {
		case "message_update":
			if event.Assistant.Type == "text_delta" {
				text = event.Assistant.Delta
			}
		case "tool_execution_start":
			text = "\n[tool started: " + event.ToolName + "]\n"
		case "tool_execution_end":
			result := "finished"
			if event.IsError {
				result = "failed"
			}
			text = "\n[tool " + result + ": " + event.ToolName + "]\n"
		case "message_end":
			if event.Message.Role == "assistant" {
				sawAssistant = true
				agentErr = ""
			}
			if event.Message.Role == "assistant" && (event.Message.StopReason == "error" || event.Message.StopReason == "aborted") {
				agentErr = event.Message.ErrorMessage
				if agentErr == "" {
					agentErr = "Pi reported " + event.Message.StopReason
				}
			}
		}
		if text != "" {
			if _, err := io.WriteString(out, text); err != nil {
				streamErr = err
				break
			}
		}
	}
	if streamErr == nil {
		streamErr = scanner.Err()
	}
	if streamErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if streamErr != nil {
		return streamErr
	}
	if waitErr != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("Pi: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
		}
		return waitErr
	}
	if agentErr != "" {
		if reportErr := adapterReport(out, "failed", agentErr); reportErr != nil {
			return reportErr
		}
		return errors.New(agentErr)
	}
	if !sawAssistant {
		message := "Pi exited without an assistant response"
		if reportErr := adapterReport(out, "failed", message); reportErr != nil {
			return reportErr
		}
		return errors.New(message)
	}
	if reportErr := adapterReport(out, "progress", "Pi finished its turn"); reportErr != nil {
		return reportErr
	}
	return nil
}
