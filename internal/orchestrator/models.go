package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Model class labels. An agent runs on either the "big" (planning, heavy
// changes) model or the "small" (fast, focused) model, both chosen by the
// operator.
const (
	ModelClassBig   = "big"
	ModelClassSmall = "small"
)

// providerModels is a curated catalog of model IDs per provider prefix for the
// model-selection menu. It is advisory: a provider may support models not
// listed here, and the UI always offers a free-text entry.
var providerModels = map[string][]string{
	"openai":     {"gpt-5", "gpt-5-mini", "gpt-4.1", "gpt-4.1-mini"},
	"deepseek":   {"deepseek-chat", "deepseek-reasoner"},
	"openrouter": {"openai/gpt-4.1", "anthropic/claude-3.5-sonnet", "meta-llama/llama-3.3-70b-instruct"},
	"groq":       {"llama-3.3-70b-versatile", "llama-3.1-8b-instant"},
	"hyper": {
		"prism", "prism-flash", "prism-core",
		"deepseek-v4.1-flash-1017", "deepseek-v4-pro-0813",
		"kimi-k3.1-pro", "kimi-k3", "glm-5.3", "glm-5.3-flash",
		"minimax-m3", "inkling", "qwen3.8-max", "qwen3.8-flash",
		"qwen3.8-27b", "qwen3.8-2.4t-a95b", "gpt-oss-120b",
		"gemma-4-26b-a4b-it", "muse-spark-open",
	},
}

// modelsForProvider returns the curated model IDs for a provider prefix, in a
// stable order. It returns nil for an unknown provider.
func modelsForProvider(provider string) []string {
	return providerModels[provider]
}

// ModelsForProvider returns the curated model catalog for a provider prefix as
// a fresh slice, for the model-selection menu.
func (s *Service) ModelsForProvider(provider string) []string {
	return append([]string(nil), modelsForProvider(provider)...)
}

// ModelSelection is the operator's choice of one big and one small model. An
// empty field means "use the profile/default model".
type ModelSelection struct {
	Big   string
	Small string
}

// modelSelectionPathFor names the model-selection file beside the state file.
func modelSelectionPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "models.json")
}

func loadModelSelection(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Big   string `json:"big,omitempty"`
		Small string `json:"small,omitempty"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("read model selection: %w", err)
	}
	selection := map[string]string{}
	if raw.Big != "" {
		selection[ModelClassBig] = raw.Big
	}
	if raw.Small != "" {
		selection[ModelClassSmall] = raw.Small
	}
	return selection, nil
}

func saveModelSelection(path string, selection map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Big   string `json:"big,omitempty"`
		Small string `json:"small,omitempty"`
	}{
		Big:   selection[ModelClassBig],
		Small: selection[ModelClassSmall],
	})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".models-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncStateFile(path, name)
}

// SetModel pins a model class ("big" or "small") to a provider/model selector.
// An empty model clears the selection for that class, reverting to the
// profile/default model.
func (s *Service) SetModel(class, model string) error {
	class = strings.TrimSpace(class)
	model = strings.TrimSpace(model)
	if class != ModelClassBig && class != ModelClassSmall {
		return fmt.Errorf("model class must be %q or %q", ModelClassBig, ModelClassSmall)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	if model != "" {
		if err := validateModelSelector(model); err != nil {
			return err
		}
		s.modelSelection[class] = model
	} else {
		delete(s.modelSelection, class)
	}
	if err := saveModelSelection(s.modelSelectionPath, s.modelSelection); err != nil {
		return err
	}
	s.applyModelSelectionLocked()
	return nil
}

// ModelSelection returns the operator's chosen big and small models (empty means
// "use the profile/default").
func (s *Service) ModelSelection() ModelSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ModelSelection{
		Big:   s.modelSelection[ModelClassBig],
		Small: s.modelSelection[ModelClassSmall],
	}
}

// applyModelSelectionLocked writes the class selections onto the Fantasy
// runners. The caller holds s.mu.
func (s *Service) applyModelSelectionLocked() {
	for name, r := range s.runners {
		fr, ok := r.(FantasyRunner)
		if !ok {
			continue
		}
		fr.modelOverride = s.modelSelection[fr.modelClass]
		s.runners[name] = fr
	}
}

// applyModelSelection re-applies stored selections after runner (re)configuration.
func (s *Service) applyModelSelection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyModelSelectionLocked()
}

// validateModelSelector reports whether a model is a well-formed provider/model
// selector naming a known provider.
func validateModelSelector(model string) error {
	provider, _, ok := strings.Cut(model, "/")
	if !ok || provider == "" {
		return fmt.Errorf("model must be provider/model, got %q", model)
	}
	if _, known := fantasyEndpoints[provider]; !known {
		return fmt.Errorf("unknown provider %q (supported: %s)", provider, supportedFantasyProviders())
	}
	return nil
}

// modelClassFor returns the class an agent belongs to, for the model menu and
// resolution. Unclassified agents default to the big model.
var modelClassFor = map[string]string{
	DefaultRouteAgent: ModelClassBig,
	"debugger":        ModelClassBig,
	"explorer":        ModelClassSmall,
	"implementer":     ModelClassBig,
	"gitter":          ModelClassBig,
	"reviewer":        ModelClassSmall,
	"tester":          ModelClassSmall,
}

// AgentModelClasses returns the built-in agents and their model class, ordered
// by name, for the model menu.
func AgentModelClasses() []struct {
	Agent string
	Class string
} {
	names := make([]string, 0, len(modelClassFor))
	for name := range modelClassFor {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]struct {
		Agent string
		Class string
	}, 0, len(names))
	for _, name := range names {
		result = append(result, struct {
			Agent string
			Class string
		}{Agent: name, Class: modelClassFor[name]})
	}
	return result
}
