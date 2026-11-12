package orchestrator

import (
	"path/filepath"
	"testing"
)

func TestModelSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	s.LoadBuiltinSpecialists()

	if err := s.SetModel(ModelClassSmall, "deepseek/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel(ModelClassSmall, "not-a-provider/model"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := s.SetModel(ModelClassSmall, "no-slash"); err == nil {
		t.Fatal("malformed selector accepted")
	}
	if err := s.SetModel("tiny", "deepseek/flash"); err == nil {
		t.Fatal("unknown class accepted")
	}

	// The small model applies to small agents, not big ones.
	explorer := s.runners["explorer"].(FantasyRunner)
	if got := explorer.RequestedModel(&Profile{Model: "hyper/prism"}); got != "deepseek/deepseek-flash" {
		t.Fatalf("small model not applied to explorer: %q", got)
	}
	implementer := s.runners["implementer"].(FantasyRunner)
	if got := implementer.RequestedModel(&Profile{Model: "hyper/prism"}); got != "hyper/prism" {
		t.Fatalf("small model leaked to implementer: %q", got)
	}

	// It persists across reopen.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Shutdown() }()
	reopened.LoadBuiltinSpecialists()
	reopenedExplorer := reopened.runners["explorer"].(FantasyRunner)
	if got := reopenedExplorer.RequestedModel(nil); got != "deepseek/deepseek-flash" {
		t.Fatalf("small model not persisted: %q", got)
	}

	// Clearing reverts to the profile/default model.
	if err := reopened.SetModel(ModelClassSmall, ""); err != nil {
		t.Fatal(err)
	}
	cleared := reopened.runners["explorer"].(FantasyRunner)
	if cleared.RequestedModel(nil) != "" {
		t.Fatalf("model not cleared: %q", cleared.RequestedModel(nil))
	}
}

func TestModelSelectionReportsClasses(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	s.LoadBuiltinSpecialists()
	if err := s.SetModel(ModelClassBig, "hyper/prism"); err != nil {
		t.Fatal(err)
	}
	sel := s.ModelSelection()
	if sel.Big != "hyper/prism" || sel.Small != "" {
		t.Fatalf("unexpected selection: %+v", sel)
	}

	classes := map[string]string{}
	for _, c := range AgentModelClasses() {
		classes[c.Agent] = c.Class
	}
	if classes["implementer"] != ModelClassBig || classes["explorer"] != ModelClassSmall || classes["reviewer"] != ModelClassSmall {
		t.Fatalf("unexpected agent classes: %v", classes)
	}
}
