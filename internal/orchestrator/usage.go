package orchestrator

import (
	"errors"
	"io"
	"sort"
	"strings"
	"time"
)

// TokenUsage is a token count reported by a model provider. Only the input and
// output counters are recorded: they are the two the orchestrator prices, and
// provider-specific counters (reasoning, cache reads) are not inferred from
// them.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// IsZero reports whether the provider reported no tokens at all.
func (u TokenUsage) IsZero() bool { return u.InputTokens == 0 && u.OutputTokens == 0 }

// AttemptUsage is the token usage recorded for one attempt, together with the
// cost estimated when the provider reported it. The cost is fixed at recording
// time because a provider with time-of-day pricing charges the rate in force
// then, not the rate in force when the operator reads the total. Rate is
// RatePeak, RateOffPeak or RateMixed for a model priced the same all day, and
// empty otherwise.
type AttemptUsage struct {
	// Model is the selector the adapter reported usage for, so a later reader
	// can price the tokens without trusting the attempt's own selector.
	Model        string    `json:"model,omitempty"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	Priced       bool      `json:"priced,omitempty"`
	Rate         string    `json:"rate,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// modelSelector is the selector this usage belongs to, falling back to the
// attempt's requested model for records written before the selector was kept.
func (u AttemptUsage) modelSelector(requested string) string {
	if u.Model != "" {
		return u.Model
	}
	return requested
}

// Usage is the attempt's token count without its cost.
func (u AttemptUsage) Usage() TokenUsage {
	return TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
}

// addUsage folds one priced token count into the attempt total. A rate change
// mid-attempt is recorded as mixed so the durable record stays truthful about
// which prices were applied.
func (u *AttemptUsage) addUsage(usage TokenUsage, rate string) {
	u.InputTokens += usage.InputTokens
	u.OutputTokens += usage.OutputTokens
	switch {
	case u.Rate == "":
		u.Rate = rate
	case rate == "" || u.Rate == rate:
		// Nothing to change: an all-day price or the same rate again.
	case u.Rate == RateMixed:
	default:
		u.Rate = RateMixed
	}
}

// UsageRecorder is an optional capability of a runner's output sink. It lets an
// adapter report the tokens a provider reported for the running attempt,
// attributed to the model selector it used. Recording is bound to the attempt,
// so a late report after a retry cannot charge the new attempt.
type UsageRecorder interface {
	RecordUsage(model string, usage TokenUsage) error
}

// RecordUsage prices one model call and adds it to the running attempt. The
// price is looked up at recording time, so an off-peak DeepSeek call is charged
// the discounted rate even when the operator reads the total later.
func (w outputWriter) RecordUsage(model string, usage TokenUsage) error {
	if usage.IsZero() {
		return nil
	}
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.s.closing {
		return errors.New("service is shutting down")
	}
	t, err := w.taskLocked()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if strings.TrimSpace(model) == "" {
		model = t.currentAttemptModel()
	}
	price, priced := PriceFor(model, now)
	rate := ""
	if priced {
		rate = price.Rate(now)
	}
	return w.s.mutateTaskLocked(t, func(t *Task) error {
		if len(t.Attempts) == 0 {
			return errors.New("execution attempt is no longer running")
		}
		attempt := &t.Attempts[len(t.Attempts)-1]
		if attempt.Usage == nil {
			attempt.Usage = &AttemptUsage{Model: model, Priced: priced}
		}
		if attempt.Usage.Model == "" {
			attempt.Usage.Model = model
		}
		attempt.Usage.addUsage(usage, rate)
		attempt.Usage.Priced = attempt.Usage.Priced && priced
		if priced {
			attempt.Usage.CostUSD += price.Cost(usage, now)
		}
		attempt.Usage.UpdatedAt = now
		t.UpdatedAt = now
		return nil
	})
}

// adapterUsage reports token usage to a sink that supports it. It never derives
// usage from prose, and a sink without the capability is simply not charged.
func adapterUsage(out io.Writer, model string, usage TokenUsage) error {
	recorder, ok := out.(UsageRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordUsage(model, usage)
}

// TaskUsage is the token usage a task's attempts recorded in total.
type TaskUsage struct {
	TokenUsage
	CostUSD float64
	// Priced reports whether every counted token had a known price, so a
	// displayed cost never silently omits part of the work.
	Priced bool
	// Model is the selector of the newest attempt that recorded usage.
	Model string
	// Rate is the rate the newest recording applied, for models with
	// time-of-day pricing.
	Rate string
}

// Usage totals the recorded usage of every attempt, including attempts from
// earlier sessions: this is the task's own history, not a session total.
func (t Task) Usage() TaskUsage {
	var total TaskUsage
	total.Priced = true
	counted := false
	for _, attempt := range t.Attempts {
		if attempt.Usage == nil || attempt.Usage.Usage().IsZero() {
			continue
		}
		total.InputTokens += attempt.Usage.InputTokens
		total.OutputTokens += attempt.Usage.OutputTokens
		total.CostUSD += attempt.Usage.CostUSD
		total.Priced = total.Priced && attempt.Usage.Priced
		total.Model = attempt.Usage.modelSelector(attempt.RequestedModel)
		total.Rate = attempt.Usage.Rate
		counted = true
	}
	if !counted {
		total.Priced = false
	}
	return total
}

// currentAttemptModel is the selector recorded for the running attempt.
func (t *Task) currentAttemptModel() string {
	if len(t.Attempts) == 0 {
		return ""
	}
	return t.Attempts[len(t.Attempts)-1].RequestedModel
}

// ModelUsage is the usage one model selector contributed to a session, with the
// price in force at the time the total was read.
type ModelUsage struct {
	Model        string     `json:"model"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	Priced       bool       `json:"priced"`
	CurrentRate  string     `json:"current_rate,omitempty"`
	Price        ModelPrice `json:"price"`
	PriceKnown   bool       `json:"price_known"`
}

// SessionUsage is the token usage and estimated cost of the running
// orchestrator process, summed over every task it ran, including delegated
// children. Restarting the process starts a new session; earlier attempts keep
// their own recorded usage in durable history but are not counted here.
type SessionUsage struct {
	StartedAt            time.Time    `json:"started_at"`
	InputTokens          int64        `json:"input_tokens"`
	OutputTokens         int64        `json:"output_tokens"`
	TotalTokens          int64        `json:"total_tokens"`
	CostUSD              float64      `json:"cost_usd"`
	Priced               bool         `json:"priced"`
	SubagentInputTokens  int64        `json:"subagent_input_tokens"`
	SubagentOutputTokens int64        `json:"subagent_output_tokens"`
	SubagentCostUSD      float64      `json:"subagent_cost_usd"`
	Tasks                int          `json:"tasks"`
	SubagentTasks        int          `json:"subagent_tasks"`
	Unpriced             []string     `json:"unpriced,omitempty"`
	Models               []ModelUsage `json:"models,omitempty"`
}

// SessionUsage returns this process's token usage and estimated cost.
func (s *Service) SessionUsage() SessionUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionUsageLocked()
}

// sessionUsageLocked sums the usage recorded by attempts this process started.
// An attempt that began before the session cannot have recorded live usage here
// because the service marks it interrupted on open, so attempt start time is the
// session boundary. The caller holds s.mu.
func (s *Service) sessionUsageLocked() SessionUsage {
	now := time.Now().UTC()
	usage := SessionUsage{StartedAt: s.sessionStartedAt, Priced: true}
	byModel := map[string]*ModelUsage{}
	unpriced := map[string]bool{}
	for _, task := range s.data.Tasks {
		counted := false
		for _, attempt := range task.Attempts {
			if attempt.Usage == nil || attempt.Usage.Usage().IsZero() {
				continue
			}
			if attempt.StartedAt.Before(s.sessionStartedAt) {
				continue
			}
			counted = true
			model := attempt.Usage.modelSelector(attempt.RequestedModel)
			entry := byModel[model]
			if entry == nil {
				price, priced := PriceFor(model, now)
				entry = &ModelUsage{Model: model, Priced: true, Price: price, PriceKnown: priced}
				if priced {
					entry.CurrentRate = price.Rate(now)
				}
				byModel[model] = entry
			}
			entry.InputTokens += attempt.Usage.InputTokens
			entry.OutputTokens += attempt.Usage.OutputTokens
			entry.CostUSD += attempt.Usage.CostUSD
			entry.Priced = entry.Priced && attempt.Usage.Priced
			usage.InputTokens += attempt.Usage.InputTokens
			usage.OutputTokens += attempt.Usage.OutputTokens
			usage.CostUSD += attempt.Usage.CostUSD
			usage.Priced = usage.Priced && attempt.Usage.Priced
			if !attempt.Usage.Priced {
				unpriced[model] = true
			}
			if task.ParentID != "" {
				usage.SubagentInputTokens += attempt.Usage.InputTokens
				usage.SubagentOutputTokens += attempt.Usage.OutputTokens
				usage.SubagentCostUSD += attempt.Usage.CostUSD
			}
		}
		if counted {
			usage.Tasks++
			if task.ParentID != "" {
				usage.SubagentTasks++
			}
		}
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	usage.Models = make([]ModelUsage, 0, len(byModel))
	for _, entry := range byModel {
		usage.Models = append(usage.Models, *entry)
	}
	sort.Slice(usage.Models, func(i, j int) bool {
		if usage.Models[i].CostUSD != usage.Models[j].CostUSD {
			return usage.Models[i].CostUSD > usage.Models[j].CostUSD
		}
		return usage.Models[i].Model < usage.Models[j].Model
	})
	for model := range unpriced {
		usage.Unpriced = append(usage.Unpriced, model)
	}
	sort.Strings(usage.Unpriced)
	return usage
}
