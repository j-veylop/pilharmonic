package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// awsEnvNames lists the environment variables that can make Bedrock look
// configured. Tests clear them so a developer machine that is logged in to AWS
// does not change the outcome.
var awsEnvNames = []string{
	"AWS_BEARER_TOKEN_BEDROCK",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_PROFILE",
	"AWS_DEFAULT_PROFILE",
	"AWS_ROLE_ARN",
	"AWS_WEB_IDENTITY_TOKEN_FILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_SHARED_CREDENTIALS_FILE",
	"AWS_CONFIG_FILE",
	"AWS_REGION",
	"AWS_DEFAULT_REGION",
	"AWS_EC2_METADATA_DISABLED",
}

// isolateProviderEnvironment clears every credential source a provider can read
// so "configured" only reflects what a test sets. HOME and USERPROFILE point at
// an empty directory because a real ~/.aws would otherwise count as configured,
// and instance metadata is disabled so no test can reach it.
func isolateProviderEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OPENAI_API_KEY", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "HYPER_API_KEY"} {
		t.Setenv(name, "")
	}
	for _, name := range awsEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestBedrockConfiguredFromAWSSources checks that Bedrock counts as configured
// without a stored key when the AWS SDK has a credential source (environment,
// ~/.aws files or their path overrides), and that a lone or incomplete signal
// does not hide the login. Values starting with "~/" name files under HOME.
func TestBedrockConfiguredFromAWSSources(t *testing.T) {
	const keys = "[default]\naws_access_key_id = AKIAIOSFODNN7EXAMPLE\naws_secret_access_key = secret\n"
	for _, tc := range []struct {
		name  string
		env   map[string]string
		files map[string]string
		want  bool
	}{
		{"nothing", nil, nil, false},
		{"bearer token", map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "bedrock-bearer"}, nil, true},
		{"static key pair", map[string]string{"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY": "secret"}, nil, true},
		{"container credentials", map[string]string{"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/token"}, nil, true},
		{"web identity", map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/token"}, nil, true},
		{"key without a secret", map[string]string{"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE"}, nil, false},
		{"profile alone", map[string]string{"AWS_PROFILE": "work"}, nil, false},
		{"role alone", map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/work"}, nil, false},
		{"credentials file", nil, map[string]string{".aws/credentials": keys}, true},
		{"config with an SSO session", nil, map[string]string{".aws/config": "[profile work]\nsso_session = work\nsso_account_id = 123456789012\n"}, true},
		{"config with only a region", nil, map[string]string{".aws/config": "[default]\nregion = eu-central-1\n"}, false},
		{"credentials with empty keys", nil, map[string]string{".aws/credentials": "[default]\naws_access_key_id = \naws_secret_access_key = \n"}, false},
		{"credentials path override", map[string]string{"AWS_SHARED_CREDENTIALS_FILE": "~/custom/credentials"}, map[string]string{"custom/credentials": keys}, true},
		{"missing credentials path", map[string]string{"AWS_SHARED_CREDENTIALS_FILE": "~/missing"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateProviderEnvironment(t)
			home := os.Getenv("HOME")
			for rel, content := range tc.files {
				writeAWSFile(t, home, rel, content)
			}
			for key, value := range tc.env {
				if rest, ok := strings.CutPrefix(value, "~/"); ok {
					value = filepath.Join(home, rest)
				}
				t.Setenv(key, value)
			}
			s := newTestService(t)
			bedrock := false
			for _, c := range s.ProviderCredentials() {
				if c.Provider == "bedrock" {
					bedrock = c.Configured
				}
			}
			if bedrock != tc.want || s.AnyProviderConfigured() != tc.want {
				t.Fatalf("bedrock configured = %v, any provider = %v, want %v", bedrock, s.AnyProviderConfigured(), tc.want)
			}
		})
	}
}

// writeAWSFile writes a shared AWS file and returns its path.
func writeAWSFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// TestBedrockBearerTokenResolvesModel checks the stored-token path: /login
// bedrock stores a bearer token and the factory resolves a Bedrock model
// without contacting AWS.
func TestBedrockBearerTokenResolvesModel(t *testing.T) {
	isolateProviderEnvironment(t)
	s := newTestService(t)

	if err := s.SetProviderCredential("bedrock", ""); err == nil {
		t.Fatal("empty bearer token accepted")
	} else if !strings.Contains(err.Error(), "bearer token") {
		t.Fatalf("unexpected error for an empty bearer token: %v", err)
	}

	must(t, s.SetProviderCredential("bedrock", "bedrock-bearer-token"))
	for _, modelID := range []string{
		"bedrock/us.anthropic.claude-sonnet-4-6-v1:0",
		// An inference-profile ARN carries its own slashes; only the prefix
		// separator may be consumed.
		"bedrock/arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-sonnet-4-6-v1:0",
	} {
		if err := validateModelSelector(modelID); err != nil {
			t.Fatalf("selector %q rejected: %v", modelID, err)
		}
		model, err := s.fantasyModelFactory(t.Context(), modelID)
		if err != nil {
			t.Fatalf("bedrock provider not resolved for %q: %v", modelID, err)
		}
		want := strings.TrimPrefix(modelID, "bedrock/")
		if got := model.Model(); got != want {
			t.Fatalf("model %q resolved as %q", modelID, got)
		}
	}
}

// TestBedrockLogoutKeepsAWSChainConfigured documents what /logout bedrock does
// on a machine that authenticates through AWS: the stored token goes away but
// the provider still counts as usable.
func TestBedrockLogoutKeepsAWSChainConfigured(t *testing.T) {
	isolateProviderEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	s := newTestService(t)

	must(t, s.SetProviderCredential("bedrock", "bedrock-bearer-token"))
	must(t, s.RemoveProviderCredential("bedrock"))
	if !s.AnyProviderConfigured() {
		t.Fatal("AWS credential chain stopped configuring bedrock after logout")
	}
}

// TestBedrockModelListingUsesAWS checks that Bedrock models are fetched from the
// account with the stored token rather than read from the built-in catalog,
// and that a failed listing is reported so the menu can fall back honestly.
func TestBedrockModelListingUsesAWS(t *testing.T) {
	isolateProviderEnvironment(t)
	s := newTestService(t)
	var gotProvider, gotToken string
	s.listModels = func(_ context.Context, provider, token string) ([]string, error) {
		gotProvider, gotToken = provider, token
		return []string{"us.anthropic.live-model-v1:0"}, nil
	}
	must(t, s.SetProviderCredential("bedrock", "bearer-secret"))

	models := mustGet(s.FetchModels(t.Context(), "bedrock"))(t)
	if len(models) != 1 || models[0] != "us.anthropic.live-model-v1:0" || gotProvider != "bedrock" || gotToken != "bearer-secret" {
		t.Fatalf("unexpected listing: models=%v provider=%q token=%q", models, gotProvider, gotToken)
	}

	delete(s.modelCache.lists, "bedrock")
	s.listModels = func(context.Context, string, string) ([]string, error) {
		return nil, errors.New("access denied")
	}
	if _, err := s.FetchModels(t.Context(), "bedrock"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("listing failure not reported: %v", err)
	}
	if len(s.ModelsForProvider("bedrock")) == 0 {
		t.Fatal("built-in bedrock fallback is empty")
	}
}
