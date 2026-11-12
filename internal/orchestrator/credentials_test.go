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
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	if err := s.SetProviderCredential("openai", "sk-test-key"); err != nil {
		t.Fatal(err)
	}
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
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
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

	if err := reopened.RemoveProviderCredential("openai"); err != nil {
		t.Fatal(err)
	}
	for _, c := range reopened.ProviderCredentials() {
		if c.Provider == "openai" && c.Configured {
			t.Fatal("credential not removed")
		}
	}
}

func TestProviderCredentialCountsEnvironment(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	for _, name := range []string{"OPENAI_API_KEY", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "HYPER_API_KEY"} {
		t.Setenv(name, "")
	}
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
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	// Without a stored key or env var, the factory fails with a clear error.
	if _, err := s.fantasyModelFactory(t.Context(), "deepseek/deepseek-flash"); err == nil {
		t.Fatal("expected a missing-key error")
	}

	if err := s.SetProviderCredential("deepseek", "sk-stored"); err != nil {
		t.Fatal(err)
	}
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

func TestHyperProviderResolves(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()

	if err := s.SetProviderCredential("hyper", "hk-test"); err != nil {
		t.Fatal(err)
	}
	model, err := s.fantasyModelFactory(t.Context(), "hyper/prism")
	if err != nil {
		t.Fatalf("hyper provider not resolved: %v", err)
	}
	if model.Model() != "prism" {
		t.Fatalf("unexpected model: %q", model.Model())
	}
}
