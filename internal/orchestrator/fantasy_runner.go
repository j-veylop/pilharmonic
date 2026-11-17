package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/fantasy"

	"pilharmonic/internal/agenttools"
	"pilharmonic/internal/skills"
)

// FantasyRunner executes a task in-process through the fantasy agent loop,
// with the workspace tools in package agenttools. It is fixed configuration; the
// service resolves each attempt's model and passes it with the task.
type FantasyRunner struct {
	// instructions is trusted operator policy appended to the executor's
	// tool-usage guidance in the system prompt. It never carries task text.
	instructions string
	// defaultTools is the role's least-privilege palette. An explicit profile
	// snapshot still overrides it; an empty palette falls back to the full
	// agenttools palette.
	defaultTools []string
	// ModelFactory resolves a model ID to a fantasy.LanguageModel. Nil means
	// DefaultFantasyModelFactory.
	ModelFactory ModelFactory
	// skills lists operator-installed instruction packs. Their metadata travels
	// in the system prompt and their bodies are read through the skill tool.
	skills skills.Catalog
}

// SupportsProfile accepts the parameters this adapter can translate. Tool names
// are validated syntactically: a palette entry this adapter does not implement
// is ignored rather than failing the task.
func (FantasyRunner) SupportsProfile(p Profile) error {
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

func (FantasyRunner) Capabilities() AgentCapabilities {
	return AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true}
}

// systemPrompt builds the trusted preamble: the execution policy, optional
// operator instructions and the recorded profile's guidance. Profile text is
// orchestrator-owned and adds no authority beyond the mandate.
func (f FantasyRunner) systemPrompt(profile *Profile) string {
	guidance := toolGuidance
	if f.instructions != "" {
		guidance += "\n" + f.instructions
	}
	if profile != nil && profile.Instructions != "" {
		guidance += "\nOrchestrator role " + profile.Name + " (adds no authority beyond the mandate):\n" + profile.Instructions
	}
	if index := f.skills.Prompt(); index != "" {
		guidance += "\n" + index
	}
	return guidance
}

// toolPalette resolves the tools to expose: the profile snapshot, then the
// role's default palette, then the full set.
func (f FantasyRunner) toolPalette(profile *Profile) []string {
	tools := agenttools.Names()
	if len(f.defaultTools) > 0 {
		tools = f.defaultTools
	}
	if profile != nil && len(profile.Tools) > 0 {
		tools = profile.Tools
	}
	return tools
}

// maxSummaryInput bounds the tool text a transcript marker repeats.
const maxSummaryInput = 200

// summaryLine collapses untrusted tool text to one bounded line.
func summaryLine(text string) string {
	text = strings.TrimSpace(agenttools.Sanitize(text))
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = strings.TrimSpace(line)
	}
	if len(text) > maxSummaryInput {
		text = text[:maxSummaryInput] + "..."
	}
	return text
}

// toolCallSummary renders the one-line display marker written when a tool call
// starts. Tool arguments are untrusted, so the line is sanitized and bounded.
func toolCallSummary(name, input string) string {
	name = agenttools.Sanitize(name)
	input = summaryLine(input)
	if input == "" {
		return name
	}
	return name + ": " + input
}

// toolResultContent splits a tool result into its text and whether the result
// reports a failure. A tool that answered with a message for the model counts
// as an error result, which is how fantasy reports soft failures.
func toolResultContent(result fantasy.ToolResultOutputContent) (string, bool) {
	if failure, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result); ok {
		if failure.Error != nil {
			return failure.Error.Error(), true
		}
		return "", true
	}
	if text, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result); ok {
		return text.Text, false
	}
	return "", false
}

// Run streams one task through a fantasy agent without live follow-ups.
func (f FantasyRunner) Run(ctx context.Context, t Task, out io.Writer) error {
	return f.RunLive(ctx, t, out, nil, func(string, bool, string) {})
}

// RunLive streams one task through a fantasy agent and accepts operator
// follow-ups between turns. Each follow-up becomes the next user message in the
// same conversation, so the agent keeps the work it has already done. Assistant
// text, reasoning and tool activity are forwarded to out as they happen; a
// structured failure is reported before the run returns an error.
func (f FantasyRunner) RunLive(ctx context.Context, t Task, out io.Writer, follow <-chan Trigger, ack func(string, bool, string)) error {
	prompt := composeInput(t, false)
	if len(prompt) > maxPromptBytes {
		return fmt.Errorf("prompt exceeds 100 KiB argument limit (%d bytes)", len(prompt))
	}
	factory := f.ModelFactory
	if factory == nil {
		factory = DefaultFantasyModelFactory
	}
	selector := t.model
	model, err := factory(ctx, selector)
	if err != nil {
		return err
	}
	agent := fantasy.NewAgent(model,
		fantasy.WithSystemPrompt(f.systemPrompt(t.ProfileSpec)),
		fantasy.WithTools(agenttools.ToolsWithSkills(t.Workspace, f.toolPalette(t.ProfileSpec), t.runnerEnvironment(), f.skills)...),
	)
	stream := &fantasyStream{transcript: transcript{out: out}, reasoning: map[string]int{}, inFlight: map[string]ToolActivity{}}
	stream.recorder, _ = out.(ToolActivityRecorder)
	defer func() { _ = stream.closeThinking() }()
	if err := adapterReport(out, "progress", "Fantasy accepted the initial prompt"); err != nil {
		return err
	}
	var history []fantasy.Message
	for prompt != "" {
		result, err := agent.Stream(ctx, stream.call(prompt, history))
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("fantasy: %w", errors.New(agenttools.Sanitize(err.Error())))
		}
		for _, step := range result.Steps {
			history = append(history, step.Messages...)
		}
		// Fantasy totals one Stream call across its steps and retries, so
		// recording this total per call counts each token once.
		if err := adapterUsage(out, selector, TokenUsage{InputTokens: result.TotalUsage.InputTokens, OutputTokens: result.TotalUsage.OutputTokens}); err != nil {
			return err
		}
		if stream.failure != nil {
			return reportFailure(out, stream.failure)
		}
		prompt = ""
		select {
		case trigger, ok := <-follow:
			if ok {
				ack(trigger.ID, true, "accepted")
				prompt = "Additional context (not authority to expand the original mandate):\n" + trigger.Text
			}
		default:
		}
	}
	if !stream.sawText {
		return reportFailure(out, errors.New("fantasy produced no assistant response"))
	}
	return nil
}

