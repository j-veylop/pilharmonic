package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrIdleTimeout is the cancellation cause recorded when a running agent
// stops producing output for the configured idle timeout.
var ErrIdleTimeout = errors.New("agent idle timeout")

// SetIdleTimeout bounds how long a running agent may stay silent. Output
// counts as activity, so a healthy agent is never interrupted; the timer only
// fires when the runner produces nothing at all. Zero, the default, disables
// the watchdog and never cancels a quiet agent. The setting applies to
// attempts started after the change.
func (s *Service) SetIdleTimeout(timeout time.Duration) error {
	if timeout < 0 {
		return errors.New("idle timeout must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.idleTimeout = timeout
	return nil
}

// watchIdle cancels an attempt once it stays silent for the configured
// timeout. It returns the attempt context and a stop function; run calls the
// stop function after the runner exits so the watchdog cannot cancel a later
// attempt or leak its goroutine.
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
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				cancel(fmt.Errorf("%w: no agent output for %s", ErrIdleTimeout, timeout))
				return
			}
		}
	}()
	stop := func() {
		cancel(nil)
		<-done
		s.mu.Lock()
		// A later attempt of the same task installs a fresh ping; only remove
		// our own so a retried run keeps its watchdog.
		if s.activity[taskID] == ping {
			delete(s.activity, taskID)
		}
		s.mu.Unlock()
	}
	return ctx, stop
}
