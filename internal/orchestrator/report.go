package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Report is an explicit observation, separate from process execution status.
// Reports do not grant authority or change the original mandate.
type Report struct {
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Truncated bool      `json:"truncated,omitempty"`
	Source    string    `json:"source,omitempty"`
	AttemptID string    `json:"attempt_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// maxReportMessage bounds one observation so a verbose adapter cannot bloat the
// durable state file.
const maxReportMessage = 4 << 10

// normalizeReport bounds the vocabulary and size of an observation so every
// observation has a known meaning. It reports whether the message was
// truncated to the size bound.
func normalizeReport(kind, message string) (string, string, bool, error) {
	switch kind {
	case "progress", "waiting", "blocked", "ready", "failed":
	default:
		return "", "", false, fmt.Errorf("unknown report kind %q", kind)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", "", false, errors.New("report message is required")
	}
	message, truncated := truncateReportMessage(message)
	return kind, message, truncated, nil
}

// truncateReportMessage keeps the beginning of an over-long message, which is
// where the observation usually is, and marks the cut.
func truncateReportMessage(message string) (string, bool) {
	if len(message) <= maxReportMessage {
		return message, false
	}
	cut := maxReportMessage
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return strings.TrimSpace(message[:cut]), true
}

// RecordReport records a local operator observation. Agent prose is never
// interpreted as a report automatically.
func (s *Service) RecordReport(taskID, kind, message string) error {
	kind, message, truncated, err := normalizeReport(kind, message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != taskID {
			continue
		}
		if t.Status == Canceled {
			return errors.New("cannot report on canceled task")
		}
		return s.appendReportLocked(t, kind, message, "operator", "", truncated)
	}
	return errors.New("task not found")
}

// appendReportLocked persists one report and its history event. The caller
// holds s.mu.
func (s *Service) appendReportLocked(t *Task, kind, message, source, attemptID string, truncated bool) error {
	return s.mutateTaskLocked(t, func(t *Task) error {
		now := time.Now().UTC()
		t.Reports = append(t.Reports, Report{Kind: kind, Message: message, Truncated: truncated, Source: source, AttemptID: attemptID, CreatedAt: now})
		t.UpdatedAt = now
		eventMessage := message
		if source != "" && source != "operator" {
			eventMessage = source + ": " + message
		}
		t.Events = append(t.Events, Event{now, "report_" + kind, eventMessage})
		return nil
	})
}

// ReportRecorder is an optional capability of a runner's output sink. It lets an
// adapter record an explicit structured observation without parsing prose.
type ReportRecorder interface {
	RecordReport(kind, message string) error
}

// RecordReport records an adapter observation for the task's running attempt.
func (w outputWriter) RecordReport(kind, message string) error {
	kind, message, truncated, err := normalizeReport(kind, message)
	if err != nil {
		return err
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
	return w.s.appendReportLocked(t, kind, message, "adapter", w.attemptID, truncated)
}

// CurrentReport excludes observations made before the current attempt began.
func (t Task) CurrentReport() (Report, bool) {
	if len(t.Reports) == 0 {
		return Report{}, false
	}
	r := t.Reports[len(t.Reports)-1]
	if r.AttemptID != "" && r.AttemptID != t.currentAttemptID() {
		return Report{}, false
	}
	for _, event := range t.Events {
		if event.Kind == "requeued" && event.Time.After(r.CreatedAt) {
			return Report{}, false
		}
	}
	if len(t.Attempts) > 0 && r.CreatedAt.Before(t.Attempts[len(t.Attempts)-1].StartedAt) {
		return Report{}, false
	}
	return r, true
}

// Stage describes what currently needs attention; process status stays separate.
func (t Task) Stage() string {
	if t.Status == Canceled {
		return "canceled"
	}
	if t.MergeStatus == MergeWaiting {
		return "waiting for subagents"
	}
	if t.MergeStatus == MergeFailed {
		return "merge failed"
	}
	if t.undeliveredForAttempt(t.currentAttemptID()) {
		return "input undelivered"
	}
	if t.AwaitingReview() {
		return "awaiting review"
	}
	if r, ok := t.CurrentReport(); ok {
		released := false
		for _, c := range t.Context {
			if c.CreatedAt.After(r.CreatedAt) {
				released = true
				break
			}
		}
		switch r.Kind {
		case "ready":
			return "awaiting review"
		case "blocked", "failed", "waiting":
			if !released {
				return r.Kind
			}
		}
	}
	switch t.Status {
	case Running:
		return "working"
	case Queued:
		if t.ScheduleRequested {
			return "scheduled"
		}
		return "queued"
	case Completed:
		return "finished"
	case Failed:
		return "failed"
	case Interrupted:
		return "interrupted"
	}
	return string(t.Status)
}
