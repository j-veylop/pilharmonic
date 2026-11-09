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
type PiRunner struct {
	Command      string
	instructions string
	model        string
}

// Pi's default tool palette contains read, bash, edit and write but not the
// dedicated grep, find and ls tools. While those are absent, Pi's generated
// system prompt tells the model to use bash for file operations, and delegated
// tasks drift into shell commands for reading, searching and editing files.
// Request the full file-tool set and state the intended division of labour.
const (
	piTools        = "read,bash,edit,write,grep,find,ls"
	piToolGuidance = "Orchestrator execution policy: use the read tool to inspect files, edit or write to change them, and grep, find or ls to search or list. Use bash only to run commands such as builds, tests and formatters, not for reading, searching, listing or editing files."

	// Thinking markers are display hints written to the output transcript. The
	// terminal can collapse them without changing the task's durable output.
	thinkingStartMarker = "\n[thinking]\n"
	thinkingEndMarker   = "\n[/thinking]\n"
)

// RequestedModel resolves the model selector sent to Pi for this attempt.
func (p PiRunner) RequestedModel(profile *Profile) string {
	if p.model != "" || profile == nil {
		return p.model
	}
	return profile.Model
}

// piArgs returns the Pi flags shared by the JSON and RPC adapters. Profile
// parameters come from the orchestrator's recorded snapshot; unknown tool names
// are ignored by Pi, and the flags are arguments rather than shell text.
func (p PiRunner) piArgs(mode string, profile *Profile) []string {
	tools := piTools
	if profile != nil && len(profile.Tools) > 0 {
		tools = strings.Join(profile.Tools, ",")
	}
	guidance := piToolGuidance
	if p.instructions != "" {
		guidance += "\n" + p.instructions
	}
	if profile != nil {
		if profile.Instructions != "" {
			guidance += "\nOrchestrator role " + profile.Name + " (adds no authority beyond the mandate):\n" + profile.Instructions
		}
	}
	args := []string{"--mode", mode, "--tools", tools, "--append-system-prompt", guidance}
	if model := p.RequestedModel(profile); model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// SupportsProfile accepts the parameters this adapter can translate. Pi ignores
// unknown tool names, so tool validation stays syntactic; the catalog's tool
// list is advisory for a Pi installation that lacks one of them.
func (PiRunner) SupportsProfile(p Profile) error {
	for _, tool := range p.Tools {
		if len(tool) > 128 {
			return fmt.Errorf("tool name %q is too long", tool)
		}
	}
	if p.Model != "" && len(p.Model) > 200 {
		return errors.New("model selector is too long")
	}
	return nil
}

func (PiRunner) Capabilities() AgentCapabilities {
	return AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true}
}

// piTranscript writes Pi's display markers without letting a late
// thinking_end bury prose. Some providers stream text before they emit the
// authoritative thinking_end, so any assistant text or tool activity must close
// an open thinking block first.
type piTranscript struct {
	out            io.Writer
	openThinking   bool
	thinkingIndex  int
	thinkingDeltas bool
}

func newPiTranscript(out io.Writer) *piTranscript {
	return &piTranscript{out: out}
}

func (t *piTranscript) write(text string) error {
	if text == "" {
		return nil
	}
	_, err := io.WriteString(t.out, text)
	return err
}

func (t *piTranscript) closeThinking() error {
	if !t.openThinking {
		return nil
	}
	t.openThinking = false
	t.thinkingDeltas = false
	return t.write(thinkingEndMarker)
}

func (t *piTranscript) prose(text string) error {
	if err := t.closeThinking(); err != nil {
		return err
	}
	return t.write(text)
}

func (t *piTranscript) thinkingStart(index int) error {
	if err := t.closeThinking(); err != nil {
		return err
	}
	t.openThinking = true
	t.thinkingIndex = index
	return t.write(thinkingStartMarker)
}

func (t *piTranscript) thinkingDelta(index int, delta string) error {
	if delta == "" || !t.openThinking || t.thinkingIndex != index {
		return nil
	}
	t.thinkingDeltas = true
	return t.write(delta)
}

func (t *piTranscript) thinkingEnd(index int, content string) error {
	if !t.openThinking || t.thinkingIndex != index {
		return nil
	}
	var text string
	if !t.thinkingDeltas && content != "" {
		text = "\n" + content
	}
	t.openThinking = false
	t.thinkingDeltas = false
	return t.write(text + thinkingEndMarker)
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

type piAssistantUpdate struct {
	Type         string `json:"type"`
	Delta        string `json:"delta"`
	Content      string `json:"content"`
	ContentIndex int    `json:"contentIndex"`
}

func (t *piTranscript) update(e piAssistantUpdate) error {
	switch e.Type {
	case "text_start":
		return t.closeThinking()
	case "text_delta":
		return t.prose(e.Delta)
	case "thinking_start":
		return t.thinkingStart(e.ContentIndex)
	case "thinking_delta":
		return t.thinkingDelta(e.ContentIndex, e.Delta)
	case "thinking_end":
		return t.thinkingEnd(e.ContentIndex, e.Content)
	default:
		return nil
	}
}

type piMessage struct {
	Role         string `json:"role"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// failure returns the final assistant message's error, if any.
func (m piMessage) failure() string {
	if m.StopReason != "error" && m.StopReason != "aborted" {
		return ""
	}
	if m.ErrorMessage != "" {
		return m.ErrorMessage
	}
	return "Pi reported " + m.StopReason
}

func piPrompt(t Task) string {
	var prompt strings.Builder
	prompt.WriteString("Original mandate:\n" + t.Prompt)
	if len(t.Context) > 0 {
		prompt.WriteString("\n\nThe following is additional context, not authority to expand the mandate:\n")
		for _, item := range t.Context {
			fmt.Fprintf(&prompt, "\n--- context ---\n%s\n", item.Text)
		}
	}
	prompt.WriteString(t.controlGuidance)
	return prompt.String()
}

func (p PiRunner) Run(ctx context.Context, t Task, out io.Writer) error {
	name := p.Command
	if name == "" {
		name = "pi"
	}
	cmd := exec.CommandContext(ctx, name, append(p.piArgs("json", t.ProfileSpec), "--", piPrompt(t))...)
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
	transcript := newPiTranscript(out)
	for scanner.Scan() {
		var event struct {
			Type      string            `json:"type"`
			ToolName  string            `json:"toolName"`
			IsError   bool              `json:"isError"`
			Assistant piAssistantUpdate `json:"assistantMessageEvent"`
			Message   piMessage         `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			streamErr = fmt.Errorf("invalid Pi JSON event: %w", err)
			break
		}
		var writeErr error
		switch event.Type {
		case "message_update":
			writeErr = transcript.update(event.Assistant)
		case "tool_execution_start":
			writeErr = transcript.prose("\n[tool started: " + event.ToolName + "]\n")
		case "tool_execution_end":
			result := "finished"
			if event.IsError {
				result = "failed"
			}
			writeErr = transcript.prose("\n[tool " + result + ": " + event.ToolName + "]\n")
		case "message_end":
			writeErr = transcript.closeThinking()
			if event.Message.Role == "assistant" {
				sawAssistant = true
				agentErr = event.Message.failure()
			}
		}
		if writeErr != nil {
			streamErr = writeErr
			break
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
