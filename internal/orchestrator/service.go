// Package orchestrator owns routing, durable task state, and agent execution.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Service struct {
	mu                 sync.Mutex
	mergeMu            sync.Mutex
	path               string
	credentialsPath    string
	creds              map[string]string
	modelSelectionPath string
	modelSelection     map[string]string
	agentClasses       map[string]string
	modelCache         modelListCache
	data               state
	runners            map[string]Runner
	specialistTools    map[string][]string
	profiles           map[string]Profile
	// routeProfile is the profile the router assigns and the default for tasks
	// created without an explicit choice.
	routeProfile   string
	cancel         map[string]context.CancelFunc
	discarding     map[string]bool
	wg             sync.WaitGroup
	closing        bool
	persistErr     error
	startupWarning string
	// outputPending counts output not yet persisted, so a crash loses at most
	// outputFlushSize bytes.
	outputPending     int
	limit             int
	live              map[string]chan Trigger
	pending           map[string]*pendingRequest
	agentDescriptions map[string]string
	// activity pings the idle watchdog of each running task, when enabled.
	activity         map[string]chan struct{}
	idleTimeout      time.Duration
	attemptRetention int
	// sessionStartedAt bounds this session's usage: attempts started after it.
	sessionStartedAt time.Time

	defaultIsolation bool

	subagentMaxDepth    int
	subagentMaxChildren int
	controlPath         string
	controlListener     net.Listener
	controlCancel       context.CancelFunc
	controlConns        map[net.Conn]struct{}
	controlWG           sync.WaitGroup
}

func Open(path string) (*Service, error) {
	if path == "" {
		return nil, errors.New("state path is required")
	}
	s := &Service{
		path:                path,
		runners:             map[string]Runner{},
		profiles:            builtinProfiles(),
		routeProfile:        DefaultProfileName,
		cancel:              make(map[string]context.CancelFunc),
		discarding:          make(map[string]bool),
		limit:               DefaultConcurrency,
		live:                make(map[string]chan Trigger),
		pending:             make(map[string]*pendingRequest),
		activity:            make(map[string]chan struct{}),
		defaultIsolation:    true,
		agentDescriptions:   map[string]string{DefaultRouteAgent: "General-purpose coding agent."},
		subagentMaxDepth:    DefaultMaxSubagentDepth,
		subagentMaxChildren: DefaultMaxSubagents,
		attemptRetention:    DefaultAttemptRetention,
		sessionStartedAt:    time.Now().UTC(),
		controlConns:        make(map[net.Conn]struct{}),
		credentialsPath:     credentialsPathFor(path),
		modelSelectionPath:  modelSelectionPathFor(path),
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	var err error
	if s.creds, err = loadCredentials(s.credentialsPath); err != nil {
		return nil, err
	}
	if s.modelSelection, s.agentClasses, err = loadModelConfig(s.modelSelectionPath); err != nil {
		return nil, err
	}
	s.runners[DefaultRouteAgent] = FantasyRunner{ModelFactory: s.fantasyModelFactory, modelClass: ModelClassBig, modelOverride: s.modelSelection[ModelClassBig]}
	if err = s.loadState(); err != nil {
		return nil, err
	}
	migrated := migrateStartupState(&s.data)
	recovered, err := recoverStartupState(s)
	if err != nil {
		return nil, err
	}
	if migrated || recovered {
		if err = s.save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Service) RegisterRunner(name string, runner Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runners[name] = runner
}

func (s *Service) ListTasks() []Task {
	s.mu.Lock()
	result := make([]Task, len(s.data.Tasks))
	for i, t := range s.data.Tasks {
		result[i] = clone(t)
	}
	s.mu.Unlock()
	sort.SliceStable(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (s *Service) GetTask(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.taskLocked(id); t != nil {
		return clone(*t), true
	}
	return Task{}, false
}

func (s *Service) Start(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.withTask(id, func(t *Task) error {
		if t.DiscardPending {
			return errors.New("worktree is being discarded; wait before starting the task")
		}
		if t.Status != Queued {
			return fmt.Errorf("task is %s, not queued", t.Status)
		}
		if t.ScheduleRequested {
			return errors.New("task is already scheduled")
		}
		runner, ok := s.runners[t.Agent]
		if !ok {
			return fmt.Errorf("agent %q unavailable", t.Agent)
		}
		profile, err := s.profileForTaskLocked(*t)
		if err != nil {
			return err
		}
		if err = validateProfileForRunner(runner, profile); err != nil {
			return err
		}
		if len(s.cancel) < s.limit {
			return s.startLocked(ctx, t)
		}
		if err := s.mutateTaskLocked(t, func(t *Task) error {
			t.ScheduleRequested = true
			t.ScheduledAt = time.Now().UTC()
			t.UpdatedAt = t.ScheduledAt
			t.Events = append(t.Events, Event{t.UpdatedAt, "scheduled", "Waiting for agent capacity"})
			return nil
		}); err != nil {
			return err
		}
		request := &pendingRequest{ctx: ctx, stop: make(chan struct{})}
		s.pending[id] = request
		if ctx.Done() != nil {
			go s.watchPending(id, request)
		}
		return nil
	})
}

func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.taskLocked(id)
	if t == nil {
		return errTaskNotFound
	}
	if t.Status == Queued {
		if err := s.cancelQueuedLocked(t, nil); err != nil {
			return err
		}
		s.resumeMergesLocked()
		return nil
	}
	if cancel, ok := s.cancel[id]; ok {
		cancel()
		return nil
	}
	return fmt.Errorf("task is %s", t.Status)
}

// Retry returns an unsuccessful task to the queue without changing its mandate.
func (s *Service) Retry(id string) error {
	return s.withTask(id, func(t *Task) error {
		if t.DiscardPending {
			return errors.New("worktree is being discarded; wait before retrying the task")
		}
		if err := s.checkRequeueIntegrationLocked(*t); err != nil {
			return err
		}
		mergeFailed := t.Status == Completed && t.MergeStatus == MergeFailed
		if t.Status != Failed && t.Status != Interrupted && t.Status != Canceled && !mergeFailed {
			return fmt.Errorf("cannot retry task in %s state", t.Status)
		}
		return s.mutateTaskLocked(t, func(t *Task) error {
			t.requeue(time.Now().UTC())
			message := "Manual retry requested"
			if mergeFailed {
				message = "Manual retry requested after automatic merge failure"
			}
			t.Events = append(t.Events, Event{t.UpdatedAt, "requeued", message})
			return nil
		})
	})
}

// Shutdown cancels active runs and waits for their processes and final state writes.
func (s *Service) Shutdown() error {
	s.mu.Lock()
	s.closing = true
	for id := range s.pending {
		s.removePendingLocked(id)
	}
	for _, cancel := range s.cancel {
		cancel()
	}
	s.mu.Unlock()
	s.closeSubagentControl()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistErr
}
