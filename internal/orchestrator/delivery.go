package orchestrator

import "time"

// Delivery states describe one attempt to hand contextual input to one
// execution. Delivery is separate from the executor's acknowledgement and from
// the model actually consuming the input.
const (
	DeliveryAtStart  = "at_start"
	DeliveryPending  = "pending"
	DeliveryAccepted = "accepted"
	DeliveryRejected = "rejected"
	DeliveryUnknown  = "unknown"
)

// Delivery is the durable per-ticket record of an attempted hand-off. Context
// recorded for a queued task is recorded here when an attempt starts; context
// sent to a live execution is recorded when it is queued or rejected. A retry
// creates new records for that attempt and never rewrites earlier attempts.
type Delivery struct {
	TriggerID string    `json:"trigger_id"`
	AttemptID string    `json:"attempt_id,omitempty"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t Task) currentAttemptID() string {
	if len(t.Attempts) == 0 {
		return ""
	}
	return t.Attempts[len(t.Attempts)-1].ID
}

func (t *Task) appendDelivery(triggerID, attemptID, state, reason string, now time.Time) {
	t.Deliveries = append(t.Deliveries, Delivery{
		TriggerID: triggerID,
		AttemptID: attemptID,
		State:     state,
		Error:     reason,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// recordStartDeliveries ties every recorded context item to the new attempt.
// The context was included in the initial prompt, but that is not an
// acknowledgement and must not be reported as one.
func (t *Task) recordStartDeliveries(attemptID string, now time.Time) {
	for i := range t.Context {
		t.Context[i].Delivery = DeliveryAtStart
		t.Context[i].DeliveryError = ""
		t.appendDelivery(t.Context[i].ID, attemptID, DeliveryAtStart, "", now)
	}
}

// updatePendingDelivery resolves the pending record for a trigger in the given
// attempt. It returns false when no such record exists (for example, in state
// written before per-attempt delivery records were introduced).
func (t *Task) updatePendingDelivery(triggerID, attemptID, state, reason string, now time.Time) bool {
	for i := len(t.Deliveries) - 1; i >= 0; i-- {
		d := &t.Deliveries[i]
		if d.TriggerID != triggerID || d.AttemptID != attemptID || d.State != DeliveryPending {
			continue
		}
		d.State = state
		d.Error = reason
		d.UpdatedAt = now
		return true
	}
	return false
}

func (t *Task) setContextDelivery(triggerID, state, reason string) {
	for i := len(t.Context) - 1; i >= 0; i-- {
		if t.Context[i].ID != triggerID {
			continue
		}
		t.Context[i].Delivery = state
		t.Context[i].DeliveryError = reason
		return
	}
}

// settlePendingDeliveries marks unresolved hand-offs from one attempt as
// unknown. A process ending, a restart or cancellation cannot prove delivery.
func (t *Task) settlePendingDeliveries(attemptID, reason string, now time.Time) {
	for i := range t.Deliveries {
		d := &t.Deliveries[i]
		if d.AttemptID != attemptID || d.State != DeliveryPending {
			continue
		}
		d.State = DeliveryUnknown
		d.Error = reason
		d.UpdatedAt = now
	}
	if attemptID != "" && attemptID == t.currentAttemptID() {
		for _, c := range t.Context {
			if c.Delivery == DeliveryPending {
				t.setContextDelivery(c.ID, DeliveryUnknown, reason)
			}
		}
	}
}

// settleAllPendingDeliveries is used when opening persisted state: no process
// survived, so no pending hand-off can be proven.
func (t *Task) settleAllPendingDeliveries(reason string, now time.Time) bool {
	changed := false
	for i := range t.Deliveries {
		d := &t.Deliveries[i]
		if d.State != DeliveryPending {
			continue
		}
		d.State = DeliveryUnknown
		d.Error = reason
		d.UpdatedAt = now
		changed = true
	}
	for i := range t.Context {
		if t.Context[i].Delivery != DeliveryPending {
			continue
		}
		t.Context[i].Delivery = DeliveryUnknown
		t.Context[i].DeliveryError = reason
		changed = true
	}
	return changed
}

// undeliveredForAttempt reports whether any contextual input for this attempt
// was rejected or remains unproven. Older state without delivery records falls
// back to the per-context summary.
func (t Task) undeliveredForAttempt(attemptID string) bool {
	if len(t.Deliveries) == 0 {
		var started time.Time
		if len(t.Attempts) > 0 {
			started = t.Attempts[len(t.Attempts)-1].StartedAt
		}
		for _, c := range t.Context {
			if !c.CreatedAt.Before(started) && (c.Delivery == DeliveryRejected || c.Delivery == DeliveryUnknown) {
				return true
			}
		}
		return false
	}
	for _, d := range t.Deliveries {
		if d.AttemptID == attemptID && (d.State == DeliveryRejected || d.State == DeliveryUnknown) {
			return true
		}
	}
	return false
}
