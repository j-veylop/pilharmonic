package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j-veylop/pilharmonic/internal/providers"
)

func TestProviderCredentialRoundTrip(t *testing.T) {
	// Pin the environment so "configured" only reflects the stored key here.
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := openTestService(t, path)

	must(t, s.SetProviderCredential("openai", "sk-test-key"))
	if err := s.SetProviderCredential("unknown-provider", "key"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := s.SetProviderCredential("openai", ""); err == nil {
		t.Fatal("empty key accepted")
	}

	creds := s.ProviderCredentials()
	configured := map[string]bool{}
	for _, c := range creds {
		configured[c.Provider] = c.Configured
	}
	if !configured["openai"] {
		t.Fatal("openai not reported configured")
	}

	// The key must persist across reopen and never be returned by any list call.
	reopened := mustGet(Open(path))(t)
	defer func() { _ = reopened.Shutdown() }()
	found := false
	for _, c := range reopened.ProviderCredentials() {
		if c.Provider == "openai" && c.Configured {
			found = true
		}
	}
	if !found {
		t.Fatal("stored credential did not survive reopen")
	}

	must(t, reopened.RemoveProviderCredential("openai"))
	for _, c := range reopened.ProviderCredentials() {
		if c.Provider == "openai" && c.Configured {
			t.Fatal("credential not removed")
		}
	}
}

func TestProviderCredentialCountsEnvironment(t *testing.T) {
	// Pin the environment so ambient credentials (including AWS ones that
	// configure Bedrock) cannot make this test pass or fail by accident.
	isolateProviderEnvironment(t)
	dir := t.TempDir()
	s := openTestService(t, filepath.Join(dir, "state.json"))

	if s.AnyProviderConfigured() {
		t.Fatal("empty environment reported a configured provider")
	}

	// An environment variable alone is enough to count as configured.
	t.Setenv("GROQ_API_KEY", "gsk-env")
	if !s.AnyProviderConfigured() {
		t.Fatal("environment variable did not configure a provider")
	}
	configured := map[string]bool{}
	for _, c := range s.ProviderCredentials() {
		configured[c.Provider] = c.Configured
	}
	if !configured["groq"] {
		t.Fatal("groq reported unconfigured despite its environment variable")
	}
	if configured["openai"] {
		t.Fatal("openai reported configured without a key")
	}
}

func TestSubscriptionCredentialIntegration(t *testing.T) {
	isolateProviderEnvironment(t)
	s := newTestService(t)
	configured := func() bool {
		t.Helper()
		credentials := s.ProviderCredentials()
		if len(credentials) != len(providers.Names()) {
			t.Fatalf("credential count = %d, want %d", len(credentials), len(providers.Names()))
		}
		for _, credential := range credentials {
			if credential.Provider == "openai-subscription" {
				return credential.Configured
			}
		}
		t.Fatal("subscription provider missing")
		return false
	}
	if configured() || s.AnyProviderConfigured() {
		t.Fatal("subscription configured without auth")
	}
	if _, err := s.fantasyModelFactory(t.Context(), "openai-subscription/gpt-5"); err == nil {
		t.Fatal("subscription model resolved without auth")
	}
	configDir := mustGet(os.UserConfigDir())(t)
	authDir := filepath.Join(configDir, "orchestrator", "openai-subscription")
	must(t, os.MkdirAll(authDir, 0700))
	must(t, os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"fake-subscription-token","refresh_token":"fake-refresh-token","account_id":"fake-account"}}`), 0600))
	if !configured() || !s.AnyProviderConfigured() {
		t.Fatal("subscription auth not reflected in credentials")
	}
	model, err := s.fantasyModelFactory(t.Context(), "openai-subscription/gpt-5")
	if err != nil {
		t.Fatalf("subscription model failed to resolve: %v", err)
	}
	if model.Model() != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", model.Model())
	}
	for _, key := range []string{"", "sk-test"} {
		if err := s.SetProviderCredential("openai-subscription", key); err == nil || !strings.Contains(err.Error(), "/login openai subscription") {
			t.Fatalf("subscription API-key error = %v", err)
		}
	}
	if err := s.RemoveProviderCredential("openai-subscription"); err == nil || !strings.Contains(err.Error(), "set CODEX_HOME to ") || !strings.Contains(err.Error(), authDir) || !strings.Contains(err.Error(), `codex -c 'cli_auth_credentials_store="file"' logout`) {
		t.Fatalf("subscription logout error = %v", err)
	}
	if !configured() {
		t.Fatal("rejected credential mutation changed subscription")
	}
}

func TestProviderCredentialFeedsModelFactory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	t.Setenv("DEEPSEEK_API_KEY", "")
	s := openTestService(t, path)

	// Without a stored key or env var, the factory fails with a clear error.
	if _, err := s.fantasyModelFactory(t.Context(), "deepseek/deepseek-flash"); err == nil {
		t.Fatal("expected a missing-key error")
	}

	must(t, s.SetProviderCredential("deepseek", "sk-stored"))
	// The factory should now resolve (it will build a provider object; it does
	// not make a network call until Generate/Stream).
	model, err := s.fantasyModelFactory(t.Context(), "deepseek/deepseek-flash")
	if err != nil {
		t.Fatalf("stored credential not used: %v", err)
	}
	if model.Model() != "deepseek-flash" {
		t.Fatalf("unexpected model: %q", model.Model())
	}
}
