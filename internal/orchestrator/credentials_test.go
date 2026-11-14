package orchestrator

import (
	"path/filepath"
	"testing"
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
