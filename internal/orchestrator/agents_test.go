package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgentConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.json")
	must(t, os.WriteFile(path, []byte(text), 0600))
	return path
}
func TestAgentConfigurationAndSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := mustGet(Open(path))(t)
	b, _ := json.Marshal(map[string]any{"agents": []AgentConfig{{Name: "echo", Command: "cat"}}})
	must(t, s.LoadAgents(writeAgentConfig(t, string(b))))
	agents := s.ListAgents()
	if len(agents) != 2 || agents[0].Name != "echo" || agents[0].Capabilities != (AgentCapabilities{}) ||
		agents[1].Capabilities != (AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true}) {
		t.Fatalf("bad capabilities: %+v", agents)
	}
	task := createTask(t, s, "task", "literal $(touch forbidden)", t.TempDir())
	must(t, s.SetTaskAgent(task.ID, "echo"))
	must(t, s.Start(context.Background(), task.ID))
	got := await(t, s, task.ID, Completed)
	if !strings.Contains(got.Output, "literal $(touch forbidden)") || got.Attempts[0].Agent != "echo" {
		t.Fatalf("bad run: %+v", got)
	}
	if err := s.SetTaskAgent(task.ID, "general"); err == nil {
		t.Fatal("changed terminal task")
	}
	restored := mustGet(Open(path))(t)
	got, _ = restored.GetTask(task.ID)
	if got.Agent != "echo" {
		t.Fatal("agent not persisted")
	}
}
func TestAgentConfigAtomicValidation(t *testing.T) {
	for _, text := range []string{
		`{"agents":[{"name":"general","command":"cat"}]}`,
		`{"agents":[{"name":"x","command":"cat"},{"name":"x","command":"cat"}]}`,
		`{"agents":[{"name":"x","command":"cat"},{"name":"y","command":"/missing-agent-command"}]}`,
		`{"agents":[{"name":"x","command":"cat","unknown":true}]}`,
		`{"agents":[]} {}`,
		`{"agents":[{"name":"","command":"cat"}]}`,
		`{"agents":[{"name":"bad name","command":"cat"}]}`,
		`{"agents":[{"name":"bad\"name","command":"cat"}]}`,
		`{"agents":[{"name":"bad%name","command":"cat"}]}`,
		`{"agents":[{"name":"` + strings.Repeat("a", 65) + `","command":"cat"}]}`,
	} {
		s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
		if err := s.LoadAgents(writeAgentConfig(t, text)); err == nil {
			t.Fatalf("accepted %s", text)
		}
		if len(s.ListAgents()) != 1 {
			t.Fatal("partial registration")
		}
	}
}
func TestCommandRunnerPassesContextOnStdin(t *testing.T) {
	var out bytes.Buffer
	err := (CommandRunner{Command: "cat"}).Run(context.Background(), Task{Prompt: "mandate", Workspace: t.TempDir(), Context: []Trigger{{Text: "context"}}, ProfileSpec: &Profile{Name: "role", Instructions: "role policy"}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mandate") || !strings.Contains(out.String(), "Additional context") || !strings.Contains(out.String(), "context") || !strings.Contains(out.String(), "role policy") {
		t.Fatal(out.String())
	}
}

func TestBuiltinSpecialistsRespectConfiguredExecutor(t *testing.T) {
	s := newTestService(t)
	must(t, s.LoadAgents(writeAgentConfig(t, `{"agents":[{"name":"tester","command":"cat","description":"Custom test runner"}]}`)))
	s.LoadBuiltinSpecialists()
	reviewer, ok := s.runners["reviewer"].(FantasyRunner)
	if !ok || reviewer.instructions == "" || !strings.Contains(reviewer.systemPrompt(Task{}), reviewer.instructions) {
		t.Fatal("missing built-in review instructions")
	}
	if _, ok := s.runners["tester"].(CommandRunner); !ok || s.agentDescriptions["tester"] != "Custom test runner" {
		t.Fatal("configured specialist was replaced")
	}
}

func TestBuiltinSpecialistsUseLeastPrivilegeTools(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	for name, want := range map[string]string{
		"reviewer":    "read,grep,rg,glob,find,ls",
		"explorer":    "read,grep,rg,glob,find,ls",
		"tester":      "read,grep,rg,glob,find,ls,bash,diagnostics",
		"implementer": "read,edit,write,multiedit,grep,rg,glob,find,ls,bash,diagnostics",
		"debugger":    "read,bash,grep,rg,glob,find,ls,diagnostics",
		"gitter":      "read,bash,edit,write,multiedit,grep,rg,glob,find,ls,diagnostics",
	} {
		runner, ok := s.runners[name].(FantasyRunner)
		if !ok || runner.instructions == "" || s.agentDescriptions[name] == "" {
			t.Fatalf("specialist %q is missing, or lacks instructions or a description", name)
		}
		got := strings.Join(runner.toolPalette(nil), ",")
		if got != want {
			t.Errorf("specialist %q tools = %q, want %q", name, got, want)
		}
	}
}

func TestSpecialistSelectionAndModel(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	must(t, s.LoadProfiles(writeProfileConfig(t, `{"default_profile":"coordinator","profiles":[{"name":"coordinator","model":"provider/parent"}],"specialists":[{"name":"tester","model":"provider/example","tools":["read"]}]}`)))
	for request, want := range map[string]string{
		"@tester Please add tests for the parser": "tester", "@general add a parser": "general",
		"@reviewer Review the diff": "reviewer", "@explorer Trace the call": "explorer", "@debugger Diagnose the timeout": "debugger",
		"@gitter Rebase the branch": "gitter", "Something unexpected happened": "general",
		"Please add tests for the parser": "general",
	} {
		task, err := s.CreateTask("draft", request, t.TempDir())
		if err != nil || task.Agent != want {
			t.Fatalf("%q: agent %q, err %v", request, task.Agent, err)
		}
		if task.ProfileSpec.Model != "provider/parent" {
			t.Fatalf("default profile not applied: %+v", task.ProfileSpec)
		}
		if want == "tester" && (len(task.SpecialistTools) != 1 || task.SpecialistTools[0] != "read") {
			t.Fatalf("tester tool limit: %v", task.SpecialistTools)
		}
	}
	if got := requestedModel(s, "tester", &Profile{Model: "provider/parent"}); got != "provider/parent" {
		t.Fatalf("tester model = %q, want provider/parent", got)
	}
	if got := requestedModel(s, "tester", &Profile{}); got != "provider/example" {
		t.Fatalf("tester model = %q, want provider/example", got)
	}
}
