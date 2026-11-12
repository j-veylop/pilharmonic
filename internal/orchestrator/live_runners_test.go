package orchestrator

import (
	"context"
	"io"
)

// liveTestRunner is a fake LiveRunner that acknowledges the first follow-up
// with "queued" and then waits for release.
type liveTestRunner struct {
	started  chan struct{}
	received chan Trigger
	release  chan struct{}
}

func (r liveTestRunner) Run(context.Context, Task, io.Writer) error { return nil }
func (r liveTestRunner) RunLive(ctx context.Context, _ Task, _ io.Writer, follow <-chan Trigger, ack func(string, bool, string)) error {
	close(r.started)
	select {
	case trigger := <-follow:
		r.received <- trigger
		ack(trigger.ID, true, "queued")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// unackedRunner is a fake LiveRunner that receives a follow-up but never acks it.
type unackedRunner struct {
	started  chan struct{}
	received chan struct{}
}

func (r unackedRunner) Run(context.Context, Task, io.Writer) error { return nil }
func (r unackedRunner) RunLive(ctx context.Context, _ Task, _ io.Writer, follow <-chan Trigger, _ func(string, bool, string)) error {
	close(r.started)
	select {
	case <-follow:
		close(r.received)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
