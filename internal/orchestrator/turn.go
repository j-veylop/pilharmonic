package orchestrator

import "errors"

// Turn bounds keep durable conversations from growing the state file with
// repeated tool output. A tool result larger than this is already bounded by
// the activity payload; the turn copy is tighter because every later attempt
// replays it.
const (
	maxTurnsPerAttempt = 400
	maxTurnTextBytes   = 8 << 10
	maxTurnToolBytes   = 16 << 10
	maxReplayedTurns   = 96 << 10
)

// TurnRecorder is an optional capability of a runner's output sink. It records
// the structured conversation so the next attempt can replay it.
type TurnRecorder interface{ RecordTurn(Turn) error }

// RecordTurn appends one completed turn to the running attempt. Deltas are
// never recorded; a turn is complete when its text is flushed or its tool
// result arrives.
func (w outputWriter) RecordTurn(turn Turn) error {
	turn = boundedTurn(turn)
	if turn.Role == "" {
		return nil
	}
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	task, err := w.taskLocked()
	if err != nil {
		return err
	}
	index := -1
	for i := len(task.Attempts) - 1; i >= 0; i-- {
		if task.Attempts[i].ID == w.attemptID {
			index = i
			break
		}
	}
	if index < 0 {
		return errNoRunningAttempt
	}
	old := clone(*task)
	attempt := &task.Attempts[index]
	attempt.Turns = append(attempt.Turns, turn)
	if len(attempt.Turns) > maxTurnsPerAttempt {
		attempt.Turns = append([]Turn(nil), attempt.Turns[len(attempt.Turns)-maxTurnsPerAttempt:]...)
	}
	if err := w.s.save(); err != nil {
		*task = old
		return err
	}
	return nil
}

// boundedTurn normalizes a turn and bounds its text on a rune boundary. The
// marker is visible to the model on replay, so a later attempt knows which
// results it can no longer see in full.
func boundedTurn(turn Turn) Turn {
	switch turn.Role {
	case "assistant", "tool":
	default:
		turn.Role = ""
		return turn
	}
	turn.Text = boundTurnText(turn.Text, maxTurnTextBytes)
	turn.Input = boundTurnText(turn.Input, maxTurnTextBytes)
	turn.Output = boundTurnText(turn.Output, maxTurnToolBytes)
	return turn
}

// boundTurnText keeps the newest bytes of text, marking what was dropped.
func boundTurnText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "[earlier text truncated]\n" + trimToRuneStart(text, limit)
}

// recordTurn forwards a completed turn to the sink when it supports that
// capability. A failed save fails the step so durable history is not silently
// lost while the run continues.
func recordTurn(out interface{}, turn Turn) error {
	recorder, ok := out.(TurnRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordTurn(turn)
}

// replayedTurns returns the structured turns of the task's earlier attempts in
// order, bounded by maxReplayedTurns. Oldest messages are dropped first, and a
// tool result is never kept without the assistant call that produced it.
func replayedTurns(t Task) []Turn {
	var all []Turn
	for _, attempt := range t.Attempts {
		all = append(all, attempt.Turns...)
	}
	for len(all) > 0 && turnBytes(all) > maxReplayedTurns {
		all = dropTurnExchange(all)
	}
	return all
}

func turnBytes(turns []Turn) int {
	size := 0
	for _, turn := range turns {
		size += len(turn.Text) + len(turn.Input) + len(turn.Output) + len(turn.ToolName) + len(turn.ToolCallID)
	}
	return size
}

// dropTurnExchange removes the oldest complete message group: an assistant
// turn that calls tools together with the tool turns that answer it. A plain
// assistant turn or user message is removed on its own.
func dropTurnExchange(turns []Turn) []Turn {
	if len(turns) == 0 {
		return turns
	}
	end := 1
	if turns[0].Role == "assistant" && turns[0].ToolCallID != "" {
		for end < len(turns) && turns[end].Role == "tool" {
			end++
		}
	}
	return turns[end:]
}

var errNoRunningAttempt = errors.New("attempt is not running")
