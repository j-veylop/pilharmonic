package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	if _, ok := s.runners[name]; !ok {
		return fmt.Errorf("agent %q unavailable", name)
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		if t.Status != Queued || t.ScheduleRequested {
			return errors.New("agent can only be changed on an unscheduled queued task")
		}
		if t.Agent == name {
			return nil
		}
		profile, err := s.profileForTaskLocked(*t)
		if err != nil {
			return err
		}
		if err = validateProfileForRunner(s.runners[name], profile); err != nil {
			return err
		}
		old := clone(*t)
		t.Agent = name
		t.UpdatedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{t.UpdatedAt, "agent", "Executor selected: " + name})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

type AgentConfig struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
}

// LoadAgents loads trusted local command definitions atomically. Pi remains built in.
func (s *Service) LoadAgents(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 1024*1024 {
		return errors.New("agents config exceeds 1 MiB")
	}
	var config struct {
		Agents []AgentConfig `json:"agents"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("agents config: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("agents config must contain exactly one JSON object")
	}
	runners := map[string]Runner{}
	for _, a := range config.Agents {
		if a.Name == "" || a.Name != strings.TrimSpace(a.Name) || strings.IndexFunc(a.Name, unicode.IsControl) >= 0 {
			return errors.New("agent name must be nonempty and contain no surrounding whitespace or control characters")
		}
		if a.Name == "pi" {
			return errors.New("agent name pi is reserved")
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
		return errors.New("service is shutting down")
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
	cmd.Dir = t.Workspace
	cmd.Env = t.runnerEnvironment()
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
