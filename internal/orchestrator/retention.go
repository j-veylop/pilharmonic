package orchestrator

import (
	"errors"
	"fmt"
)

// DefaultAttemptRetention is how many attempts per task keep their full output.
// A task that is retried many times would otherwise grow the state file by up
// to maxOutput for every attempt. Earlier attempt records, statuses, errors and
// timings remain; only their copied transcript is dropped, and the task records
// one event when that happens. Zero keeps every attempt output and is the
// explicit opt-out.
const DefaultAttemptRetention = 8

// SetAttemptRetention changes how many attempts per task keep their full
// output. It applies as each new attempt starts. Zero disables truncation.
func (s *Service) SetAttemptRetention(keep int) error {
	if keep < 0 {
		return errors.New("attempt retention must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.attemptRetention = keep
	return nil
}

// retainAttemptOutputs drops the copied transcript of attempts older than the
// newest keep attempts. It returns how many outputs it dropped so the caller
// can record one event, and it never touches attempt identity or outcome.
func retainAttemptOutputs(task *Task, keep int) int {
	if keep <= 0 || len(task.Attempts) <= keep {
		return 0
	}
	limit := len(task.Attempts) - keep
	dropped := 0
	for i := 0; i < limit; i++ {
		attempt := &task.Attempts[i]
		if attempt.Output == "" || attempt.OutputTruncated {
			continue
		}
		attempt.Output = ""
		attempt.OutputTruncated = true
		dropped++
	}
	return dropped
}

func retentionEvent(task *Task, dropped, keep int) Event {
	return Event{
		Time:    task.UpdatedAt,
		Kind:    "retention",
		Message: fmt.Sprintf("Dropped output for %d earlier attempt(s); %d newest kept in full", dropped, keep),
	}
}
