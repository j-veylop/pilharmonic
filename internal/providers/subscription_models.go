package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const subscriptionModelsClientVersion = "999.0.0"

func listSubscriptionModels(ctx context.Context) ([]string, error) {
	home, err := subscriptionHome()
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return fetchSubscriptionModels(ctx, filepath.Join(home, "auth.json"), subscriptionBaseURL, subscriptionTokenURL, &http.Client{})
}

func fetchSubscriptionModels(ctx context.Context, authPath, baseURL, tokenURL string, client *http.Client) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	secureClient := *client
	secureClient.Timeout = 30 * time.Second
	secureClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tokens, err := subscriptionCredential(ctx, authPath, tokenURL, &secureClient, "")
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models?client_version="+subscriptionModelsClientVersion, nil)
		if err != nil {
			return nil, subscriptionError(ctx, err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		req.Header.Set("ChatGPT-Account-Id", tokens.AccountID)
		req.Header.Set("originator", "crush")
		req.Header.Set("Accept", "application/json")
		resp, err := secureClient.Do(req)
		if err != nil {
			return nil, subscriptionError(ctx, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = resp.Body.Close()
			tokens, err = subscriptionCredential(ctx, authPath, tokenURL, &secureClient, tokens.AccessToken)
			if err != nil {
				return nil, subscriptionError(ctx, err)
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ChatGPT subscription models endpoint returned HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil {
			return nil, subscriptionError(ctx, err)
		}
		var body struct {
			Models *[]struct {
				Slug       string `json:"slug"`
				Visibility string `json:"visibility"`
			} `json:"models"`
		}
		if len(data) > 1<<20 || json.Unmarshal(data, &body) != nil || body.Models == nil {
			return nil, fmt.Errorf("invalid ChatGPT subscription models response")
		}
		models := make([]string, 0, len(*body.Models))
		seen := make(map[string]bool)
		for _, model := range *body.Models {
			id := strings.TrimSpace(model.Slug)
			if model.Visibility != "list" || id == "" || seen[id] {
				continue
			}
			seen[id] = true
			models = append(models, id)
		}
		if len(models) == 0 {
			return nil, fmt.Errorf("the Codex model catalog was empty")
		}
		sort.Strings(models)
		return models, nil
	}
	return nil, fmt.Errorf("ChatGPT subscription models authentication failed")
}
