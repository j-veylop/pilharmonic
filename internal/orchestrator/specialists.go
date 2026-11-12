package orchestrator

import (
	_ "embed"
	"strings"
	"unicode"
)

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
	{"debugger", "Reproduce a failure and isolate its cause.", debuggerPrompt, []string{"read", "bash", "grep", "find", "ls"}},
	{"explorer", "Trace code paths and explain how the repository works.", explorerPrompt, []string{"read", "grep", "find", "ls"}},
	{"implementer", "Implement a scoped change and verify its behavior.", implementerPrompt, []string{"read", "edit", "write", "grep", "find", "ls", "bash"}},
	{"gitter", "Inspect Git history and handle scoped branch, commit, and merge work.", gitterPrompt, []string{"read", "bash", "edit", "write", "grep", "find", "ls"}},
	{"reviewer", "Review changes for bugs and missing tests.", reviewerPrompt, []string{"read", "grep", "find", "ls"}},
	{"tester", "Design and run focused regression tests.", testerPrompt, []string{"read", "grep", "find", "ls", "bash"}},
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
		s.runners[role.name] = FantasyRunner{instructions: strings.TrimSpace(role.instructions), defaultTools: append([]string(nil), role.tools...), ModelFactory: s.fantasyModelFactory, modelClass: modelClassFor[role.name]}
		s.agentDescriptions[role.name] = role.description
	}
	s.applyModelSelectionLocked()
}

// SetDefaultModel fills the model on any Fantasy runner that does not already
// carry one. A profile with an explicit model always wins over this fallback,
// which exists so an operator can run without a profiles file that names a
// model.
func (s *Service) SetDefaultModel(model string) {
	if model == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, r := range s.runners {
		fr, ok := r.(FantasyRunner)
		if !ok || fr.model != "" {
			continue
		}
		fr.model = model
		s.runners[name] = fr
	}
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
