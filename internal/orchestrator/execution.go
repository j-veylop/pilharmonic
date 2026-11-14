package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

const maxOutput = 256 * 1024

// outputFlushSize bounds streamed output that a crash can lose. Output is not
// an authorization decision, so it batches; lifecycle changes save at once.
const outputFlushSize = 8 * 1024

// trimToRuneStart keeps at most the last n bytes of s without splitting a rune.
func trimToRuneStart(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// outputWriter is the sink handed to one attempt. Every callback is bound to
// the attempt ID, so an adapter retaining it cannot modify a later attempt.
type outputWriter struct {
	s         *Service
	taskID    string
	attemptID string
}

func (w outputWriter) taskLocked() (*Task, error) {
	t := w.s.taskLocked(w.taskID)
	if t == nil {
		return nil, errTaskNotFound
	}
	if t.Status != Running || len(t.Attempts) == 0 || t.currentAttemptID() != w.attemptID {
		return nil, errors.New("execution attempt is no longer running")
	}
	return t, nil
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	t, err := w.taskLocked()
	if err != nil {
		return 0, err
	}
	attempt := &t.Attempts[len(t.Attempts)-1]
	oldOutput, oldAttemptOutput, oldUpdatedAt := t.Output, attempt.Output, t.UpdatedAt
	t.Output = trimToRuneStart(t.Output+string(p), maxOutput)
	t.UpdatedAt = time.Now().UTC()
	attempt.Output = t.Output
	select {
	case w.s.activity[w.taskID] <- struct{}{}:
	default:
	}
	w.s.outputPending += len(p)
	if w.s.outputPending < outputFlushSize {
		return len(p), nil
	}
	if err := w.s.save(); err != nil {
		t.Output, attempt.Output, t.UpdatedAt = oldOutput, oldAttemptOutput, oldUpdatedAt
		w.s.outputPending -= len(p)
		return 0, err
	}
	return len(p), nil
}

func (w outputWriter) contextAck(triggerID string, accepted bool, reason string) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	task, err := w.taskLocked()
	if err != nil {
		return
	}
	state, deliveryError := DeliveryRejected, reason
	if accepted {
		state, deliveryError = DeliveryAccepted, ""
	}
	if err := w.s.settleDeliveryLocked(task, w.attemptID, triggerID, state, deliveryError, reason, time.Now().UTC()); err != nil {
		w.s.persistErr = err
	}
}

// settleDeliveryLocked resolves a pending context hand-off for one attempt. It
// is a no-op when the trigger is not pending. Caller holds mu.
func (s *Service) settleDeliveryLocked(task *Task, attemptID, triggerID, state, deliveryError, reason string, now time.Time) error {
	for i := range task.Context {
		if task.Context[i].ID != triggerID || task.Context[i].Delivery != DeliveryPending {
			continue
		}
		return s.mutateTaskLocked(task, func(task *Task) error {
			task.Context[i].Delivery, task.Context[i].DeliveryError = state, deliveryError
			if !task.updatePendingDelivery(triggerID, attemptID, state, deliveryError, now) {
				task.appendDelivery(triggerID, attemptID, state, deliveryError, now)
			}
			task.Events = append(task.Events, Event{now, "context_" + state, "Context " + triggerID + ": " + reason})
			return nil
		})
	}
	return nil
}

