package orchestrator

import (
	"context"
	"strings"
	"time"

	"charm.land/fantasy"

	"pilharmonic/internal/agenttools"
)

const (
	// maxTitleRunes bounds a display title. Titles are a single line in the
	// board, detail header and subagent rows.
	maxTitleRunes = 60
	// maxTitleMandateRunes bounds the mandate sent to the title model. A title
	// needs the opening request, not a whole attachment.
	maxTitleMandateRunes = 4000
	// titleTimeout bounds the best-effort title call so shutdown and an
	// unresponsive provider are never held up by it.
	titleTimeout = 10 * time.Second
)

// TitleGenerator proposes a display title for a task mandate. The model
// selector is resolved by the service; the generator must not mutate state.
// A nil or failing generator leaves the deterministic derived title in place.
type TitleGenerator func(ctx context.Context, model, mandate string) (string, error)

// SetTitleGenerator replaces the model-backed title generator. Tests and
// embedders use it to avoid a provider call; passing nil restores the default.
func (s *Service) SetTitleGenerator(generator TitleGenerator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generator == nil {
		generator = s.generateTitle
	}
	s.titleGenerator = generator
}

// scheduleTitleLocked starts one best-effort title generation for an accepted
// task. It never blocks acceptance and preserves the derived title whenever
// the model is unconfigured, fails, or returns an unusable line. Caller holds
// mu.
func (s *Service) scheduleTitleLocked(id, mandate, agent string, spec *Profile) {
	if s.closing || s.titleGenerator == nil {
		return
	}
	model := s.titleModelLocked(agent, spec)
	if model == "" {
		return
	}
	generator := s.titleGenerator
	ctx := s.lifeCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		callCtx, cancel := context.WithTimeout(ctx, titleTimeout)
		defer cancel()
		title, err := generator(callCtx, model, mandate)
		if err != nil {
			return
		}
		s.applyGeneratedTitle(id, title)
	}()
}

// titleModelLocked prefers the operator's small class, which is intended for
// short, focused work, and otherwise uses the model the task itself would run.
// Caller holds mu.
func (s *Service) titleModelLocked(agent string, spec *Profile) string {
	if model := s.modelSelection[ModelClassSmall]; model != "" {
		return model
	}
	return s.fantasyModelLocked(agent, spec)
}

// applyGeneratedTitle records a generated title as display metadata. The
// mandate, decision and attempt history are untouched; a task deleted or a
// service closing before the call returns simply keeps the derived title.
func (s *Service) applyGeneratedTitle(id, raw string) {
	title := titleText(raw)
	if title == "" {
		return
	}
	_ = s.updateTask(id, func(t *Task) error {
		if t.Title == title {
			return nil
		}
		t.Title = title
		t.TitleGenerated = true
		t.UpdatedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{t.UpdatedAt, "titled", "Generated display title: " + title})
		return nil
	})
}

// titleText turns model output into one bounded display line. A model may wrap
// the title, quote it, prefix it with "Title:" or add a closing period; none of
// that belongs in the board column.
func titleText(raw string) string {
	raw = agenttools.Sanitize(raw)
	line := ""
	for _, candidate := range strings.Split(raw, "\n") {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			line = candidate
			break
		}
	}
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"task title:", "title:", "title -", "title —"} {
		if len(line) >= len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
			line = strings.TrimSpace(line[len(prefix):])
		}
	}
	line = strings.Trim(line, "\"'`*_ ")
	line = strings.Join(strings.Fields(line), " ")
	line = strings.TrimRight(line, ".")
	line = strings.TrimSpace(line)
	runes := []rune(line)
	if len(runes) > maxTitleRunes {
		line = strings.TrimSpace(string(runes[:maxTitleRunes-3])) + "..."
	}
	return line
}

// titleMandate bounds the text sent for titling without splitting a rune.
func titleMandate(mandate string) string {
	mandate = agenttools.Sanitize(mandate)
	runes := []rune(mandate)
	if len(runes) > maxTitleMandateRunes {
		return string(runes[:maxTitleMandateRunes])
	}
	return mandate
}

// titleInstruction is trusted orchestrator text: the mandate following it is
// untrusted data and cannot widen the instruction.
const titleInstruction = `You name tasks. Reply with only a short title for the task described by the mandate.
Use at most six words, in the mandate's language, with no quotes, labels, ending
punctuation or explanation. Name the concrete goal, not the process.`

// generateTitle asks the configured model for a concise title. Missing
// credentials, a provider error or an empty answer are ordinary failures: the
// caller keeps the derived title.
func (s *Service) generateTitle(ctx context.Context, model, mandate string) (string, error) {
	return generateTitleWith(ctx, s.fantasyModelFactory, model, mandate)
}

// generateTitleWith is the model-facing title call. The factory is injected so
// the prompt and response handling can be tested without a provider.
func generateTitleWith(ctx context.Context, factory ModelFactory, model, mandate string) (string, error) {
	languageModel, err := factory(ctx, model)
	if err != nil {
		return "", err
	}
	maxTokens := int64(40)
	temperature := 0.2
	response, err := languageModel.Generate(ctx, fantasy.Call{
		Prompt: fantasy.Prompt{
			{Role: fantasy.MessageRoleSystem, Content: []fantasy.MessagePart{fantasy.TextPart{Text: titleInstruction}}},
			{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: titleMandate(mandate)}}},
		},
		MaxOutputTokens: &maxTokens,
		Temperature:     &temperature,
	})
	if err != nil {
		return "", err
	}
	return titleText(response.Content.Text()), nil
}
