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
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestAgentConfigurationAndSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"agents": []AgentConfig{{Name: "echo", Command: "cat"}}})
	if err := s.LoadAgents(writeAgentConfig(t, string(b))); err != nil {
		t.Fatal(err)
	}
	agents := s.ListAgents()
	if len(agents) != 2 || agents[0].Name != "echo" || agents[0].Capabilities.LiveContext || !agents[1].Capabilities.LiveContext {
		t.Fatalf("bad capabilities: %+v", agents)
	}
	task, err := s.CreateTask("task", "literal $(touch forbidden)", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskAgent(task.ID, "echo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	got := await(t, s, task.ID, Completed)
	if !strings.Contains(got.Output, "literal $(touch forbidden)") || got.Attempts[0].Agent != "echo" {
		t.Fatalf("bad run: %+v", got)
	}
	if err := s.SetTaskAgent(task.ID, "general"); err == nil {
		t.Fatal("changed terminal task")
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = restored.GetTask(task.ID)
	if got.Agent != "echo" {
		t.Fatal("agent not persisted")
	}
}
func TestAgentConfigAtomicValidation(t *testing.T) {
	for _, text := range []string{`{"agents":[{"name":"general","command":"cat"}]}`, `{"agents":[{"name":"x","command":"cat"},{"name":"x","command":"cat"}]}`, `{"agents":[{"name":"x","command":"cat"},{"name":"y","command":"/missing-agent-command"}]}`, `{"agents":[{"name":"x","command":"cat","unknown":true}]}`, `{"agents":[]} {}`} {
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
	err := (CommandRunner{Command: "cat"}).Run(context.Background(), Task{Prompt: "mandate", Workspace: t.TempDir(), Context: []Trigger{{Text: "context"}}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mandate") || !strings.Contains(out.String(), "not authority") || !strings.Contains(out.String(), "context") {
		t.Fatal(out.String())
	}
}

func TestAgentCapabilityRegistry(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	agents := s.ListAgents()
	if len(agents) != 1 || agents[0].Name != "general" || !agents[0].Capabilities.LiveContext || !agents[0].Capabilities.ToolActivity || !agents[0].Capabilities.StructuredReports {
		t.Fatalf("bad general capabilities: %+v", agents)
	}
	s.RegisterRunner("echo", CommandRunner{Command: "cat"})
	for _, agent := range s.ListAgents() {
		if agent.Name != "echo" {
			continue
		}
		if agent.Capabilities.LiveContext || agent.Capabilities.ToolActivity || agent.Capabilities.StructuredReports {
			t.Fatalf("command runner overclaimed capabilities: %+v", agent)
		}
		return
	}
	t.Fatal("echo runner not listed")
}

func TestBuiltinSpecialistsRespectConfiguredExecutor(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.LoadAgents(writeAgentConfig(t, `{"agents":[{"name":"tester","command":"cat","description":"Custom test runner"}]}`)); err != nil {
		t.Fatal(err)
	}
	s.LoadBuiltinSpecialists()
	reviewer, ok := s.runners["reviewer"].(PiRunner)
	if !ok || reviewer.instructions == "" || !strings.Contains(strings.Join(reviewer.piArgs("rpc", nil), " "), reviewer.instructions) {
		t.Fatal("missing built-in review instructions")
	}
	if _, ok := s.runners["tester"].(CommandRunner); !ok || s.agentDescriptions["tester"] != "Custom test runner" {
		t.Fatal("configured specialist was replaced")
	}
}

func TestBuiltinSpecialistCatalog(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.LoadBuiltinSpecialists()
	for _, name := range []string{"debugger", "explorer", "implementer", "gitter", "reviewer", "tester"} {
		runner, ok := s.runners[name].(PiRunner)
		if !ok || runner.instructions == "" || s.agentDescriptions[name] == "" {
			t.Errorf("missing specialist instructions or description for %q", name)
		}
	}
}

func TestSpecialistSelectionAndModel(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.LoadBuiltinSpecialists()
	if err := s.LoadProfiles(writeProfileConfig(t, `{"default_profile":"coordinator","profiles":[{"name":"coordinator","model":"provider/parent"}],"specialists":[{"name":"tester","model":"provider/example","tools":["read"]}]}`)); err != nil {
		t.Fatal(err)
	}
	for request, want := range map[string]string{
		"Please add tests for the parser": "tester", "add a parser": "general",
		"Review the diff": "reviewer", "Trace the call": "explorer", "Diagnose the timeout": "debugger",
		"Rebase the branch": "gitter", "Something unexpected happened": "general",
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
	for _, mode := range []string{"rpc", "json"} {
		args := s.runners["tester"].(PiRunner).piArgs(mode, &Profile{Model: "provider/parent"})
		if args[len(args)-2] != "--model" || args[len(args)-1] != "provider/example" {
			t.Fatalf("unexpected args %v", args)
		}
	}
}
