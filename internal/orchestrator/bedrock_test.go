package orchestrator

import (
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

// writeAWSFile writes a shared AWS file and returns its path.
func writeAWSFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBedrockConfiguredThroughAWSCredentialChain checks that Bedrock counts as
// configured without a stored key when the AWS SDK has a credential source, and
// that a lone or incomplete signal does not.
func TestBedrockConfiguredThroughAWSCredentialChain(t *testing.T) {
	isolateProviderEnvironment(t)
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	bedrock := func() bool {
		for _, c := range s.ProviderCredentials() {
			if c.Provider == "bedrock" {
				return c.Configured
			}
		}
		t.Fatal("bedrock missing from the provider list")
		return false
	}
	if s.AnyProviderConfigured() {
		t.Fatal("empty environment reported a configured provider")
	}

	configured := map[string]map[string]string{
		"AWS_BEARER_TOKEN_BEDROCK":               {"AWS_BEARER_TOKEN_BEDROCK": "bedrock-bearer"},
		"static key pair":                        {"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY": "secret"},
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": {"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/token"},
		"AWS_WEB_IDENTITY_TOKEN_FILE":            {"AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/token"},
	}
	for name, env := range configured {
		t.Run(name, func(t *testing.T) {
			for key, value := range env {
				t.Setenv(key, value)
			}
			if !bedrock() {
				t.Fatalf("%s did not configure bedrock", name)
			}
			if !s.AnyProviderConfigured() {
				t.Fatal("bedrock configured but no provider reported usable")
			}
		})
	}

	// Signals that do not authenticate on their own must not hide the login.
	incomplete := map[string]map[string]string{
		"AWS_ACCESS_KEY_ID without a secret": {"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE"},
		"AWS_PROFILE alone":                  {"AWS_PROFILE": "work"},
		"AWS_ROLE_ARN alone":                 {"AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/work"},
	}
	for name, env := range incomplete {
		t.Run(name, func(t *testing.T) {
			for key, value := range env {
				t.Setenv(key, value)
			}
			if bedrock() {
				t.Fatalf("%s wrongly reported bedrock as configured", name)
			}
			if s.AnyProviderConfigured() {
				t.Fatal("incomplete signal configured a provider")
			}
		})
	}
}

// TestBedrockSharedAWSFiles covers the ~/.aws files the AWS SDK falls back to
// when no environment variable is set: one with credential material counts, one
// that only names a region does not.
func TestBedrockSharedAWSFiles(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		want    bool
	}{
		{
			name:    "credentials with static keys",
			file:    "credentials",
			content: "[default]\naws_access_key_id = AKIAIOSFODNN7EXAMPLE\naws_secret_access_key = secret\n",
			want:    true,
		},
		{
			name:    "config with an SSO session",
			file:    "config",
			content: "[profile work]\nsso_session = work\nsso_account_id = 123456789012\n",
			want:    true,
		},
		{
			name:    "config with only a region",
			file:    "config",
			content: "[default]\nregion = eu-central-1\n",
			want:    false,
		},
		{
			name:    "empty credentials file",
			file:    "credentials",
			content: "[default]\n",
			want:    false,
		},
		{
			name:    "credentials file with an empty key",
			file:    "credentials",
			content: "[default]\naws_access_key_id = \naws_secret_access_key = \n",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateProviderEnvironment(t)
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			writeAWSFile(t, filepath.Join(home, ".aws"), tc.file, tc.content)

			s, err := Open(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Shutdown() }()
			if got := s.AnyProviderConfigured(); got != tc.want {
				t.Fatalf("AnyProviderConfigured = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBedrockSharedFileEnvironmentPaths checks the AWS_SHARED_CREDENTIALS_FILE
// and AWS_CONFIG_FILE overrides the AWS SDK honours.
func TestBedrockSharedFileEnvironmentPaths(t *testing.T) {
	isolateProviderEnvironment(t)
	dir := t.TempDir()
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", writeAWSFile(t, dir, "credentials", "[default]\naws_access_key_id = AKIAIOSFODNN7EXAMPLE\naws_secret_access_key = secret\n"))
	t.Setenv("AWS_CONFIG_FILE", writeAWSFile(t, dir, "config", "[default]\nregion = eu-central-1\n"))

	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if !s.AnyProviderConfigured() {
		t.Fatal("shared credentials file override did not configure bedrock")
	}

	// A path that names no usable file is not a credential source.
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "missing"))
	if s.AnyProviderConfigured() {
		t.Fatal("missing shared credentials file configured bedrock")
	}
}

// TestBedrockBearerTokenResolvesModel checks the stored-token path: /login
// bedrock stores a bearer token and the factory resolves a Bedrock model
// without contacting AWS.
func TestBedrockBearerTokenResolvesModel(t *testing.T) {
	isolateProviderEnvironment(t)
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	if err := s.SetProviderCredential("bedrock", ""); err == nil {
		t.Fatal("empty bearer token accepted")
	} else if !strings.Contains(err.Error(), "bearer token") {
		t.Fatalf("unexpected error for an empty bearer token: %v", err)
	}

	if err := s.SetProviderCredential("bedrock", "bedrock-bearer-token"); err != nil {
		t.Fatal(err)
	}
	for _, modelID := range []string{
		"bedrock/us.anthropic.claude-sonnet-4-6-v1:0",
		// An inference-profile ARN carries its own slashes; only the prefix
		// separator may be consumed.
		"bedrock/arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-sonnet-4-6-v1:0",
	} {
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
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	if err := s.SetProviderCredential("bedrock", "bedrock-bearer-token"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveProviderCredential("bedrock"); err != nil {
		t.Fatal(err)
	}
	if !s.AnyProviderConfigured() {
		t.Fatal("AWS credential chain stopped configuring bedrock after logout")
	}
}

// TestBedrockUsesAWSCredentialChainWithoutKey checks that a machine with AWS
// credentials needs no stored key.
func TestBedrockUsesAWSCredentialChainWithoutKey(t *testing.T) {
	isolateProviderEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("AWS_REGION", "us-east-1")

	model, err := DefaultFantasyModelFactory(t.Context(), "bedrock/us.anthropic.claude-haiku-4-5-v1:0")
	if err != nil {
		t.Fatalf("bedrock model not resolved through the AWS credential chain: %v", err)
	}
	if got := model.Model(); got != "us.anthropic.claude-haiku-4-5-v1:0" {
		t.Fatalf("unexpected model: %q", got)
	}
}

// TestBedrockRegionResolution checks the region the bearer-token path passes to
// the AWS client: environment first, then the shared AWS config, then the
// documented default.
func TestBedrockRegionResolution(t *testing.T) {
	isolateProviderEnvironment(t)
	ctx := t.Context()
	if got := bedrockRegion(ctx); got != bedrockDefaultRegion {
		t.Fatalf("region without AWS configuration = %q, want %q", got, bedrockDefaultRegion)
	}
	t.Setenv("AWS_DEFAULT_REGION", "eu-central-1")
	if got := bedrockRegion(ctx); got != "eu-central-1" {
		t.Fatalf("AWS_DEFAULT_REGION not honoured: %q", got)
	}
	t.Setenv("AWS_REGION", "us-west-2")
	if got := bedrockRegion(ctx); got != "us-west-2" {
		t.Fatalf("AWS_REGION did not take precedence: %q", got)
	}

	// A region only in the shared config is used when no variable is set.
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", writeAWSFile(t, t.TempDir(), "config", "[default]\nregion = ap-southeast-2\n"))
	if got := bedrockRegion(ctx); got != "ap-southeast-2" {
		t.Fatalf("shared config region not honoured: %q", got)
	}

	// A named profile's region is honoured too, which is the usual setup for
	// operators with more than one AWS account.
	t.Setenv("AWS_PROFILE", "work")
	t.Setenv("AWS_CONFIG_FILE", writeAWSFile(t, t.TempDir(), "config", "[profile work]\nregion = ca-central-1\n"))
	if got := bedrockRegion(ctx); got != "ca-central-1" {
		t.Fatalf("profile region not honoured: %q", got)
	}
}

// TestBedrockModelSelectorsAccepted checks that Bedrock selectors validate and
// that the curated catalog is offered.
func TestBedrockModelSelectorsAccepted(t *testing.T) {
	isolateProviderEnvironment(t)
	for _, selector := range []string{
		"bedrock/us.anthropic.claude-sonnet-4-6-v1:0",
		"bedrock/arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-sonnet-4-6-v1:0",
	} {
		if err := validateModelSelector(selector); err != nil {
			t.Fatalf("selector %q rejected: %v", selector, err)
		}
	}
	if len(modelsForProvider("bedrock")) == 0 {
		t.Fatal("no curated bedrock models")
	}
}

// TestBedrockModelListingUnsupported checks that the model menu falls back to
// the curated catalog instead of an OpenAI-compatible /models request.
func TestBedrockModelListingUnsupported(t *testing.T) {
	isolateProviderEnvironment(t)
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	_, err = s.FetchModels(t.Context(), "bedrock")
	if err == nil {
		t.Fatal("bedrock model listing succeeded")
	}
	if !strings.Contains(err.Error(), "does not list models") {
		t.Fatalf("unexpected listing error: %v", err)
	}
	if len(s.ModelsForProvider("bedrock")) == 0 {
		t.Fatal("curated bedrock catalog is empty")
	}
}
