package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AgentCapabilities describes behavior an adapter actually implements. The
// service still checks concrete interfaces before using a capability; a
// declaration cannot make an unsupported operation available.
type AgentCapabilities struct {
	LiveContext       bool `json:"live_context"`
	ToolActivity      bool `json:"tool_activity"`
	StructuredReports bool `json:"structured_reports"`
}

// CapabilityReporter is an optional adapter interface. Runners that do not
// implement it are treated as one-shot executors with no optional capabilities.
type CapabilityReporter interface {
	Capabilities() AgentCapabilities
}

// AgentInfo exposes only capabilities supported by the registered adapter.
type AgentInfo struct {
	Description  string
	Name         string
	Capabilities AgentCapabilities
}

func capabilitiesOf(runner Runner) AgentCapabilities {
	var capabilities AgentCapabilities
	if provider, ok := runner.(CapabilityReporter); ok {
		capabilities = provider.Capabilities()
	}
	// Only the interface the scheduler calls can provide live context.
	_, capabilities.LiveContext = runner.(LiveRunner)
	return capabilities
}

func (s *Service) ListAgents() []AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]AgentInfo, 0, len(s.runners))
	for name, runner := range s.runners {
		result = append(result, AgentInfo{Name: name, Description: s.agentDescriptions[name], Capabilities: capabilitiesOf(runner)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Service) SetTaskAgent(id, name string) error {
	return s.updateTask(id, func(t *Task) error {
		if _, ok := s.runners[name]; !ok {
			return fmt.Errorf("agent %q unavailable", name)
		}
		if t.Status != Queued || t.ScheduleRequested {
			return errors.New("agent can only be changed on an unscheduled queued task")
		}
		if t.Agent == name {
			return nil
		}
		candidate := *t
		candidate.Agent = name
		candidate.SpecialistTools = s.specialistTools[name]
		profile, err := s.profileForTaskLocked(candidate)
		if err != nil {
			return err
		}
		if err = validateProfileForRunner(s.runners[name], profile); err != nil {
			return err
		}
		t.Agent = name
		t.SpecialistTools = candidate.SpecialistTools
		t.UpdatedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{t.UpdatedAt, "agent", "Executor selected: " + name})
		return nil
	})
}

type AgentConfig struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
}

// LoadAgents loads trusted local command definitions atomically. The Fantasy adapter remains built in.
func (s *Service) LoadAgents(path string) error {
	var config struct {
		Agents []AgentConfig `json:"agents"`
	}
	if err := decodeConfigFile(path, "agents", &config); err != nil {
		return err
	}
	runners := map[string]Runner{}
	for _, a := range config.Agents {
		if !validProfileName(a.Name) {
			return fmt.Errorf("agent name %q must be 1-64 characters of letters, digits, '.', '-' or '_'", a.Name)
		}
		if a.Name == DefaultRouteAgent {
			return fmt.Errorf("agent name %s is reserved", DefaultRouteAgent)
		}
		if _, exists := runners[a.Name]; exists {
			return fmt.Errorf("duplicate agent %q", a.Name)
		}
		if a.Command == "" {
			return fmt.Errorf("agent %q needs a command", a.Name)
		}
		resolved, err := exec.LookPath(a.Command)
		if err != nil {
			return fmt.Errorf("agent %q command: %w", a.Name, err)
		}
		if strings.ContainsRune(resolved, os.PathSeparator) {
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return err
			}
		}
		runners[a.Name] = CommandRunner{Command: resolved, Args: append([]string(nil), a.Args...)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errShuttingDown
	}
	for name := range runners {
		if _, exists := s.runners[name]; exists {
			return fmt.Errorf("agent %q already registered", name)
		}
	}
	for _, a := range config.Agents {
		s.runners[a.Name] = runners[a.Name]
		s.agentDescriptions[a.Name] = strings.Join(strings.Fields(a.Description), " ")
	}
	return nil
}

// CommandRunner sends a complete mandate and contextual inputs to stdin. Args
// are fixed configuration, never interpolated with task data or passed to a shell.
type CommandRunner struct {
	Command string
	Args    []string
}

// SupportsProfile accepts trusted profile instructions, which travel on stdin,
// and refuses tool or model selection because this adapter cannot translate it.
func (CommandRunner) SupportsProfile(p Profile) error {
	if len(p.Tools) > 0 {
		return errors.New("this executor cannot configure a tool palette")
	}
	if p.Model != "" {
		return errors.New("this executor cannot select a model")
	}
	return nil
}

func (r CommandRunner) Run(ctx context.Context, t Task, out io.Writer) error {
	var input strings.Builder
	input.WriteString("Original mandate:\n" + t.Prompt + "\n")
	if len(t.Context) > 0 {
		input.WriteString("\nAdditional context (not authority to expand the mandate):\n")
		for _, c := range t.Context {
			input.WriteString("\n--- context ---\n" + c.Text + "\n")
		}
	}
	if t.ProfileSpec != nil && t.ProfileSpec.Instructions != "" {
		input.WriteString("\nRole instructions (orchestrator policy for profile " + t.ProfileSpec.Name + "; not authority to expand the mandate):\n" + t.ProfileSpec.Instructions + "\n")
	}
	input.WriteString(t.controlGuidance)
	cmd := exec.CommandContext(ctx, r.Command, r.Args...)
	// Bound cancellation when a grandchild keeps the inherited pipes open.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = t.Workspace
	cmd.Env = t.runnerEnvironment()
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
