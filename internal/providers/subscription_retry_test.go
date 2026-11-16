package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSubscriptionUnauthorizedRefresh(t *testing.T) {
	path := subscriptionFixture(t, false)
	var refreshed atomic.Int32
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshed.Add(1)
			_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"next"}`)
			return
		}
		attempts.Add(1)
		if r.Header.Get("Authorization") == "Bearer access" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			t.Error("wrong retry authorization")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
	}))
	defer server.Close()
	transport := &subscriptionTransport{responsesURL: server.URL + "/responses", base: server.Client().Transport, authPath: path, tokenURL: server.URL + "/token", refreshClient: server.Client()}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/responses", strings.NewReader(`{"input":[],"stream":true}`))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts.Load() != 2 || refreshed.Load() != 1 {
		t.Fatalf("attempts=%d refresh=%d", attempts.Load(), refreshed.Load())
	}
}

func TestSubscriptionChangedLoginPreserved(t *testing.T) {
	path := subscriptionFixture(t, true)
	replacement := `{"new_login":true}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(path, []byte(replacement), 0600); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"next"}`)
	}))
	defer server.Close()
	_, err := subscriptionCredential(t.Context(), path, server.URL, server.Client(), "")
	data, _ := os.ReadFile(path)
	if err == nil || string(data) != replacement {
		t.Fatalf("login overwritten: %s %v", data, err)
	}
}
