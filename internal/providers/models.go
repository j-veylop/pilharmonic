package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsbedrock "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/smithy-go/auth/bearer"
)

// ListModels returns the models a provider currently offers: its
// OpenAI-compatible /models endpoint, or for Bedrock the AWS control plane.
// key is the provider credential; Bedrock without one uses the AWS chain.
func ListModels(ctx context.Context, provider, key string) ([]string, error) {
	if provider == Subscription {
		return listSubscriptionModels(ctx)
	}
	endpoint, known := endpoints[provider]
	if !known {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
	if provider == Bedrock {
		return listBedrockModels(ctx, key)
	}
	return fetchOpenAICompatibleModels(ctx, provider, endpoint, key)
}

// fetchOpenAICompatibleModels reads a provider's /models endpoint and returns
// the sorted, de-duplicated model IDs.
func fetchOpenAICompatibleModels(ctx context.Context, provider string, endpoint endpoint, key string) ([]string, error) {
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

// bedrockListTimeout bounds one Bedrock catalog listing, which can need several
// pages of inference profiles.
const bedrockListTimeout = 30 * time.Second

// listBedrockModels asks the Bedrock control plane which inference profiles and
// on-demand foundation models are available in the resolved region. A stored
// bearer token is used as-is; otherwise the AWS default credential chain signs
// the request, matching how the runner authenticates.
func listBedrockModels(ctx context.Context, bearerToken string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, bedrockListTimeout)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(bedrockRegion(ctx)))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := awsbedrock.NewFromConfig(cfg, func(options *awsbedrock.Options) {
		if bearerToken != "" {
			options.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: bearerToken}}
			options.AuthSchemePreference = []string{"httpBearerAuth"}
		}
	})

	seen := map[string]bool{}
	var models []string
	add := func(id *string) {
		if id == nil {
			return
		}
		value := strings.TrimSpace(*id)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		models = append(models, value)
	}

	profiles := awsbedrock.NewListInferenceProfilesPaginator(client, &awsbedrock.ListInferenceProfilesInput{
		TypeEquals: bedrocktypes.InferenceProfileTypeSystemDefined,
	})
	for profiles.HasMorePages() {
		page, err := profiles.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list bedrock inference profiles: %w", err)
		}
		for _, profile := range page.InferenceProfileSummaries {
			if profile.Status == bedrocktypes.InferenceProfileStatusActive {
				add(profile.InferenceProfileId)
			}
		}
	}

	foundation, err := client.ListFoundationModels(ctx, &awsbedrock.ListFoundationModelsInput{
		ByOutputModality: bedrocktypes.ModelModalityText,
		ByInferenceType:  bedrocktypes.InferenceTypeOnDemand,
	})
	if err != nil {
		return nil, fmt.Errorf("list bedrock foundation models: %w", err)
	}
	for _, summary := range foundation.ModelSummaries {
		add(summary.ModelId)
	}

	sort.Strings(models)
	return models, nil
}
