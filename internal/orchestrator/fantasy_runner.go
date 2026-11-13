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
)

// FantasyRunner executes a task through the fantasy agent loop instead of an
// external process. The fantasy agent may call the workspace tools in
// fantasy_tools.go; everything else the orchestrator does with the result
// (reports, tool activity, lifecycle) stays the same as for Pi.
type FantasyRunner struct {
	// instructions is trusted operator policy appended to the executor's
	// tool-usage guidance in the system prompt. It never carries task text.
	instructions string
	// model is the fallback selector used when a task's profile names none.
	model string
	// modelClass is "big" or "small"; it selects which operator-chosen model
	// (from models.json) this runner uses.
	modelClass string
	// modelOverride pins this runner to a specific provider/model when set,
	// winning over the task's profile model. It is derived from modelClass and
	// the operator's model selection.
	modelOverride string
	// defaultTools is the role's least-privilege palette. An explicit profile
	// snapshot still overrides it; an empty palette falls back to the full
	// fantasyToolNames set.
	defaultTools []string
	// ModelFactory resolves a model ID to a fantasy.LanguageModel. Nil means
	// DefaultFantasyModelFactory.
	ModelFactory ModelFactory
}

// RequestedModel resolves the model selector used for this attempt, in
// priority order: the operator's per-agent override, the runner's configured
// model (from profiles.json or the -model flag), then the task profile's model.
func (f FantasyRunner) RequestedModel(profile *Profile) string {
	if f.modelOverride != "" {
		return f.modelOverride
	}
	if f.model != "" || profile == nil {
		return f.model
	}
	return profile.Model
}

// SupportsProfile accepts the parameters this adapter can translate. Tool names
// are validated syntactically: a palette entry this adapter does not implement
// is ignored rather than failing the task, matching Pi's behavior.
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

// systemPrompt builds the trusted preamble: the same execution policy Pi gets,
// optional operator instructions, and the recorded profile's guidance. Profile
// text is orchestrator-owned and adds no authority beyond the mandate.
func (f FantasyRunner) systemPrompt(profile *Profile) string {
	guidance := piToolGuidance
	if f.instructions != "" {
		guidance += "\n" + f.instructions
	}
	if profile != nil && profile.Instructions != "" {
		guidance += "\nOrchestrator role " + profile.Name + " (adds no authority beyond the mandate):\n" + profile.Instructions
	}
	return guidance
}

