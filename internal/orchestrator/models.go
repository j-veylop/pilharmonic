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

// AgentClass reports the class an agent runs on and whether the operator chose
// it explicitly (overriding the default).
type AgentClass struct {
	Agent      string
	Class      string
	Overridden bool
}

// modelConfig is the on-disk shape of models.json: the big/small model
// selectors plus per-agent class overrides.
type modelConfig struct {
	Big    string            `json:"big,omitempty"`
	Small  string            `json:"small,omitempty"`
	Agents map[string]string `json:"agents,omitempty"`
}

// modelSelectionPathFor names the model-selection file beside the state file.
func modelSelectionPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "models.json")
}

func loadModelConfig(path string) (map[string]string, map[string]string, error) {
	empty := map[string]string{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, empty, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var raw modelConfig
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, nil, fmt.Errorf("read model config: %w", err)
	}
	selection := map[string]string{}
	if raw.Big != "" {
		selection[ModelClassBig] = raw.Big
	}
	if raw.Small != "" {
		selection[ModelClassSmall] = raw.Small
	}
	if raw.Agents == nil {
		raw.Agents = map[string]string{}
	}
	return selection, raw.Agents, nil
}

func saveModelConfig(path string, selection, agents map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(modelConfig{
		Big:    selection[ModelClassBig],
		Small:  selection[ModelClassSmall],
		Agents: agents,
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
	if err := saveModelConfig(s.modelSelectionPath, s.modelSelection, s.agentClasses); err != nil {
		return err
	}
	s.applyModelSelectionLocked()
	return nil
}

// SetAgentClass pins an agent to a model class ("big" or "small"), overriding
// the default. An empty class reverts the agent to its default.
func (s *Service) SetAgentClass(agent, class string) error {
	agent = strings.TrimSpace(agent)
	class = strings.TrimSpace(class)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	if _, ok := s.runners[agent].(FantasyRunner); !ok {
		return fmt.Errorf("%q is not a known agent", agent)
	}
	if class != "" && class != ModelClassBig && class != ModelClassSmall {
		return fmt.Errorf("model class must be %q or %q", ModelClassBig, ModelClassSmall)
	}
	if class == "" {
		delete(s.agentClasses, agent)
	} else {
		s.agentClasses[agent] = class
	}
	if err := saveModelConfig(s.modelSelectionPath, s.modelSelection, s.agentClasses); err != nil {
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

// AgentClasses returns every Fantasy agent, its effective class, and whether the
// operator overrode the default, ordered by name.
func (s *Service) AgentClasses() []AgentClass {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(modelClassFor))
	for name := range modelClassFor {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]AgentClass, 0, len(names))
	for _, name := range names {
		class, overridden := s.agentClasses[name]
		if !overridden {
			class = modelClassFor[name]
		}
		result = append(result, AgentClass{Agent: name, Class: class, Overridden: overridden})
	}
	return result
}

// applyModelSelectionLocked resolves each agent's effective class and writes
// the corresponding model override onto the Fantasy runners. The caller holds
// s.mu.
func (s *Service) applyModelSelectionLocked() {
	for name, r := range s.runners {
		fr, ok := r.(FantasyRunner)
		if !ok {
			continue
		}
		class := s.agentClasses[name]
		if class == "" {
			class = modelClassFor[name]
		}
		fr.modelClass = class
		fr.modelOverride = s.modelSelection[class]
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

// modelClassFor returns the default class an agent belongs to. Only the
// coordinator (general) is big by default; every specialist is small unless the
// operator overrides it.
var modelClassFor = map[string]string{
	DefaultRouteAgent: ModelClassBig,
	"debugger":        ModelClassSmall,
	"explorer":        ModelClassSmall,
	"implementer":     ModelClassSmall,
	"gitter":          ModelClassSmall,
	"reviewer":        ModelClassSmall,
	"tester":          ModelClassSmall,
}
