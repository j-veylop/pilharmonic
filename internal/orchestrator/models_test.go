package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func TestSubscriptionModelCatalog(t *testing.T) {
	s := newTestService(t)
	models := s.ModelsForProvider("openai-subscription")
	if len(models) == 0 {
		t.Fatal("missing subscription fallback catalog")
	}
	seen := map[string]bool{}
	for _, model := range models {
		if model == "" || seen[model] {
			t.Fatalf("empty or duplicate subscription model: %q", model)
		}
		seen[model] = true
		must(t, validateModelSelector("openai-subscription/"+model))
	}
	for _, model := range []string{"gpt-5-codex", "gpt-5.1-codex-mini", "gpt-5.2-codex"} {
		if !seen[model] {
			t.Fatalf("missing fallback model %q", model)
		}
	}
	models[0] = "mutated"
	if s.ModelsForProvider("openai-subscription")[0] == "mutated" {
		t.Fatal("catalog exposed shared storage")
	}
	must(t, s.SetModel(ModelClassBig, "openai-subscription/gpt-5-codex"))
	if got := requestedModel(s, DefaultRouteAgent, nil); got != "openai-subscription/gpt-5-codex" {
		t.Fatalf("subscription selector not applied: %q", got)
	}
}

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

	if got := requestedModel(s, "explorer", nil); got != "deepseek/deepseek-flash" {
		t.Fatalf("small model not applied to explorer: %q", got)
	}
	if got := requestedModel(s, DefaultRouteAgent, nil); got != "" {
		t.Fatalf("small model leaked to general: %q", got)
	}

	// It persists across reopen.
	reopened := mustGet(Open(path))(t)
	defer func() { _ = reopened.Shutdown() }()
	reopened.LoadBuiltinSpecialists()
	if got := requestedModel(reopened, "explorer", nil); got != "deepseek/deepseek-flash" {
		t.Fatalf("small model not persisted: %q", got)
	}

	// Clearing reverts to the profile/default model.
	must(t, reopened.SetModel(ModelClassSmall, ""))
	if requestedModel(reopened, "explorer", nil) != "" {
		t.Fatalf("model not cleared: %q", requestedModel(reopened, "explorer", nil))
	}
}

