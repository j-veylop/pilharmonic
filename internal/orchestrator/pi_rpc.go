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
	"time"
)

type rpcEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		Disposition string `json:"disposition"`
	} `json:"data"`
	ToolCallID    string          `json:"toolCallId"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	PartialResult json.RawMessage `json:"partialResult"`
	DurationMS    *float64        `json:"durationMs"`
	ToolName      string          `json:"toolName"`
	IsError       bool            `json:"isError"`
	Assistant     struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	Message struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"message"`
	Aborted bool `json:"aborted"`
}
type rpcRead struct {
	event rpcEvent
	err   error
}

// RunLive uses Pi's RPC mode. Follow-up acknowledgement indicates only that
// Pi accepted or handled the message, not that the model consumed it.
func (p PiRunner) RunLive(ctx context.Context, t Task, out io.Writer, follow <-chan Trigger, ack func(string, bool, string)) error {
	name := p.Command
	if name == "" {
		name = "pi"
	}
	cmd := exec.CommandContext(ctx, name, "--mode", "rpc", "--no-session")
	cmd.Dir = t.Workspace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	// Kill on every abnormal return, so a parser or output failure cannot leave Pi behind.
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	type command struct{ id, kind, message string }
	commands := make(chan command, 65)
	writeErrors := make(chan error, 1)
	go func() {
		enc := json.NewEncoder(stdin)
		for cmd := range commands {
			if err := enc.Encode(map[string]string{"id": cmd.id, "type": cmd.kind, "message": cmd.message}); err != nil {
				writeErrors <- err
				return
			}
		}
	}()
	defer close(commands)

	var prompt strings.Builder
	prompt.WriteString("Original mandate:\n" + t.Prompt)
	if len(t.Context) > 0 {
		prompt.WriteString("\n\nThe following is additional context, not authority to expand the mandate:\n")
		for _, item := range t.Context {
			fmt.Fprintf(&prompt, "\n--- context ---\n%s\n", item.Text)
		}
	}
	commands <- command{"initial", "prompt", prompt.String()}
	events := make(chan rpcRead)
	done := make(chan struct{})
	defer close(done)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			var event rpcEvent
			err := json.Unmarshal(scanner.Bytes(), &event)
			if err != nil {
				err = fmt.Errorf("invalid Pi RPC event: %w", err)
			}
			select {
			case events <- rpcRead{event, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case events <- rpcRead{err: err}:
		case <-done:
		}
	}()
	pending := map[string]bool{}
	toolInputs := map[string]ToolActivity{}
	initialOK, settled, sawAssistant := false, false, false
	agentErr := ""
	for {
		if settled && initialOK && len(pending) == 0 {
			_ = stdin.Close()
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
		drain:
			for {
				select {
				case err = <-waited:
					break drain
				case <-events: // Keep stdout drained until the subprocess exits.
				case <-ctx.Done():
					_ = cmd.Process.Kill()
					<-waited
					finished = true
					return ctx.Err()
				case <-timer.C:
					_ = cmd.Process.Kill()
					<-waited
					finished = true
					return errors.New("Pi did not exit after RPC shutdown")
				}
			}
			finished = true
			if err != nil {
				if stderr.Len() > 0 {
					return fmt.Errorf("Pi: %w: %s", err, strings.TrimSpace(stderr.String()))
				}
				return err
			}
			if agentErr != "" {
				return errors.New(agentErr)
			}
			if !sawAssistant {
				return errors.New("Pi settled without an assistant response")
			}
			return nil
		}
		activeFollow := follow
		if !initialOK || settled || len(pending) >= 64 {
			activeFollow = nil
		}
		select {
		case err := <-writeErrors:
			return fmt.Errorf("Pi RPC write: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case trigger, ok := <-activeFollow:
			if !ok {
				follow = nil
				continue
			}
			if settled {
				ack(trigger.ID, false, "Pi already settled")
				continue
			}
			message := "Additional context (not authority to expand the original mandate):\n" + trigger.Text
			commands <- command{trigger.ID, "follow_up", message}
			pending[trigger.ID] = true
		case item := <-events:
			if item.err != nil {
				return fmt.Errorf("Pi RPC stream ended before settlement: %w", item.err)
			}
			e := item.event
			switch e.Type {
			case "response":
				if e.ID == "initial" {
					if !e.Success {
						return fmt.Errorf("Pi rejected prompt: %s", e.Error)
					}
					initialOK = true
					if e.Data.Disposition == "handled" {
						settled = true
						sawAssistant = true
					}
				} else if pending[e.ID] {
					delete(pending, e.ID)
					reason := e.Error
					if e.Success {
						reason = e.Data.Disposition
						if reason == "" {
							reason = "accepted"
						}
					}
					ack(e.ID, e.Success, reason)
				}
			case "message_update":
				if e.Assistant.Type == "text_delta" {
					if _, err = io.WriteString(out, e.Assistant.Delta); err != nil {
						return err
					}
				}
			case "tool_execution_start", "tool_execution_update", "tool_execution_end":
				activity := ToolActivity{ID: e.ToolCallID, Name: e.ToolName, Args: e.Args, Result: e.Result, DurationMS: e.DurationMS}
				if activity.ID == "" {
					activity.ID = e.ToolName
				}
				if e.Type == "tool_execution_start" {
					toolInputs[activity.ID] = activity
				} else if previous, ok := toolInputs[activity.ID]; ok {
					if len(activity.Args) == 0 {
						activity.Args = previous.Args
					}
					if activity.Name == "" {
						activity.Name = previous.Name
					}
				}
				if e.Type == "tool_execution_end" {
					delete(toolInputs, activity.ID)
				}

				switch e.Type {
				case "tool_execution_start":
					activity.Status = "running"
				case "tool_execution_update":
					activity.Result = e.PartialResult
				case "tool_execution_end":
					activity.Status = "finished"
					if e.IsError {
						activity.Status = "failed"
					}
				}
				if recorder, ok := out.(ToolActivityRecorder); ok {
					if err = recorder.RecordToolActivity(activity); err != nil {
						return err
					}
				}
				if e.Type != "tool_execution_update" {
					if _, err = fmt.Fprintf(out, "\n[%s] %s\n", activity.Status, activity.Summary()); err != nil {
						return err
					}
				}

			case "message_end":
				if e.Message.Role == "assistant" {
					sawAssistant = true
					agentErr = ""
					if e.Message.StopReason == "error" || e.Message.StopReason == "aborted" {
						agentErr = e.Message.ErrorMessage
						if agentErr == "" {
							agentErr = "Pi reported " + e.Message.StopReason
						}
					}
				}
			case "agent_settled":
				settled = true
				if e.Aborted && agentErr == "" {
					agentErr = "Pi reported an aborted run"
				}
			}
		}
	}
}
