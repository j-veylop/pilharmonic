package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/openaicompat"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// ModelFactory resolves a model selector such as "openai/gpt-4.1-mini" to a
// fantasy.LanguageModel. Tests inject a factory so the adapter never needs
// credentials or a network connection.
type ModelFactory func(ctx context.Context, modelID string) (fantasy.LanguageModel, error)

// bedrockProvider is the provider prefix for AWS Bedrock. Unlike the other
// prefixes it speaks the Bedrock API rather than an OpenAI-compatible one, and
// it authenticates through AWS rather than a single API key.
const bedrockProvider = "bedrock"

// bedrockDefaultRegion is the region Bedrock uses when neither the AWS
// environment nor the shared AWS config names one. It matches the default the
// Bedrock client applies to bearer-token auth on its own; resolving it here
// keeps the documented behavior independent of that detail.
const bedrockDefaultRegion = "us-east-1"

// awsSharedFileLimit bounds how much of a shared AWS config or credentials file
// is read while deciding whether Bedrock looks configured.
const awsSharedFileLimit = 64 << 10

// fantasyEndpoint names the OpenAI-compatible base URL and the environment
// variable holding the credential for one provider prefix. Credentials are read
// from the local environment only; they are never written to task state. A
// provider with no baseURL is built by newFantasyProvider instead of the
// OpenAI-compatible client.
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
	// Bedrock needs no key when the AWS credential chain supplies one; the
	// environment variable names the bearer token that chain can carry.
	bedrockProvider: {envVar: "AWS_BEARER_TOKEN_BEDROCK"},
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

// DefaultFantasyModelFactory builds the provider for the prefix of modelID and
// asks it for the named model, reading the credential from the local
// environment only. The service injects a factory that also consults stored
// credentials.
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
// returns the API key (or, for Bedrock, the bearer token) for a provider
// prefix; an empty value may still be usable for Bedrock through the AWS
// credential chain.
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
	if key == "" && providerName != bedrockProvider {
		return nil, fmt.Errorf("fantasy provider %q requires an API key (set %s or use /login %s)", providerName, endpoint.envVar, providerName)
	}
	provider, err := newFantasyProvider(ctx, providerName, endpoint, key)
	if err != nil {
		return nil, err
	}
	languageModel, err := provider.LanguageModel(ctx, strings.TrimSpace(model))
	if err != nil {
		return nil, fmt.Errorf("fantasy model %q: %w", modelID, err)
	}
	return languageModel, nil
}

// newFantasyProvider builds the fantasy provider for a prefix. Bedrock uses its
// AWS-native provider; every other prefix is an OpenAI-compatible endpoint.
func newFantasyProvider(ctx context.Context, providerName string, endpoint fantasyEndpoint, key string) (fantasy.Provider, error) {
	if providerName == bedrockProvider {
		return newBedrockProvider(ctx, key)
	}
	provider, err := openaicompat.New(openaicompat.WithAPIKey(key), openaicompat.WithBaseURL(endpoint.baseURL))
	if err != nil {
		return nil, fmt.Errorf("fantasy provider %q: %w", providerName, err)
	}
	return provider, nil
}

// newBedrockProvider builds the Bedrock provider. A stored or exported bearer
// token is used as-is; without one the AWS SDK resolves credentials from its
// default chain (environment, shared config and profiles, SSO, or an attached
// instance role), so no key has to be stored for a machine that already has AWS
// credentials.
func newBedrockProvider(ctx context.Context, bearerToken string) (fantasy.Provider, error) {
	options := []bedrock.Option{bedrock.WithRegion(bedrockRegion(ctx))}
	if bearerToken != "" {
		options = append(options, bedrock.WithAPIKey(bearerToken))
	}
	provider, err := bedrock.New(options...)
	if err != nil {
		return nil, fmt.Errorf("fantasy provider %q: %w", bedrockProvider, err)
	}
	return provider, nil
}

// bedrockRegion resolves the AWS region for Bedrock from the AWS environment
// variables, then from the shared AWS config, so a region set by profile or
// config file is not silently replaced by the default. Resolving the region
// reads local files only; it never contacts a credential endpoint.
func bedrockRegion(ctx context.Context) string {
	for _, name := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if region := strings.TrimSpace(os.Getenv(name)); region != "" {
			return region
		}
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err == nil {
		if region := strings.TrimSpace(cfg.Region); region != "" {
			return region
		}
	}
	return bedrockDefaultRegion
}

// bedrockAWSConfigured reports whether Bedrock can likely authenticate without
// a stored bearer token, using only sources the AWS SDK reads without a
// network probe (instance metadata is not probed). It only gates the login
// prompt; model resolution still defers to the AWS SDK.
func bedrockAWSConfigured() bool {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return true
	}
	for _, name := range []string{
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	for _, name := range []string{"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE"} {
		if path := strings.TrimSpace(os.Getenv(name)); path != "" && awsFileHasCredentials(path) {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return awsFileHasCredentials(filepath.Join(home, ".aws", "credentials")) ||
		awsFileHasCredentials(filepath.Join(home, ".aws", "config"))
}

// awsFileHasCredentials reports whether a shared AWS credentials or config file
// carries something the SDK can authenticate with: static keys, a credential
// process, SSO settings, or a web-identity token. Profiles that hold only a
// region do not count, so their presence alone cannot hide the login prompt.
func awsFileHasCredentials(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	b, err := io.ReadAll(io.LimitReader(file, awsSharedFileLimit))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "aws_access_key_id", "aws_secret_access_key", "credential_process",
			"sso_session", "sso_start_url", "web_identity_token_file":
			return true
		}
	}
	return false
}
