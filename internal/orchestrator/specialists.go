package orchestrator

import (
	_ "embed"
	"os"
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

// LoadBuiltinSpecialists fills unused executor names after loading the
// operator's command configuration and before resuming queued work.
func (s *Service) LoadBuiltinSpecialists() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, role := range []struct{ name, description, instructions string }{
		{"debugger", "Reproduce a failure and isolate its cause.", debuggerPrompt},
		{"explorer", "Trace code paths and explain how the repository works.", explorerPrompt},
		{"implementer", "Implement a scoped change and verify its behavior.", implementerPrompt},
		{"gitter", "Inspect Git history and handle scoped branch, commit, and merge work.", gitterPrompt},
		{"reviewer", "Review changes for bugs and missing tests.", reviewerPrompt},
		{"tester", "Design and run focused regression tests.", testerPrompt},
	} {
		if _, exists := s.runners[role.name]; exists {
			continue
		}
		s.runners[role.name] = PiRunner{instructions: strings.TrimSpace(role.instructions), model: strings.TrimSpace(os.Getenv("ORCHESTRATOR_MODEL_" + strings.ToUpper(role.name)))}
		s.agentDescriptions[role.name] = role.description
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

func (s *Service) selectAgent(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectAgentLocked(text, DefaultRouteAgent)
}
