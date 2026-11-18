package orchestrator

import (
	"context"
	_ "embed"
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
	return s.whileOpen(func() error {
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
	})
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
	cmd := exec.CommandContext(ctx, r.Command, r.Args...)
	// Bound cancellation when a grandchild keeps the inherited pipes open.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = t.Workspace
	cmd.Env = t.runnerEnvironment()
	cmd.Stdin = strings.NewReader(composeInput(t, true))
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

var (
	//go:embed prompts/debugger.md
	debuggerPrompt string
	//go:embed prompts/explorer.md
	explorerPrompt string
	//go:embed prompts/implementer.md
	implementerPrompt string
	//go:embed prompts/gitter.md
	gitterPrompt string
	//go:embed prompts/reviewer.md
	reviewerPrompt string
	//go:embed prompts/tester.md
	testerPrompt string
)

// builtinSpecialistRole describes one bundled specialist executor.
type builtinSpecialistRole struct {
	name, description, instructions string
	tools                           []string
}

// builtinSpecialistRoles defines the bundled specialists: the role name,
// description, compiled instructions and least-privilege tool palette.
var builtinSpecialistRoles = []builtinSpecialistRole{
	{"debugger", "Reproduce a failure and isolate its cause.", debuggerPrompt, []string{"read", "bash", "grep", "rg", "glob", "find", "ls", "diagnostics"}},
	{"explorer", "Trace code paths and explain how the repository works.", explorerPrompt, []string{"read", "grep", "rg", "glob", "find", "ls"}},
	{"implementer", "Implement a scoped change and verify its behavior.", implementerPrompt, []string{"read", "edit", "write", "multiedit", "grep", "rg", "glob", "find", "ls", "bash", "diagnostics"}},
	{"gitter", "Inspect Git history and handle scoped branch, commit, and merge work.", gitterPrompt, []string{"read", "bash", "edit", "write", "multiedit", "grep", "rg", "glob", "find", "ls", "diagnostics"}},
	{"reviewer", "Review changes for bugs and missing tests.", reviewerPrompt, []string{"read", "grep", "rg", "glob", "find", "ls"}},
	{"tester", "Design and run focused regression tests.", testerPrompt, []string{"read", "grep", "rg", "glob", "find", "ls", "bash", "diagnostics"}},
}

// LoadBuiltinSpecialists fills unused executor names after loading the
// operator's command configuration and before resuming queued work. Each
// bundled role starts from a least-privilege tool palette so a specialist
// cannot edit, write or run commands beyond what its charge needs.
func (s *Service) LoadBuiltinSpecialists() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, role := range builtinSpecialistRoles {
		if _, exists := s.runners[role.name]; exists {
			continue
		}
		s.runners[role.name] = FantasyRunner{instructions: strings.TrimSpace(role.instructions), defaultTools: append([]string(nil), role.tools...), ModelFactory: s.fantasyModelFactory, checks: s.checks}
		s.agentDescriptions[role.name] = role.description
	}
}

// SetDefaultModel sets the model for Fantasy attempts that resolve no profile,
// class or specialist model.
func (s *Service) SetDefaultModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultModel = model
}

// selectAgentLocked matches the requested action at the start of the mandate.
// Only registered executors can be selected; other requests keep the fallback.
func (s *Service) selectAgentLocked(text, fallback string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	request := strings.Join(words, " ") + " "
	for _, prefix := range []string{"can you ", "could you ", "please "} {
		request = strings.TrimPrefix(request, prefix)
	}
	for _, rule := range []struct {
		agent   string
		actions []string
	}{
		{"tester", []string{"test", "write tests", "add tests", "add regression tests", "run tests", "run the tests"}},
		{"debugger", []string{"debug", "diagnose", "reproduce", "investigate", "find out why"}},
		{"reviewer", []string{"review", "audit", "check code quality"}},
		{"gitter", []string{"git", "commit", "rebase", "merge", "cherry pick", "create a branch", "create a worktree"}},
		{"explorer", []string{"explore", "explain", "locate", "trace", "find", "how does", "where is"}},
		{"implementer", []string{"implement", "build", "add", "fix", "refactor", "update", "create", "remove"}},
	} {
		for _, action := range rule.actions {
			if strings.HasPrefix(request, action+" ") {
				if _, ok := s.runners[rule.agent]; ok {
					return rule.agent
				}
				return fallback
			}
		}
	}
	return fallback
}
