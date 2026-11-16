package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pilharmonic/internal/instance"
	"pilharmonic/internal/providers"
)

// ProviderCredential reports whether a provider has a stored API key. The key
// itself is never exposed through this type or any list method.
type ProviderCredential struct {
	Provider   string
	Configured bool
}

// credentialsPathFor names the credentials file beside the state file, so both
// live in the same owner-only directory.
func credentialsPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "credentials.json")
}

// loadCredentials reads the provider credentials file, or an empty set when it
// does not exist yet. A corrupt file is an error so the operator can repair it
// rather than silently losing provider access.
func loadCredentials(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Providers map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	if raw.Providers == nil {
		raw.Providers = map[string]string{}
	}
	return raw.Providers, nil
}

// saveCredentials writes provider credentials atomically with owner-only
// permissions. The caller holds s.mu so the map is not mutated concurrently.
func saveCredentials(path string, providers map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Providers map[string]string `json:"providers"`
	}{Providers: providers})
	if err != nil {
		return err
	}
	return instance.WriteFileAtomic(path, ".credentials-*", b)
}

// SetProviderCredential stores an API key for a provider prefix, replacing any
// previous key. The provider must be one the built-in adapter understands.
func (s *Service) SetProviderCredential(provider, key string) error {
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if provider == "openai-subscription" {
		return errors.New("OpenAI subscriptions do not use API keys; use /login openai subscription")
	}
	if !providers.Known(provider) {
		return fmt.Errorf("unknown provider %q (supported: %s)", provider, strings.Join(providers.Names(), ", "))
	}
	if key == "" {
		if provider == providers.Bedrock {
			return errors.New("bearer token is required (Bedrock without a stored key uses the AWS credential chain)")
		}
		return errors.New("API key is required")
	}
	return s.whileOpen(func() error {
		s.creds[provider] = key
		return saveCredentials(s.credentialsPath, s.creds)
	})
}

// RemoveProviderCredential forgets a provider's stored API key.
func (s *Service) RemoveProviderCredential(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "openai-subscription" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("locate isolated OpenAI subscription credentials for logout: %w", err)
		}
		return fmt.Errorf("to remove OpenAI subscription credentials, set CODEX_HOME to %q, then run codex -c 'cli_auth_credentials_store=\"file\"' logout", filepath.Join(dir, "orchestrator", "openai-subscription"))
	}
	return s.whileOpen(func() error {
		delete(s.creds, provider)
		return saveCredentials(s.credentialsPath, s.creds)
	})
}

// ProviderCredentials lists every known provider in a stable order, marking
// which are configured by a stored key or by the provider's environment
// variable. Keys are never returned. The AWS probe runs before the lock is
// taken so no file read happens while the service mutex is held.
func (s *Service) ProviderCredentials() []ProviderCredential {
	bedrockConfigured := providers.BedrockAWSConfigured()
	subscriptionConfigured := providers.SubscriptionConfigured()
	s.mu.Lock()
	defer s.mu.Unlock()
	names := providers.Names()
	result := make([]ProviderCredential, 0, len(names))
	for _, name := range names {
		result = append(result, ProviderCredential{Provider: name, Configured: s.providerConfiguredLocked(name, bedrockConfigured, subscriptionConfigured)})
	}
	return result
}

// AnyProviderConfigured reports whether at least one provider can be used right
// now, either through a stored key or an environment variable. The terminal
// forces the login menu while this is false.
func (s *Service) AnyProviderConfigured() bool {
	bedrockConfigured := providers.BedrockAWSConfigured()
	subscriptionConfigured := providers.SubscriptionConfigured()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range providers.Names() {
		if s.providerConfiguredLocked(name, bedrockConfigured, subscriptionConfigured) {
			return true
		}
	}
	return false
}

// providerConfiguredLocked is the one definition of "configured": a stored key
// or the provider's environment variable, and for Bedrock the AWS credential
// chain, probed by the caller so this does no I/O. Caller holds mu.
func (s *Service) providerConfiguredLocked(name string, bedrockConfigured, subscriptionConfigured bool) bool {
	if name == "openai-subscription" {
		return subscriptionConfigured
	}
	if s.creds[name] != "" {
		return true
	}
	if providers.EnvCredential(name) != "" {
		return true
	}
	return name == providers.Bedrock && bedrockConfigured
}
