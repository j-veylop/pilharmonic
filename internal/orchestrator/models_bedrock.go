package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsbedrock "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/smithy-go/auth/bearer"
)

// bedrockListTimeout bounds one Bedrock catalog listing, which can need several
// pages of inference profiles.
const bedrockListTimeout = 30 * time.Second

// bedrockModelLister fetches the model and inference-profile IDs the account
// can call on Bedrock. Tests replace it so no AWS call is made.
var bedrockModelLister = listBedrockModels

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
