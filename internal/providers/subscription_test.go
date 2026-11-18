package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
)

func subscriptionFixture(t *testing.T, expired bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	stamp := time.Now().UTC()
	if expired {
		stamp = stamp.Add(-8 * 24 * time.Hour)
	}
	data, _ := json.Marshal(subscriptionAuth{Mode: "chatgpt", Tokens: subscriptionTokens{AccessToken: "access", RefreshToken: "refresh", IDToken: "id", AccountID: "account"}, LastRefresh: stamp})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSubscriptionLoginCommandIsolatesCodexHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := LoginCommand(true)
	if !strings.Contains(strings.Join(cmd.Args, " "), "--device-auth") || !strings.Contains(strings.Join(cmd.Args, " "), `cli_auth_credentials_store="file"`) {
		t.Fatalf("login args: %v", cmd.Args)
	}
	home, _ := subscriptionHome()
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "CODEX_HOME="+home) {
		t.Fatal("login not isolated")
	}
}

func TestSubscriptionConcurrentRefresh(t *testing.T) {
	path := subscriptionFixture(t, true)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != subscriptionClientID || body["refresh_token"] != "refresh" {
			t.Errorf("unexpected refresh request")
		}
		_, _ = io.WriteString(w, `{"access_token":"renewed","refresh_token":"rotated"}`)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			tokens, err := subscriptionCredential(t.Context(), path, server.URL, server.Client(), "")
			if err != nil || tokens.AccessToken != "renewed" {
				t.Errorf("refresh: %v %v", tokens.AccessToken, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh called %d times", calls.Load())
	}
	auth, _, err := readSubscriptionAuth(path)
	if err != nil || auth.Tokens.RefreshToken != "rotated" || auth.Tokens.AccountID != "account" {
		t.Fatal("refresh not preserved")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
}

func TestSubscriptionRefreshFailurePreservesCredentials(t *testing.T) {
	path := subscriptionFixture(t, true)
	before, _ := os.ReadFile(path)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, "secret refresh")
	}))
	defer server.Close()
	_, err := subscriptionCredential(t.Context(), path, server.URL, server.Client(), "")
	after, _ := os.ReadFile(path)
	if err == nil || strings.Contains(err.Error(), "secret") || string(before) != string(after) {
		t.Fatalf("unsafe refresh failure: %v", err)
	}
}

func TestSubscriptionNativeResponses(t *testing.T) {
	path := subscriptionFixture(t, false)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Errorf("unexpected request %s %v", r.URL.Path, r.Header)
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if string(body["stream"]) != "true" || string(body["store"]) != "false" || !strings.Contains(string(body["instructions"]), "Be careful") {
			t.Errorf("invalid fields: %s", body)
		}
		if !strings.Contains(string(body["tools"]), "lookup") {
			t.Errorf("lost tools: %s", body)
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Error("unsupported token limit sent")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.3-codex\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
	}))
	defer server.Close()
	transport := &subscriptionTransport{responsesURL: server.URL + "/responses", base: server.Client().Transport, authPath: path, tokenURL: server.URL + "/token", refreshClient: server.Client()}
	provider, err := openai.New(openai.WithName(Subscription), openai.WithBaseURL(server.URL), openai.WithAPIKey("placeholder"), openai.WithUseResponsesAPI(), openai.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	model, err := provider.LanguageModel(t.Context(), "gpt-5.3-codex")
	if err != nil {
		t.Fatal(err)
	}
	call := fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewSystemMessage("Be careful"), fantasy.NewUserMessage("Hello")}, Tools: []fantasy.Tool{fantasy.FunctionTool{Name: "lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}}}
	response, err := model.Generate(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content.Text() != "Hello" || response.Usage.TotalTokens != 6 {
		t.Fatalf("bad generated response: %+v", response)
	}
	stream, err := model.Stream(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for part := range stream {
		if part.Type == fantasy.StreamPartTypeError {
			t.Fatal(part.Error)
		}
		if part.Type == fantasy.StreamPartTypeTextDelta {
			text += part.Delta
		}
	}
	if text != "Hello" || requests.Load() != 2 {
		t.Fatalf("stream text=%q requests=%d", text, requests.Load())
	}
}
