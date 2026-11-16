package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
)

type subscriptionTestTransport func(*http.Request) (*http.Response, error)

func (f subscriptionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSubscriptionResolvedStreamToolLoop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home, err := subscriptionHome()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(subscriptionFixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "auth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/responses" {
			t.Error("not Responses API")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid JSON")
		}
		if string(body["store"]) != "false" || string(body["stream"]) != "true" || !strings.Contains(string(body["instructions"]), "fake_ok") {
			t.Error("missing subscription fields")
		}
		if !strings.Contains(string(body["tools"]), "fake_ok") {
			t.Error("missing tool")
		}
		if requests == 2 && (!strings.Contains(string(body["input"]), "function_call_output") || !strings.Contains(string(body["input"]), "call_1")) {
			t.Error("missing tool roundtrip")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"arbitrary-future-model\"}}\n\n")
		if requests == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"fake_ok\",\"arguments\":\"\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":\"{}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"fake_ok\",\"arguments\":\"{}\"}}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"OK\"}\n\n")
		}
		if requests == 2 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\",\"annotations\":[]}]}}\n\n")
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
	}))
	defer server.Close()
	original := http.DefaultTransport
	target, _ := url.Parse(server.URL + "/responses")
	http.DefaultTransport = subscriptionTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != subscriptionBaseURL+"/responses" {
			t.Error("unexpected endpoint")
			return nil, fmt.Errorf("unexpected endpoint")
		}
		clone := r.Clone(r.Context())
		clone.URL = target
		return original.RoundTrip(clone)
	})
	defer func() { http.DefaultTransport = original }()
	model, err := resolveSubscription(t.Context(), "arbitrary-future-model")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	tool := fantasy.NewAgentTool("fake_ok", "Return OK", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		calls++
		return fantasy.NewTextResponse("OK"), nil
	})
	agent := fantasy.NewAgent(model, fantasy.WithSystemPrompt("Use fake_ok then reply OK"), fantasy.WithTools(tool))
	result, err := agent.Stream(t.Context(), fantasy.AgentStreamCall{Prompt: "Reply OK"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || calls != 1 || len(result.Steps) != 2 || result.Response.Content.Text() != "OK" {
		t.Fatalf("requests=%d calls=%d steps=%d", requests, calls, len(result.Steps))
	}
}
