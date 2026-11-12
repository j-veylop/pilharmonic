package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchModelsFromEndpoint(t *testing.T) {
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

	// Point the "deepseek" endpoint at the test server, store a key, and fetch.
	original := fantasyEndpoints["deepseek"]
	fantasyEndpoints["deepseek"] = fantasyEndpoint{baseURL: server.URL + "/v1", envVar: "DEEPSEEK_API_KEY"}
	defer func() { fantasyEndpoints["deepseek"] = original }()

	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if err := s.SetProviderCredential("deepseek", "sk-test"); err != nil {
		t.Fatal(err)
	}

	models, err := s.FetchModels(context.Background(), "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("unexpected models: %v", models)
	}
}

func TestFetchModelsRequiresKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if _, err := s.FetchModels(context.Background(), "openai"); err == nil {
		t.Fatal("fetch without a key succeeded")
	} else if !strings.Contains(err.Error(), "requires an API key") {
		t.Fatalf("unexpected error: %v", err)
	}
}