func (s *Service) run(ctx context.Context, t Task, r Runner) {
	defer s.wg.Done()
	t, err := s.prepareExecution(ctx, t)
	if err == nil {
		var stop func()
		ctx, stop = s.watchIdle(ctx, t.ID)
		defer stop()
		writer := outputWriter{s: s, taskID: t.ID, attemptID: t.currentAttemptID()}
		err = runRecover(func() error {
			if lr, ok := r.(LiveRunner); ok {
				s.mu.Lock()
				live := s.live[t.ID]
				s.mu.Unlock()
				return lr.RunLive(ctx, t, writer, live, writer.contextAck)
			}
			return r.Run(ctx, t, writer)
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var integration func(*Task)
	if err == nil && ctx.Err() == nil && t.Isolated {
		integration = s.integrateAttemptLocked(t)
	}
	defer s.cancel[t.ID]()
	delete(s.cancel, t.ID)
	delete(s.live, t.ID)
	task := s.taskLocked(t.ID)
	if task == nil {
		return
	}
	now := time.Now().UTC()
	task.controlSocket, task.controlToken, task.controlGuidance = "", "", ""
	task.settlePendingDeliveries(task.currentAttemptID(), "execution ended before acknowledgement", now)
	switch {
	case ctx.Err() != nil:
		task.endAttempt(Canceled, cancelReason(ctx), now)
	case err != nil:
		task.endAttempt(Failed, err.Error(), now)
	default:
		task.endAttempt(Completed, "", now)
	}
	task.integrating = false
	if integration != nil {
		integration(task)
	}
	if err := s.save(); err != nil {
		s.persistErr = err
	} else if !s.closing {
		if schedErr := s.scheduleLocked(); schedErr != nil {
			s.persistErr = schedErr
		}
	}
}

// integrateAttemptLocked prepares a successful isolated attempt's commit for
// review and returns how to record the outcome. Caller holds mu; it is
// released while Git runs.
func (s *Service) integrateAttemptLocked(t Task) func(*Task) {
	if t.noAutoIntegrate() {
		return func(task *Task) {
			task.Events = append(task.Events, Event{task.UpdatedAt, "integration_skipped", "Profile " + task.ProfileName + " does not integrate prepared work; worktree retained for the operator"})
		}
	}
	owner := s.taskLocked(t.ID)
	if owner == nil {
		return nil
	}
	if blocker, blocked := s.integrationBlockerLocked(*owner); blocked {
		return func(task *Task) { task.recordMerge(blocker) }
	}
	owner.integrating = true
	s.mu.Unlock()
	preparation, err := s.prepareIsolatedCommit(t)
	s.mu.Lock()
	if err != nil {
		failed := mergeOutcome{Status: MergeFailed, Error: err.Error(), WorktreeCommit: preparation.WorktreeCommit}
		return func(task *Task) { task.recordMerge(failed) }
	}
	return func(task *Task) { task.recordPreparation(preparation) }
}

// runRecover converts a runner panic into an error so one misbehaving adapter
// cannot wedge the service.
func runRecover(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("runner panic: %v", r)
		}
	}()
	return fn()
}

func cancelReason(ctx context.Context) string {
	cause := context.Cause(ctx)
	if errors.Is(cause, context.Canceled) && !errors.Is(cause, ErrIdleTimeout) {
		return "canceled by operator"
	}
	return cause.Error()
}

// ErrIdleTimeout is the cancellation cause recorded when a running agent
// stops producing output for the configured idle timeout.
var ErrIdleTimeout = errors.New("agent idle timeout")

// SetIdleTimeout bounds how long a running agent may stay silent. Output
// counts as activity; zero, the default, disables the watchdog. It applies to
// attempts started after the change.
func (s *Service) SetIdleTimeout(timeout time.Duration) error {
	if timeout < 0 {
		return errors.New("idle timeout must not be negative")
	}
	return s.whileOpen(func() error { s.idleTimeout = timeout; return nil })
}

// watchIdle cancels an attempt that stays silent for the idle timeout. run
// calls the returned stop function after the runner exits, so the watchdog
// cannot cancel a later attempt or leak.
func (s *Service) watchIdle(parent context.Context, taskID string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	s.mu.Lock()
	timeout := s.idleTimeout
	ping := make(chan struct{}, 1)
	if timeout > 0 {
		s.activity[taskID] = ping
	}
	s.mu.Unlock()
	if timeout <= 0 {
		return ctx, func() { cancel(nil) }
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ping:
				timer.Reset(timeout)
			case <-timer.C:
				cancel(fmt.Errorf("%w: no agent output for %s", ErrIdleTimeout, timeout))
				return
			}
		}
	}()
	return ctx, func() {
		cancel(nil)
		<-done
		s.mu.Lock()
		// A retried attempt installs its own ping; only remove ours.
		if s.activity[taskID] == ping {
			delete(s.activity, taskID)
		}
		s.mu.Unlock()
	}
}
