package orchestrator

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
)

// ModelFactory resolves a model selector such as "openai/gpt-4.1-mini" to a
// fantasy.LanguageModel. Tests inject a factory so the adapter never needs
// credentials or a network connection.
type ModelFactory func(ctx context.Context, modelID string) (fantasy.LanguageModel, error)

// fantasyEndpoint names the OpenAI-compatible base URL and the environment
// variable holding the credential for one provider prefix. Credentials are read
// from the local environment only; they are never written to task state.
type fantasyEndpoint struct {
	baseURL string
	envVar  string
}

var fantasyEndpoints = map[string]fantasyEndpoint{
	"openai":     {baseURL: "https://api.openai.com/v1", envVar: "OPENAI_API_KEY"},
	"deepseek":   {baseURL: "https://api.deepseek.com/v1", envVar: "DEEPSEEK_API_KEY"},
	"openrouter": {baseURL: "https://openrouter.ai/api/v1", envVar: "OPENROUTER_API_KEY"},
	"groq":       {baseURL: "https://api.groq.com/openai/v1", envVar: "GROQ_API_KEY"},
	"hyper":      {baseURL: "https://hyper.charm.land/api/v1/fantasy", envVar: "HYPER_API_KEY"},
}

// supportedFantasyProviders lists the provider prefixes in a stable order for
// error messages.
func supportedFantasyProviders() string {
	names := make([]string, 0, len(fantasyEndpoints))
	for name := range fantasyEndpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// DefaultFantasyModelFactory builds an OpenAI-compatible provider for the
// prefix of modelID and asks it for the named model, reading the credential
// from the local environment only. The service injects a factory that also
// consults stored credentials.
func DefaultFantasyModelFactory(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	return resolveFantasyModel(ctx, modelID, func(providerName string) string {
		return os.Getenv(fantasyEndpoints[providerName].envVar)
	})
}

// fantasyModelFactory resolves a model using the stored provider credential
// first, falling back to the environment variable. The operator's key is never
// written to task state.
func (s *Service) fantasyModelFactory(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	return resolveFantasyModel(ctx, modelID, func(providerName string) string {
		s.mu.Lock()
		key := s.creds[providerName]
		s.mu.Unlock()
		if key != "" {
			return key
		}
		return os.Getenv(fantasyEndpoints[providerName].envVar)
	})
}

// resolveFantasyModel is the shared provider-resolution path. credential
// returns the API key for a provider prefix.
func resolveFantasyModel(ctx context.Context, modelID string, credential func(string) string) (fantasy.LanguageModel, error) {
	providerName, model, ok := strings.Cut(modelID, "/")
	if !ok || providerName == "" || model == "" {
		return nil, fmt.Errorf("model must be provider/model, got %q", modelID)
	}
	endpoint, known := fantasyEndpoints[providerName]
	if !known {
		return nil, fmt.Errorf("unknown fantasy provider %q (supported: %s)", providerName, supportedFantasyProviders())
	}
	key := strings.TrimSpace(credential(providerName))
	if key == "" {
		return nil, fmt.Errorf("fantasy provider %q requires an API key (set %s or use /login %s)", providerName, endpoint.envVar, providerName)
	}
	provider, err := openaicompat.New(openaicompat.WithAPIKey(key), openaicompat.WithBaseURL(endpoint.baseURL))
	if err != nil {
		return nil, fmt.Errorf("fantasy provider %q: %w", providerName, err)
	}
	languageModel, err := provider.LanguageModel(ctx, strings.TrimSpace(model))
	if err != nil {
		return nil, fmt.Errorf("fantasy model %q: %w", modelID, err)
	}
	return languageModel, nil
}
