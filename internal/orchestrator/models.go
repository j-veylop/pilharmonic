package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"

	"pilharmonic/internal/instance"
	"pilharmonic/internal/providers"
)

// Model class labels: the operator's "big" (planning, heavy changes) and
// "small" (fast, focused) models. A profile or specialist model may name a
// class instead of a selector.
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
	// Bedrock selects a model by foundation-model or inference-profile ID, and
	// nothing else: the identifiers below are cross-region inference profiles,
	// so the menu offers IDs that work across regions rather than bare model
	// names. Any other ID, including an inference-profile ARN, can be typed.
	"bedrock": {
		"us.anthropic.claude-sonnet-4-6-v1:0",
		"us.anthropic.claude-haiku-4-5-v1:0",
		"us.anthropic.claude-opus-4-8-v1:0",
		"global.anthropic.claude-sonnet-4-6-v1:0",
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

// modelConfig is the on-disk shape of models.json. Agents holds the retired
// per-agent classes and is read only to warn about them.
type modelConfig struct {
	Big    string            `json:"big,omitempty"`
	Small  string            `json:"small,omitempty"`
	Agents map[string]string `json:"agents,omitempty"`
}

// modelSelectionPathFor names the model-selection file beside the state file.
func modelSelectionPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "models.json")
}

// loadModelConfig returns the class selection and whether the file still
// carries per-agent classes.
func loadModelConfig(path string) (map[string]string, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var raw modelConfig
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false, fmt.Errorf("read model config: %w", err)
	}
	selection := map[string]string{}
	if raw.Big != "" {
		selection[ModelClassBig] = raw.Big
	}
	if raw.Small != "" {
		selection[ModelClassSmall] = raw.Small
	}
	return selection, len(raw.Agents) > 0, nil
}

func saveModelConfig(path string, selection map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(modelConfig{
		Big:   selection[ModelClassBig],
		Small: selection[ModelClassSmall],
	})
	if err != nil {
		return err
	}
	return instance.WriteFileAtomic(path, ".models-*", b)
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
	if model != "" {
		if err := validateModelSelector(model); err != nil {
			return err
		}
	}
	return s.whileOpen(func() error {
		if model != "" {
			s.modelSelection[class] = model
		} else {
			delete(s.modelSelection, class)
		}
		return s.saveModelConfigLocked()
	})
}

// saveModelConfigLocked persists models.json. Caller holds mu.
func (s *Service) saveModelConfigLocked() error {
	return saveModelConfig(s.modelSelectionPath, s.modelSelection)
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

// fantasyModelLocked resolves the selector a Fantasy attempt runs with: the
// task profile model, the agent's class model (models.json), the specialist
// model (profiles.json), then -model. A "big" or "small" alias stands for that
// class's model and resolves to nothing while the class is unset, so an alias
// never reaches a provider. Caller holds mu.
func (s *Service) fantasyModelLocked(agent string, profile *Profile) string {
	resolve := func(model string) string {
		if model == ModelClassBig || model == ModelClassSmall {
			return s.modelSelection[model]
		}
		return model
	}
	class, specialist := modelClassFor[agent], s.agentModels[agent]
	if specialist == ModelClassBig || specialist == ModelClassSmall {
		class = specialist
	}
	var profileModel string
	if profile != nil {
		profileModel = profile.Model
	}
	for _, model := range []string{resolve(profileModel), s.modelSelection[class], resolve(specialist), resolve(s.defaultModel)} {
		if model != "" {
			return model
		}
	}
	return ""
}

// validateModelSelector reports whether a model is a well-formed provider/model
// selector naming a known provider.
func validateModelSelector(model string) error {
	provider, _, ok := strings.Cut(model, "/")
	if !ok || provider == "" {
		return fmt.Errorf("model must be provider/model, got %q", model)
	}
	if !providers.Known(provider) {
		return fmt.Errorf("unknown provider %q (supported: %s)", provider, strings.Join(providers.Names(), ", "))
	}
	return nil
}

// modelClassFor returns the default class an agent belongs to. Only the
// coordinator (general) is big; every specialist is small unless its
// profiles.json entry names the big class.
var modelClassFor = map[string]string{
	DefaultRouteAgent: ModelClassBig,
	"debugger":        ModelClassSmall,
	"explorer":        ModelClassSmall,
	"implementer":     ModelClassSmall,
	"gitter":          ModelClassSmall,
	"reviewer":        ModelClassSmall,
	"tester":          ModelClassSmall,
}

// ModelFactory resolves a model selector such as "openai/gpt-4.1-mini" to a
// fantasy.LanguageModel. Tests inject a factory so the adapter never needs
// credentials or a network connection.
type ModelFactory func(ctx context.Context, modelID string) (fantasy.LanguageModel, error)

// DefaultFantasyModelFactory resolves a model with the credential from the
// local environment only. The service injects a factory that also consults
// stored credentials.
func DefaultFantasyModelFactory(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	return providers.Resolve(ctx, modelID, providers.EnvCredential)
}

// fantasyModelFactory resolves a model using the stored provider credential
// first, falling back to the environment variable. The operator's key is never
// written to task state.
func (s *Service) fantasyModelFactory(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	return providers.Resolve(ctx, modelID, s.credentialFor)
}

// credentialFor returns the stored API key for a provider, falling back to the
// environment variable. It never returns anything but the credential.
func (s *Service) credentialFor(provider string) string {
	s.mu.Lock()
	key := s.creds[provider]
	s.mu.Unlock()
	if key != "" {
		return key
	}
	return providers.EnvCredential(provider)
}

// modelListTTL bounds how long a fetched model list is reused before the next
// menu open refreshes it. Providers change their catalogs slowly, so a short
// TTL keeps the menu fresh without hammering the endpoint.
const modelListTTL = 10 * time.Minute

// modelListEntry is one cached provider catalog.
type modelListEntry struct {
	models    []string
	fetchedAt time.Time
}

// modelListCache memoizes fetched provider catalogs in memory for the process
// lifetime.
type modelListCache struct {
	mu    sync.Mutex
	lists map[string]modelListEntry
}

// FetchModels returns the models a provider currently offers, fetched from the
// provider. Results are cached for modelListTTL; the
// curated catalog in providerModels is only the fallback the UI uses, and says
// so, when the fetch fails, so an unreachable endpoint never blocks model
// selection. Bedrock is listed through the AWS control plane instead.
func (s *Service) FetchModels(ctx context.Context, provider string) ([]string, error) {
	provider = strings.TrimSpace(provider)
	if !providers.Known(provider) {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}

	s.modelCache.mu.Lock()
	if entry, ok := s.modelCache.lists[provider]; ok && time.Since(entry.fetchedAt) < modelListTTL {
		models := append([]string(nil), entry.models...)
		s.modelCache.mu.Unlock()
		return models, nil
	}
	s.modelCache.mu.Unlock()

	models, err := s.listModels(ctx, provider, s.credentialFor(provider))
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider %q returned no models", provider)
	}

	s.modelCache.mu.Lock()
	if s.modelCache.lists == nil {
		s.modelCache.lists = make(map[string]modelListEntry)
	}
	s.modelCache.lists[provider] = modelListEntry{models: models, fetchedAt: time.Now()}
	s.modelCache.mu.Unlock()
	return append([]string(nil), models...), nil
}
