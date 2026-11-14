package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateEnvironment clears every credential source a provider can read and
// points HOME at an empty directory, so a developer machine that is logged in
// to AWS does not change the outcome. Instance metadata is disabled.
func isolateEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range Names() {
		t.Setenv(endpoints[name].envVar, "")
	}
	for _, name := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
		"AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_REGION", "AWS_DEFAULT_REGION",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

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

func TestResolveErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Resolve(ctx, "openai", EnvCredential); err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("missing slash: %v", err)
	}
	if _, err := Resolve(ctx, "nosuch/gpt", EnvCredential); err == nil || !strings.Contains(err.Error(), "unknown fantasy provider") {
		t.Fatalf("unknown provider: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := Resolve(ctx, "openai/gpt-4.1", EnvCredential); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("missing key: %v", err)
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	model, err := Resolve(ctx, "openrouter/anthropic/claude-sonnet-4", EnvCredential)
	if err != nil {
		t.Fatalf("configured provider rejected: %v", err)
	}
	if model == nil {
		t.Fatal("nil model returned")
	}
	if model.Provider() == "" || model.Model() == "" {
		t.Fatalf("model not resolved: %q/%q", model.Provider(), model.Model())
	}
}

// TestBedrockUsesAWSCredentialChainWithoutKey checks that a machine with AWS
// credentials needs no stored key.
func TestBedrockUsesAWSCredentialChainWithoutKey(t *testing.T) {
	isolateEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("AWS_REGION", "us-east-1")

	model, err := Resolve(t.Context(), "bedrock/us.anthropic.claude-haiku-4-5-v1:0", EnvCredential)
	if err != nil {
		t.Fatalf("bedrock model not resolved through the AWS credential chain: %v", err)
	}
	if got := model.Model(); got != "us.anthropic.claude-haiku-4-5-v1:0" {
		t.Fatalf("unexpected model: %q", got)
	}
}

// TestBedrockRegionResolution checks the region the bearer-token path passes to
// the AWS client: environment first, then the shared AWS config (including a
// named profile), then the documented default.
func TestBedrockRegionResolution(t *testing.T) {
	for _, tc := range []struct {
		env    map[string]string
		config string
		want   string
	}{
		{nil, "", bedrockDefaultRegion},
		{map[string]string{"AWS_DEFAULT_REGION": "eu-central-1"}, "", "eu-central-1"},
		{map[string]string{"AWS_DEFAULT_REGION": "eu-central-1", "AWS_REGION": "us-west-2"}, "[default]\nregion = ap-southeast-2\n", "us-west-2"},
		{nil, "[default]\nregion = ap-southeast-2\n", "ap-southeast-2"},
		{map[string]string{"AWS_PROFILE": "work"}, "[profile work]\nregion = ca-central-1\n", "ca-central-1"},
	} {
		isolateEnvironment(t)
		for key, value := range tc.env {
			t.Setenv(key, value)
		}
		if tc.config != "" {
			t.Setenv("AWS_CONFIG_FILE", writeAWSFile(t, t.TempDir(), "config", tc.config))
		}
		if got := bedrockRegion(t.Context()); got != tc.want {
			t.Fatalf("env %v config %q: region = %q, want %q", tc.env, tc.config, got, tc.want)
		}
	}
}

func TestListModelsFromEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("missing bearer token: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"},{"id":"model-a"}]}`))
	}))
	defer server.Close()

	// Point the "deepseek" endpoint at the test server and fetch.
	original := endpoints["deepseek"]
	endpoints["deepseek"] = endpoint{baseURL: server.URL + "/v1", envVar: "DEEPSEEK_API_KEY"}
	defer func() { endpoints["deepseek"] = original }()

	models, err := ListModels(context.Background(), "deepseek", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("unexpected models: %v", models)
	}
}

func TestListModelsRequiresKey(t *testing.T) {
	if _, err := ListModels(context.Background(), "openai", ""); err == nil {
		t.Fatal("fetch without a key succeeded")
	} else if !strings.Contains(err.Error(), "requires an API key") {
		t.Fatalf("unexpected error: %v", err)
	}
}
