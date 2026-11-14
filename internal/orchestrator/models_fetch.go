package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

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

// FetchModels returns the models a provider currently offers, fetched from its
// OpenAI-compatible /models endpoint. Results are cached for modelListTTL; the
// curated catalog in providerModels is only the fallback the UI uses, and says
// so, when the fetch fails, so an unreachable endpoint never blocks model
// selection. Bedrock is listed through the AWS control plane instead.
func (s *Service) FetchModels(ctx context.Context, provider string) ([]string, error) {
	provider = strings.TrimSpace(provider)
	endpoint, known := fantasyEndpoints[provider]
	if !known {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}

	s.modelCache.mu.Lock()
	if entry, ok := s.modelCache.lists[provider]; ok && time.Since(entry.fetchedAt) < modelListTTL {
		models := append([]string(nil), entry.models...)
		s.modelCache.mu.Unlock()
		return models, nil
	}
	s.modelCache.mu.Unlock()

	var models []string
	var err error
	if provider == bedrockProvider {
		models, err = bedrockModelLister(ctx, s.credentialFor(provider))
	} else {
		models, err = fetchOpenAICompatibleModels(ctx, provider, endpoint, s.credentialFor(provider))
	}
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

// fetchOpenAICompatibleModels reads a provider's /models endpoint and returns
// the sorted, de-duplicated model IDs.
func fetchOpenAICompatibleModels(ctx context.Context, provider string, endpoint fantasyEndpoint, key string) ([]string, error) {
	if key == "" {
		return nil, fmt.Errorf("provider %q requires an API key", provider)
	}
	url := strings.TrimRight(endpoint.baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider %q: models endpoint returned %s", provider, resp.Status)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	models := make([]string, 0, len(body.Data))
	seen := make(map[string]bool, len(body.Data))
	for _, m := range body.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
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
	return strings.TrimSpace(os.Getenv(fantasyEndpoints[provider].envVar))
}