func TestFantasyModelResolutionOrder(t *testing.T) {
	for _, tc := range []struct {
		name, profile, specialist, big, small, flag, want string
	}{
		{name: "profile concrete beats everything", profile: "hyper/profile", specialist: "hyper/specialist", big: "hyper/big", small: "hyper/small", flag: "hyper/flag", want: "hyper/profile"},
		{name: "profile alias uses class model", profile: ModelClassBig, specialist: "hyper/specialist", big: "hyper/big", small: "hyper/small", flag: "hyper/flag", want: "hyper/big"},
		{name: "profile alias with unset class falls through", profile: ModelClassBig, specialist: ModelClassBig, small: "hyper/small", flag: "hyper/flag", want: "hyper/flag"},
		{name: "specialist alias picks class", specialist: ModelClassBig, big: "hyper/big", small: "hyper/small", flag: "hyper/flag", want: "hyper/big"},
		{name: "default class beats specialist concrete", specialist: "hyper/specialist", small: "hyper/small", want: "hyper/small"},
		{name: "specialist concrete when class unset", specialist: "hyper/specialist", big: "hyper/big", flag: "hyper/flag", want: "hyper/specialist"},
		{name: "flag last", big: "hyper/big", flag: "hyper/flag", want: "hyper/flag"},
		{name: "nothing configured", profile: ModelClassSmall, specialist: ModelClassSmall, want: ""},
		{name: "flag alias uses class model", big: "hyper/big", flag: ModelClassBig, want: "hyper/big"},
		{name: "unset flag alias resolves to nothing", flag: ModelClassSmall, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t)
			s.LoadBuiltinSpecialists()
			must(t, s.SetModel(ModelClassBig, tc.big))
			must(t, s.SetModel(ModelClassSmall, tc.small))
			s.SetDefaultModel(tc.flag)
			if tc.specialist != "" {
				must(t, s.LoadProfiles(writeProfileConfig(t, `{"specialists":[{"name":"tester","model":"`+tc.specialist+`"}]}`)))
			}
			if got := requestedModel(s, "tester", &Profile{Name: "task", Model: tc.profile}); got != tc.want {
				t.Fatalf("model = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentModelOverridesPersistAndRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := openTestService(t, path)
	s.LoadBuiltinSpecialists()
	must(t, s.SetModel(ModelClassSmall, "deepseek/deepseek-chat"))
	must(t, s.SetAgentModel("tester", ModelClassBig))
	must(t, s.SetModel(ModelClassBig, "openai/gpt-5"))
	if got := requestedModel(s, "tester", nil); got != "openai/gpt-5" {
		t.Fatalf("agent override = %q", got)
	}
	if got := requestedModel(s, "explorer", nil); got != "deepseek/deepseek-chat" {
		t.Fatalf("class fallback = %q", got)
	}
	must(t, s.Shutdown())
	reopened := openTestService(t, path)
	if got := requestedModel(reopened, "tester", nil); got != "openai/gpt-5" {
		t.Fatalf("persisted override = %q", got)
	}
	must(t, reopened.SetAgentModel("tester", "openai/gpt-4.1"))
	for _, invalid := range []string{"unknown/model", "openai/"} {
		if err := reopened.SetAgentModel("tester", invalid); err == nil {
			t.Fatalf("invalid selector %q accepted", invalid)
		}
	}
	must(t, reopened.Shutdown())
	reloaded := openTestService(t, path)
	if got := requestedModel(reloaded, "tester", nil); got != "openai/gpt-4.1" {
		t.Fatalf("custom model not persisted: %q", got)
	}
	if got := requestedModel(reloaded, "tester", &Profile{Model: "openai/gpt-5"}); got != "openai/gpt-5" {
		t.Fatalf("custom model overrode the task profile: %q", got)
	}
	must(t, reloaded.SetAgentModel("tester", ModelClassSmall))
	if got := requestedModel(reloaded, "tester", nil); got != "deepseek/deepseek-chat" {
		t.Fatalf("class assignment did not replace custom model: %q", got)
	}
}

func TestAgentClassLoadsAndPreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	must(t, os.WriteFile(modelsPath, []byte(`{"big":"hyper/prism","agents":{"tester":"big"}}`), 0600))
	s := openTestService(t, filepath.Join(dir, "state.json"))
	s.LoadBuiltinSpecialists()
	if got := requestedModel(s, "tester", nil); got != "hyper/prism" {
		t.Fatalf("agent class not applied: %q", got)
	}
	if got := requestedModel(s, DefaultRouteAgent, nil); got != "hyper/prism" {
		t.Fatalf("big model not loaded: %q", got)
	}
	must(t, s.SetModel(ModelClassSmall, "hyper/prism-flash"))
	b := mustGet(os.ReadFile(modelsPath))(t)
	if !strings.Contains(string(b), `"agents"`) || !strings.Contains(string(b), "hyper/prism-flash") || !strings.Contains(string(b), `"big"`) {
		t.Fatalf("models.json not rewritten: %s", b)
	}
}

func TestLegacyProfileModelStillRuns(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	fixture := `{"tasks":[{"id":"legacy","title":"old","prompt":"old mandate","workspace":` + strconv.Quote(t.TempDir()) + `,"status":"queued","agent":"general","profile":"old","profile_spec":{"name":"old","model":"hyper/frozen"},"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z","events":[]}]}`
	must(t, os.WriteFile(statePath, []byte(fixture), 0600))
	s := openTestService(t, statePath)
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	s.SetDefaultModel("hyper/flag")
	requested := make(chan string, 1)
	s.RegisterRunner(DefaultRouteAgent, FantasyRunner{ModelFactory: func(_ context.Context, model string) (fantasy.LanguageModel, error) {
		requested <- model
		return &fakeFantasyModel{scripts: [][]fantasy.StreamPart{textDelta("done")}}, nil
	}})
	must(t, s.Start(context.Background(), "legacy"))
	done := await(t, s, "legacy", Completed)
	if got := <-requested; got != "hyper/frozen" || done.Attempts[len(done.Attempts)-1].RequestedModel != got {
		t.Fatalf("adapter ran %q, attempt recorded %q", got, done.Attempts[len(done.Attempts)-1].RequestedModel)
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

func requestedModel(s *Service, agent string, profile *Profile) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fantasyModelLocked(agent, profile)
}

// TestFantasyAttemptRunsTheRecordedModel checks the service fixes the resolved
// selector on the attempt and hands exactly that selector to the adapter.
func TestFantasyAttemptRunsTheRecordedModel(t *testing.T) {
	s := newTestService(t)
	must(t, s.SetModel(ModelClassBig, "hyper/prism-flash"))
	must(t, s.SetAgentModel(DefaultRouteAgent, "hyper/prism"))
	requested := make(chan string, 1)
	s.RegisterRunner(DefaultRouteAgent, FantasyRunner{ModelFactory: func(_ context.Context, model string) (fantasy.LanguageModel, error) {
		requested <- model
		return &fakeFantasyModel{scripts: [][]fantasy.StreamPart{textDelta("done")}}, nil
	}})
	task := createTask(t, s, "model", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if got := <-requested; got != "hyper/prism" || done.Attempts[0].RequestedModel != got {
		t.Fatalf("adapter ran %q, attempt recorded %q", got, done.Attempts[0].RequestedModel)
	}
}
