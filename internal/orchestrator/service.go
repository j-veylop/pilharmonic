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

	"pilharmonic/internal/providers"
)

type Service struct {
	mu                 sync.Mutex
	mergeMu            sync.Mutex
	path               string
	credentialsPath    string
	creds              map[string]string
	modelSelectionPath string
	modelSelection     map[string]string
	// agentModels and defaultModel are the configured Fantasy models; see
	// fantasyModelLocked.
	agentModels     map[string]string
	defaultModel    string
	modelCache      modelListCache
	listModels      func(ctx context.Context, provider, key string) ([]string, error)
	data            state
	runners         map[string]Runner
	specialistTools map[string][]string
	profiles        map[string]Profile
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
		agentModels:         map[string]string{},
		listModels:          providers.ListModels,
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
	var legacyClasses bool
	if s.modelSelection, legacyClasses, err = loadModelConfig(s.modelSelectionPath); err != nil {
		return nil, err
	}
	s.runners[DefaultRouteAgent] = FantasyRunner{ModelFactory: s.fantasyModelFactory}
	if err = s.loadState(); err != nil {
		return nil, err
	}
	if legacyClasses {
		s.addStartupWarning(`models.json per-agent classes are no longer supported and were ignored; set "model": "big" or "small" on the specialist in profiles.json`)
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

// DefaultStatePath is the state file used when none is given: state stays
// under the historical "orchestrator" directory of the user config dir.
func DefaultStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "orchestrator", "state.json"), nil
}

// Options is the startup configuration applied once by Configure. Empty paths
// default to files beside the state file.
type Options struct {
	AgentsPath     string // optional command agent definitions
	ProfilesPath   string // profiles.json; required only when set explicitly
	DefaultModel   string
	RoutingProfile string
	Isolation      bool
	MaxConcurrent  int
	IdleTimeout    time.Duration
	// AttemptRetention is how many attempts keep their output; zero keeps all.
	AttemptRetention int
	Subagents        bool
	MaxSubagentDepth int
	MaxSubagents     int
	SubagentSocket   string
}

// Configure registers executors and profiles and applies the execution
// limits, in the order resumed starts depend on. It returns the profiles file
// it loaded, or "" when the optional default file is absent.
func (s *Service) Configure(o Options) (string, error) {
	if err := s.SetDefaultIsolation(o.Isolation); err != nil {
		return "", err
	}
	if o.AgentsPath != "" {
		if err := s.LoadAgents(o.AgentsPath); err != nil {
			return "", err
		}
	}
	s.LoadBuiltinSpecialists()
	profiles := o.ProfilesPath
	if profiles == "" {
		profiles = filepath.Join(filepath.Dir(s.path), "profiles.json")
	}
	if err := s.LoadProfiles(profiles); errors.Is(err, os.ErrNotExist) && o.ProfilesPath == "" {
		profiles = ""
	} else if err != nil {
		return "", err
	}
	if o.DefaultModel != "" {
		s.SetDefaultModel(o.DefaultModel)
	}
	if o.RoutingProfile != "" {
		if err := s.SetRoutingProfile(o.RoutingProfile); err != nil {
			return "", err
		}
	}
	if !o.Subagents {
		o.MaxSubagentDepth, o.MaxSubagents = 0, 0
	}
	if err := s.SetSubagentLimits(o.MaxSubagentDepth, o.MaxSubagents); err != nil {
		return "", err
	}
	if o.MaxSubagentDepth > 0 && o.MaxSubagents > 0 {
		socket := o.SubagentSocket
		if socket == "" {
			socket = filepath.Join(filepath.Dir(s.path), "subagent.sock")
		}
		if err := s.EnableSubagentControl(socket); err != nil {
			return "", err
		}
	}
	return profiles, errors.Join(s.SetConcurrencyLimit(o.MaxConcurrent), s.SetIdleTimeout(o.IdleTimeout), s.SetAttemptRetention(o.AttemptRetention))
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
		if err := s.settleQueuedLocked(t, Canceled, "Canceled before start", nil); err != nil {
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
