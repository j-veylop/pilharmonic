package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	"github.com/j-veylop/pilharmonic/internal/instance"
)

func TestSubscriptionRefreshRedirectRejected(t *testing.T) {
	path := subscriptionFixture(t, true)
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := subscriptionCredential(t.Context(), path, source.URL, source.Client(), "")
	if err == nil || leaked {
		t.Fatalf("refresh redirect followed: leaked=%v err=%v", leaked, err)
	}
}

func TestSubscriptionOriginGuard(t *testing.T) {
	transport := &subscriptionTransport{authPath: "must-not-be-read"}
	for _, url := range []string{"https://evil.example/responses", "http://chatgpt.com/backend-api/codex/responses", "https://chatgpt.com/backend-api/codex/responses?redirect=1", "https://chatgpt.com/other/responses"} {
		req, _ := http.NewRequest("POST", url, strings.NewReader(`{}`))
		_, err := transport.RoundTrip(req)
		if err == nil || err.Error() != "unsupported subscription request" {
			t.Fatalf("origin not rejected before auth: %s %v", url, err)
		}
	}
}

func TestSubscriptionRefreshLockCancellation(t *testing.T) {
	path := subscriptionFixture(t, true)
	release, err := instance.Acquire(path + ".refresh")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = subscriptionCredential(ctx, path, "unused", http.DefaultClient, "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("lock cancellation: %v", err)
	}
}

func TestSubscriptionSanitizesHTTPAndStreamErrors(t *testing.T) {
	for _, mode := range []string{"http", "sse", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			path := subscriptionFixture(t, false)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					t.Error("not Responses API")
				}
				if mode == "http" {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"message":"access refresh id","type":"invalid_request_error"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "sse" {
					_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"message\":\"access refresh id\",\"code\":\"bad\"}\n\n")
				}
			}))
			defer server.Close()
			tr := &subscriptionTransport{responsesURL: server.URL + "/responses", base: server.Client().Transport, authPath: path, tokenURL: server.URL + "/token", refreshClient: server.Client()}
			p, err := openai.New(openai.WithBaseURL(server.URL), openai.WithAPIKey("placeholder"), openai.WithUseResponsesAPI(), openai.WithResponsesAPIFunc(func(string) bool { return true }), openai.WithHTTPClient(&http.Client{Transport: tr}))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := p.LanguageModel(t.Context(), "arbitrary-future-model")
			if err != nil {
				t.Fatal(err)
			}
			model := subscriptionModel{raw}
			call := fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hello")}}
			assertSafe := func(err error) {
				t.Helper()
				if err == nil {
					t.Fatal("expected error")
				}
				if mode == "http" && err.Error() != "ChatGPT subscription request failed (HTTP 400)" {
					t.Fatal("missing sanitized status")
				}
				dump := fmt.Sprintf("%+v %#v", err, err)
				if strings.Contains(dump, "access refresh") || strings.Contains(dump, "Bearer") || errors.Unwrap(err) != nil {
					t.Fatalf("unsafe error: %s", dump)
				}
			}
			_, err = model.Generate(t.Context(), call)
			assertSafe(err)
			stream, err := model.Stream(t.Context(), call)
			if err != nil {
				assertSafe(err)
				return
			}
			var streamErr error
			for part := range stream {
				if part.Error != nil {
					streamErr = part.Error
				}
			}
			assertSafe(streamErr)
		})
	}
}
