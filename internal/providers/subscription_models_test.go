package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscriptionModelsCatalog(t *testing.T) {
	path := subscriptionFixture(t, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" || r.URL.RawQuery != "client_version=999.0.0" || r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("ChatGPT-Account-Id") != "account" || r.Header.Get("originator") != "crush" || r.Header.Get("Accept") != "application/json" {
			t.Error("incorrect catalog request")
		}
		io.WriteString(w, `{"models":[{"slug":" z-model ","display_name":"Z","visibility":"list","supported_in_api":false,"supported_reasoning_levels":[{"effort":"high","description":"High"}],"priority":1},{"slug":"a-model","visibility":"list","supported_in_api":true},{"slug":"z-model","visibility":"list"},{"slug":"hidden","visibility":"hide"},{"slug":"none","visibility":"none"},{"slug":"future","visibility":"future"},{"slug":"missing"},{"slug":" ","visibility":"list"}]}`)
	}))
	defer server.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	home, _ := subscriptionHome()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	client := server.Client()
	client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "chatgpt.com" || r.URL.Path != "/backend-api/codex/models" {
			t.Fatal("incorrect production endpoint")
		}
		r = r.Clone(r.Context())
		target, _ := http.NewRequest(http.MethodGet, server.URL+"/models?"+r.URL.RawQuery, nil)
		r.URL = target.URL
		return old.RoundTrip(r)
	})
	http.DefaultTransport = client.Transport
	defer func() { http.DefaultTransport = old }()
	models, err := ListModels(t.Context(), Subscription, "ignored-api-key")
	if err != nil || !reflect.DeepEqual(models, []string{"a-model", "z-model"}) {
		t.Fatalf("catalog: %v %v", models, err)
	}
}

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSubscriptionModelsRefresh(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			path := subscriptionFixture(t, expired)
			var refreshes, listings atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					refreshes.Add(1)
					io.WriteString(w, `{"access_token":"renewed","refresh_token":"rotated"}`)
					return
				}
				listings.Add(1)
				if r.Header.Get("Authorization") == "Bearer access" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.Header.Get("Authorization") != "Bearer renewed" || r.Header.Get("ChatGPT-Account-Id") != "account" {
					t.Error("incorrect refreshed credential")
				}
				io.WriteString(w, `{"models":[{"slug":"current","visibility":"list","supported_in_api":false}]}`)
			}))
			defer server.Close()
			models, err := fetchSubscriptionModels(t.Context(), path, server.URL, server.URL+"/token", server.Client())
			wantListings := int32(2)
			if expired {
				wantListings = 1
			}
			if err != nil || len(models) != 1 || refreshes.Load() != 1 || listings.Load() != wantListings {
				t.Fatalf("models=%v err=%v refreshes=%d listings=%d", models, err, refreshes.Load(), listings.Load())
			}
			auth, _, err := readSubscriptionAuth(path)
			if err != nil || auth.Tokens.AccessToken != "renewed" || auth.Tokens.RefreshToken != "rotated" {
				t.Fatal("rotation not persisted")
			}
		})
	}
}

func TestSubscriptionModelsErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"forbidden", 403, "access refresh account"},
		{"unauthorized", 401, "access refresh account"},
		{"server", 500, "access refresh account"},
		{"malformed", 200, `{"models":access}`},
		{"wrong-shape", 200, `{"data":[{"id":"static"}]}`},
		{"null", 200, `{"models":null}`},
		{"wrong-type", 200, `{"models":[{"slug":123}]}`},
		{"trailing", 200, `{"models":[]} access`},
		{"oversize", 200, `{"models":[],"extra":"` + strings.Repeat("x", 1<<20) + `"}`},
		{"empty", 200, `{"models":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			models, err := fetchSubscriptionModels(t.Context(), subscriptionFixture(t, false), server.URL, server.URL, server.Client())
			if err == nil || models != nil {
				t.Fatalf("accepted failed listing: %v %v", models, err)
			}
			for _, secret := range []string{"access", "refresh account", server.URL} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("unsafe diagnostic: %v", err)
				}
			}
		})
	}
}

func TestSubscriptionModelsRedirects(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprint(refresh), func(t *testing.T) {
			var leaked atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/access-refresh-account", http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			_, err := fetchSubscriptionModels(t.Context(), subscriptionFixture(t, refresh), server.URL, server.URL, server.Client())
			if err == nil || leaked.Load() != 0 || strings.Contains(err.Error(), target.URL) {
				t.Fatalf("unsafe redirect: %v calls=%d", err, leaked.Load())
			}
		})
	}
}

func TestSubscriptionModelsTransportErrors(t *testing.T) {
	for _, cause := range []error{errors.New("access refresh account https://secret.invalid"), context.DeadlineExceeded} {
		client := &http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) { return nil, cause })}
		_, err := fetchSubscriptionModels(t.Context(), subscriptionFixture(t, false), "https://catalog.invalid", "https://token.invalid", client)
		if err == nil || strings.Contains(err.Error(), "access") || strings.Contains(err.Error(), "secret.invalid") {
			t.Fatalf("unsafe transport error: %v", err)
		}
		if errors.Is(cause, context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline lost: %v", err)
		}
	}
}

func TestSubscriptionModelsCancellation(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprint(refresh), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := fetchSubscriptionModels(ctx, subscriptionFixture(t, refresh), server.URL, server.URL, server.Client())
				done <- err
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("listing ignored cancellation")
			}
		})
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if models, err := ListModels(t.Context(), Subscription, "api-key"); err == nil || models != nil {
		t.Fatal("missing app login returned a catalog")
	}
}
