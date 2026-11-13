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
	return writeFileAtomic(path, ".credentials-*", b)
}

// SetProviderCredential stores an API key for a provider prefix, replacing any
// previous key. The provider must be one the built-in adapter understands.
func (s *Service) SetProviderCredential(provider, key string) error {
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if _, known := fantasyEndpoints[provider]; !known {
		return fmt.Errorf("unknown provider %q (supported: %s)", provider, supportedFantasyProviders())
	}
	if key == "" {
		if provider == bedrockProvider {
			return errors.New("bearer token is required (Bedrock without a stored key uses the AWS credential chain)")
		}
		return errors.New("API key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.creds[provider] = key
	return saveCredentials(s.credentialsPath, s.creds)
}

// RemoveProviderCredential forgets a provider's stored API key.
func (s *Service) RemoveProviderCredential(provider string) error {
	provider = strings.TrimSpace(provider)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	delete(s.creds, provider)
	return saveCredentials(s.credentialsPath, s.creds)
}

// ProviderCredentials lists every known provider in a stable order, marking
// which are configured by a stored key or by the provider's environment
// variable. Keys are never returned. The AWS probe runs before the lock is
// taken so no file read happens while the service mutex is held.
func (s *Service) ProviderCredentials() []ProviderCredential {
	bedrockConfigured := bedrockAWSConfigured()
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(fantasyEndpoints))
	for name := range fantasyEndpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]ProviderCredential, 0, len(names))
	for _, name := range names {
		result = append(result, ProviderCredential{Provider: name, Configured: s.providerConfiguredLocked(name, bedrockConfigured)})
	}
	return result
}

// AnyProviderConfigured reports whether at least one provider can be used right
// now, either through a stored key or an environment variable. The terminal
// forces the login menu while this is false.
func (s *Service) AnyProviderConfigured() bool {
	bedrockConfigured := bedrockAWSConfigured()
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range fantasyEndpoints {
		if s.providerConfiguredLocked(name, bedrockConfigured) {
			return true
		}
	}
	return false
}

// providerConfiguredLocked applies the one definition of "configured" used by
// both listing and the forced-login check: a stored key, or a non-empty
// environment variable for that provider. Bedrock also counts the AWS
// credential chain, because it needs no stored key on a machine that is already
// authenticated to AWS; bedrockConfigured carries that result so this function
// does no I/O. The caller holds s.mu.
func (s *Service) providerConfiguredLocked(name string, bedrockConfigured bool) bool {
	if s.creds[name] != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv(fantasyEndpoints[name].envVar)) != "" {
		return true
	}
	return name == bedrockProvider && bedrockConfigured
}
