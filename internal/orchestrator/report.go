package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Report is an explicit observation, separate from process execution status.
// Reports do not grant authority or change the original mandate.
type Report struct {
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordReport records a local operator or adapter observation. Agent prose is
// never interpreted as a report automatically.
func (s *Service) RecordReport(taskID, kind, message string) error {
	switch kind {
	case "progress", "waiting", "blocked", "ready", "failed":
	default:
		return fmt.Errorf("unknown report kind %q", kind)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("report message is required")
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
		old := clone(*t)
		now := time.Now().UTC()
		t.Reports = append(t.Reports, Report{kind, message, now})
		t.UpdatedAt = now
		t.Events = append(t.Events, Event{now, "report_" + kind, message})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

// CurrentReport excludes observations made before the current attempt began.
func (t Task) CurrentReport() (Report, bool) {
	if len(t.Reports) == 0 {
		return Report{}, false
	}
	r := t.Reports[len(t.Reports)-1]
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
	var started time.Time
	if len(t.Attempts) > 0 {
		started = t.Attempts[len(t.Attempts)-1].StartedAt
	}
	for _, c := range t.Context {
		if !c.CreatedAt.Before(started) && (c.Delivery == "rejected" || c.Delivery == "unknown") {
			return "input undelivered"
		}
	}
	if r, ok := t.CurrentReport(); ok {
		released := false
		for _, c := range t.Context {
			if c.CreatedAt.After(r.CreatedAt) {
				released = true
			}
		}
		switch r.Kind {
		case "ready":
			return "awaiting review"
		case "blocked", "failed":
			if !released {
				return r.Kind
			}
		case "waiting":
			if !released {
				return "waiting"
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
