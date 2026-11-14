package orchestrator

import (
	"path/filepath"
	"testing"
)

func TestModelSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := openTestService(t, path)
	s.LoadBuiltinSpecialists()

	must(t, s.SetModel(ModelClassSmall, "deepseek/deepseek-flash"))
	if err := s.SetModel(ModelClassSmall, "not-a-provider/model"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := s.SetModel(ModelClassSmall, "no-slash"); err == nil {
		t.Fatal("malformed selector accepted")
	}
	if err := s.SetModel("tiny", "deepseek/flash"); err == nil {
		t.Fatal("unknown class accepted")
	}

	// By default every specialist is small, so the small model applies to them.
	explorer := s.runners["explorer"].(FantasyRunner)
	if got := explorer.RequestedModel(&Profile{Model: "hyper/prism"}); got != "deepseek/deepseek-flash" {
		t.Fatalf("small model not applied to explorer: %q", got)
	}
	// The coordinator (general) is big by default, so the small model does not
	// leak to it.
	general := s.runners[DefaultRouteAgent].(FantasyRunner)
	if got := general.RequestedModel(&Profile{Model: "hyper/prism"}); got != "hyper/prism" {
		t.Fatalf("small model leaked to general: %q", got)
	}

	// It persists across reopen.
	reopened := mustGet(Open(path))(t)
	defer func() { _ = reopened.Shutdown() }()
	reopened.LoadBuiltinSpecialists()
	reopenedExplorer := reopened.runners["explorer"].(FantasyRunner)
	if got := reopenedExplorer.RequestedModel(nil); got != "deepseek/deepseek-flash" {
		t.Fatalf("small model not persisted: %q", got)
	}

	// Clearing reverts to the profile/default model.
	must(t, reopened.SetModel(ModelClassSmall, ""))
	cleared := reopened.runners["explorer"].(FantasyRunner)
	if cleared.RequestedModel(nil) != "" {
		t.Fatalf("model not cleared: %q", cleared.RequestedModel(nil))
	}
}

func TestAgentClassOverride(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()

	// Default: everything but general is small.
	classes := map[string]string{}
	for _, c := range s.AgentClasses() {
		classes[c.Agent] = c.Class
	}
	if classes[DefaultRouteAgent] != ModelClassBig {
		t.Fatalf("general default class = %q", classes[DefaultRouteAgent])
	}
	if classes["implementer"] != ModelClassSmall || classes["explorer"] != ModelClassSmall {
		t.Fatalf("specialist default classes: %v", classes)
	}

	// Pin implementer to the big model and set both class models.
	must(t, s.SetAgentClass("implementer", ModelClassBig))
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	implementer := s.runners["implementer"].(FantasyRunner)
	if got := implementer.RequestedModel(&Profile{Model: "hyper/prism-flash"}); got != "hyper/prism" {
		t.Fatalf("big model not applied to overridden implementer: %q", got)
	}
	if err := s.SetAgentClass("bogus", ModelClassBig); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if err := s.SetAgentClass("implementer", "huge"); err == nil {
		t.Fatal("invalid class accepted")
	}

	// Clearing reverts to the default (small).
	must(t, s.SetAgentClass("implementer", ""))
	cleared := s.runners["implementer"].(FantasyRunner)
	if got := cleared.RequestedModel(nil); got != "" {
		t.Fatalf("class override not cleared: %q", got)
	}
}

func TestModelSelectionReportsClasses(t *testing.T) {
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	sel := s.ModelSelection()
	if sel.Big != "hyper/prism" || sel.Small != "" {
		t.Fatalf("unexpected selection: %+v", sel)
	}
}