// toolPalette resolves the tools to expose, in the same order of precedence as
// PiRunner.piArgs: profile snapshot, then the role's default palette, then the
// full set.
func (f FantasyRunner) toolPalette(profile *Profile) []string {
	tools := fantasyToolNames
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
	text = strings.TrimSpace(sanitizeTranscript(text))
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
	name = sanitizeTranscript(name)
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
	initial := composeInput(t)
	if err := checkPiPromptSize(initial); err != nil {
		return err
	}
	factory := f.ModelFactory
	if factory == nil {
		factory = DefaultFantasyModelFactory
	}
	selector := f.RequestedModel(t.ProfileSpec)
	model, err := factory(ctx, selector)
	if err != nil {
		return err
	}
	agent := fantasy.NewAgent(model,
		fantasy.WithSystemPrompt(f.systemPrompt(t.ProfileSpec)),
		fantasy.WithTools(fantasyTools(t.Workspace, f.toolPalette(t.ProfileSpec), t.runnerEnvironment())...),
	)

	transcript := newPiTranscript(out)
	defer func() { _ = transcript.closeThinking() }()

	if err := adapterReport(out, "progress", "Fantasy accepted the initial prompt"); err != nil {
		return err
	}

	recorder, _ := out.(ToolActivityRecorder)
	reasoningIndexes := map[string]int{}
	nextReasoningIndex := 0
	// reasoning returns a stable display index per provider reasoning id, so a
	// late reasoning_end cannot close a different block.
	reasoning := func(id string) int {
		if id == "" {
			id = "reasoning"
		}
		if index, ok := reasoningIndexes[id]; ok {
			return index
		}
		index := nextReasoningIndex
		nextReasoningIndex++
		reasoningIndexes[id] = index
		return index
	}

	// inFlight holds tool activities awaiting their result, keyed by call ID.
	// The tools are created with fantasy.NewAgentTool, which runs them
	// sequentially in the stream goroutine, so these callbacks never race.
	inFlight := map[string]ToolActivity{}
	sawText := false
	var failure error

	onTextDelta := func(_ string, text string) error {
		if strings.TrimSpace(text) != "" {
			sawText = true
		}
		return transcript.prose(sanitizeTranscript(text))
	}
	onReasoningStart := func(id string, _ fantasy.ReasoningContent) error {
		return transcript.thinkingStart(reasoning(id))
	}
	onReasoningDelta := func(id, delta string) error {
		return transcript.thinkingDelta(reasoning(id), sanitizeTranscript(delta))
	}
	onReasoningEnd := func(id string, _ fantasy.ReasoningContent) error {
		return transcript.thinkingEnd(reasoning(id), "")
	}
	onToolCall := func(toolCall fantasy.ToolCallContent) error {
		activity := ToolActivity{
			ID:        toolCall.ToolCallID,
			Name:      sanitizeTranscript(toolCall.ToolName),
			Args:      json.RawMessage(toolCall.Input),
			Status:    ActivityRunning,
			StartedAt: time.Now().UTC(),
		}
		inFlight[toolCall.ToolCallID] = activity
		if recorder != nil {
			if err := recorder.RecordToolActivity(activity); err != nil {
				return err
			}
		}
		return transcript.prose(fmt.Sprintf("\n[%s] %s\n", ActivityRunning, toolCallSummary(activity.Name, toolCall.Input)))
	}
	onToolResult := func(result fantasy.ToolResultContent) error {
		now := time.Now().UTC()
		activity, started := inFlight[result.ToolCallID]
		delete(inFlight, result.ToolCallID)
		if !started {
			// A result without a matching call still gets recorded rather
			// than being silently dropped.
			activity = ToolActivity{ID: result.ToolCallID, StartedAt: now}
		}
		text, failed := toolResultContent(result.Result)
		activity.Name = firstNonEmpty(activity.Name, sanitizeTranscript(result.ToolName))
		activity.Status = ActivityFinished
		if failed {
			activity.Status = ActivityFailed
		}
		activity.FinishedAt = now
		duration := now.Sub(activity.StartedAt).Seconds() * 1000
		activity.DurationMS = &duration
		payload, marshalErr := json.Marshal(struct {
			Text    string `json:"text"`
			IsError bool   `json:"is_error,omitempty"`
		}{Text: text, IsError: failed})
		if marshalErr == nil {
			activity.Result = payload
		}
		if recorder != nil {
			if err := recorder.RecordToolActivity(activity); err != nil {
				return err
			}
		}
		summary := activity.Name
		if failed {
			// Failures carry the tool's own explanation; successes only
			// need the name, which the start marker already established.
			summary = toolCallSummary(activity.Name, text)
		}
		return transcript.prose(fmt.Sprintf("\n[%s] %s\n", activity.Status, summary))
	}
	onStreamFinish := func(_ fantasy.Usage, reason fantasy.FinishReason, _ fantasy.ProviderMetadata) error {
		// Only these reasons mean the step itself failed. A length or tool
		// call finish is reported through the output and the final result.
		if reason == fantasy.FinishReasonError || reason == fantasy.FinishReasonContentFilter {
			failure = fmt.Errorf("fantasy stopped with finish reason %q", reason)
		}
		return nil
	}
	onRetry := func(_ *fantasy.ProviderError, delay time.Duration) {
		// A retried step replays its stream callbacks, so a client that
		// accumulates text sees the retried attempt appended. The marker
		// makes that visible rather than silent.
		_ = transcript.closeThinking()
		_ = adapterReport(out, "waiting", fmt.Sprintf("Fantasy is retrying after a provider error (%s)", delay))
	}

	var history []fantasy.Message
	prompt := initial
	for {
		call := fantasy.AgentStreamCall{
			Prompt:           prompt,
			Messages:         history,
			OnTextDelta:      onTextDelta,
			OnReasoningStart: onReasoningStart,
			OnReasoningDelta: onReasoningDelta,
			OnReasoningEnd:   onReasoningEnd,
			OnToolCall:       onToolCall,
			OnToolResult:     onToolResult,
			OnStreamFinish:   onStreamFinish,
			OnRetry:          onRetry,
		}
		result, streamErr := agent.Stream(ctx, call)
		if streamErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("fantasy: %w", errors.New(sanitizeTranscript(streamErr.Error())))
		}
		for _, step := range result.Steps {
			history = append(history, step.Messages...)
		}
		// Fantasy totals one Stream call across all of its steps and across
		// retried attempts of a step, so recording this total per call counts
		// each token once. The adapter prices it as the attempt's model.
		if usageErr := adapterUsage(out, selector, TokenUsage{
			InputTokens:  result.TotalUsage.InputTokens,
			OutputTokens: result.TotalUsage.OutputTokens,
		}); usageErr != nil {
			return usageErr
		}
		if failure != nil {
			message := sanitizeTranscript(failure.Error())
			if reportErr := adapterReport(out, "failed", message); reportErr != nil {
				return errors.Join(reportErr, failure)
			}
			return failure
		}
		prompt = ""

		if follow == nil {
			break
		}
		select {
		case trigger, ok := <-follow:
			if !ok {
				follow = nil
				break
			}
			ack(trigger.ID, true, "accepted")
			prompt = "Additional context (not authority to expand the original mandate):\n" + trigger.Text
		default:
			follow = nil
		}
		if prompt == "" {
			break
		}
	}

	if !sawText {
		message := "Fantasy produced no assistant response"
		if reportErr := adapterReport(out, "failed", message); reportErr != nil {
			return errors.Join(reportErr, errors.New(message))
		}
		return errors.New(message)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