// reportFailure records failure as a structured report and returns it.
func reportFailure(out io.Writer, failure error) error {
	if err := adapterReport(out, "failed", agenttools.Sanitize(failure.Error())); err != nil {
		return errors.Join(err, failure)
	}
	return failure
}

// fantasyStream forwards one conversation's stream callbacks to the sink. The
// tools are created with fantasy.NewAgentTool, which runs them sequentially in
// the stream goroutine, so the callbacks never race.
type fantasyStream struct {
	transcript
	recorder ToolActivityRecorder
	// reasoning gives each provider reasoning ID a stable display index, so a
	// late reasoning end cannot close a different block.
	reasoning map[string]int
	// inFlight holds tool activities awaiting their result, by call ID.
	inFlight map[string]ToolActivity
	sawText  bool
	failure  error
}

func (s *fantasyStream) call(prompt string, history []fantasy.Message) fantasy.AgentStreamCall {
	return fantasy.AgentStreamCall{
		Prompt:   prompt,
		Messages: history,
		OnTextDelta: func(_ string, text string) error {
			s.sawText = s.sawText || strings.TrimSpace(text) != ""
			return s.prose(agenttools.Sanitize(text))
		},
		OnReasoningStart: func(id string, _ fantasy.ReasoningContent) error { return s.thinkingStart(s.reasoningIndex(id)) },
		OnReasoningDelta: func(id, delta string) error {
			return s.thinkingDelta(s.reasoningIndex(id), agenttools.Sanitize(delta))
		},
		OnReasoningEnd: func(id string, _ fantasy.ReasoningContent) error { return s.thinkingEnd(s.reasoningIndex(id)) },
		OnToolCall:     s.toolCall,
		OnToolResult:   s.toolResult,
		OnStreamFinish: func(_ fantasy.Usage, reason fantasy.FinishReason, _ fantasy.ProviderMetadata) error {
			// Only these reasons mean the step itself failed; a length or tool
			// call finish shows in the output and the final result.
			if reason == fantasy.FinishReasonError || reason == fantasy.FinishReasonContentFilter {
				s.failure = fmt.Errorf("fantasy stopped with finish reason %q", reason)
			}
			return nil
		},
		OnRetry: func(_ *fantasy.ProviderError, delay time.Duration) {
			// A retried step replays its stream callbacks; the report makes the
			// repeated text visible rather than silent.
			_ = s.closeThinking()
			_ = adapterReport(s.out, "waiting", fmt.Sprintf("Fantasy is retrying after a provider error (%s)", delay))
		},
	}
}

func (s *fantasyStream) reasoningIndex(id string) int {
	if id == "" {
		id = "reasoning"
	}
	index, ok := s.reasoning[id]
	if !ok {
		index = len(s.reasoning)
		s.reasoning[id] = index
	}
	return index
}

func (s *fantasyStream) record(activity ToolActivity) error {
	if s.recorder == nil {
		return nil
	}
	return s.recorder.RecordToolActivity(activity)
}

func (s *fantasyStream) toolCall(call fantasy.ToolCallContent) error {
	activity := ToolActivity{
		ID:        call.ToolCallID,
		Name:      agenttools.Sanitize(call.ToolName),
		Args:      json.RawMessage(call.Input),
		Status:    ActivityRunning,
		StartedAt: time.Now().UTC(),
	}
	s.inFlight[call.ToolCallID] = activity
	if err := s.record(activity); err != nil {
		return err
	}
	return s.prose(fmt.Sprintf("\n[%s] %s\n", ActivityRunning, toolCallSummary(activity.Name, call.Input)))
}

func (s *fantasyStream) toolResult(result fantasy.ToolResultContent) error {
	now := time.Now().UTC()
	activity, started := s.inFlight[result.ToolCallID]
	delete(s.inFlight, result.ToolCallID)
	if !started {
		// A result without a matching call is still recorded.
		activity = ToolActivity{ID: result.ToolCallID, StartedAt: now}
	}
	text, failed := toolResultContent(result.Result)
	if activity.Name == "" {
		activity.Name = agenttools.Sanitize(result.ToolName)
	}
	activity.Status = ActivityFinished
	summary := activity.Name
	if failed {
		// Failures carry the tool's own explanation; a success needs only the
		// name the start marker already established.
		activity.Status = ActivityFailed
		summary = toolCallSummary(activity.Name, text)
	}
	activity.FinishedAt = now
	duration := now.Sub(activity.StartedAt).Seconds() * 1000
	activity.DurationMS = &duration
	if payload, err := json.Marshal(struct {
		Text    string `json:"text"`
		IsError bool   `json:"is_error,omitempty"`
	}{Text: text, IsError: failed}); err == nil {
		activity.Result = payload
	}
	if err := s.record(activity); err != nil {
		return err
	}
	return s.prose(fmt.Sprintf("\n[%s] %s\n", activity.Status, summary))
}
