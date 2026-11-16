package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	"pilharmonic/internal/instance"
)

const Subscription = "openai-subscription"
const subscriptionBaseURL = "https://chatgpt.com/backend-api/codex"
const subscriptionTokenURL = "https://auth.openai.com/oauth/token"
const subscriptionClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

type subscriptionTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
}

type subscriptionAuth struct {
	Mode        string             `json:"auth_mode"`
	Tokens      subscriptionTokens `json:"tokens"`
	LastRefresh time.Time          `json:"last_refresh"`
}

func subscriptionHome() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "orchestrator", "openai-subscription"), nil
}

func LoginCommand(device bool) *exec.Cmd {
	args := []string{"-c", `cli_auth_credentials_store="file"`, "login"}
	if device {
		args = append(args, "--device-auth")
	}
	cmd := exec.Command("codex", args...)
	home, err := subscriptionHome()
	if err != nil {
		cmd.Err = err
		return cmd
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		cmd.Err = err
		return cmd
	}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CODEX_HOME=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	return cmd
}

func readSubscriptionAuth(path string) (subscriptionAuth, []byte, error) {
	var auth subscriptionAuth
	file, err := os.Open(path)
	if err != nil {
		return auth, nil, fmt.Errorf("ChatGPT subscription requires /login openai-subscription")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return auth, nil, fmt.Errorf("read subscription credentials: %w", err)
	}
	if json.Unmarshal(data, &auth) != nil || (auth.Mode != "" && auth.Mode != "chatgpt") || auth.Tokens.AccessToken == "" || auth.Tokens.RefreshToken == "" || auth.Tokens.AccountID == "" {
		return auth, nil, fmt.Errorf("invalid ChatGPT subscription credentials; use /login openai-subscription")
	}
	return auth, data, nil
}

func SubscriptionConfigured() bool {
	home, err := subscriptionHome()
	if err != nil {
		return false
	}
	_, _, err = readSubscriptionAuth(filepath.Join(home, "auth.json"))
	return err == nil
}

func subscriptionExpired(auth subscriptionAuth) bool {
	parts := strings.Split(auth.Tokens.AccessToken, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if err == nil && json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
			return time.Now().Add(time.Minute).Unix() >= claims.Exp
		}
	}
	return auth.LastRefresh.IsZero() || time.Since(auth.LastRefresh) >= 7*24*time.Hour
}

func subscriptionCredential(ctx context.Context, path, tokenURL string, client *http.Client, rejected string) (subscriptionTokens, error) {
	if err := ctx.Err(); err != nil {
		return subscriptionTokens{}, err
	}
	auth, original, err := readSubscriptionAuth(path)
	if err != nil {
		return subscriptionTokens{}, err
	}
	if !subscriptionExpired(auth) && (rejected == "" || rejected != auth.Tokens.AccessToken) {
		return auth.Tokens, nil
	}
	var release func()
	for {
		release, err = instance.Acquire(path + ".refresh")
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return subscriptionTokens{}, fmt.Errorf("lock subscription refresh: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer release()
	auth, original, err = readSubscriptionAuth(path)
	if err != nil {
		return subscriptionTokens{}, err
	}
	if !subscriptionExpired(auth) && (rejected == "" || rejected != auth.Tokens.AccessToken) {
		return auth.Tokens, nil
	}
	body, _ := json.Marshal(map[string]string{"client_id": subscriptionClientID, "grant_type": "refresh_token", "refresh_token": auth.Tokens.RefreshToken, "scope": "openid profile email"})
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(refreshCtx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return subscriptionTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	refreshClient := *client
	refreshClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := refreshClient.Do(req)
	if err != nil {
		return subscriptionTokens{}, fmt.Errorf("refresh ChatGPT subscription failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return subscriptionTokens{}, fmt.Errorf("refresh ChatGPT subscription returned HTTP %d; use /login openai-subscription", resp.StatusCode)
	}
	var tokens subscriptionTokens
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens) != nil || tokens.AccessToken == "" {
		return subscriptionTokens{}, fmt.Errorf("invalid subscription refresh response")
	}
	auth.Tokens.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		auth.Tokens.RefreshToken = tokens.RefreshToken
	}
	if tokens.IDToken != "" {
		auth.Tokens.IDToken = tokens.IDToken
	}
	auth.LastRefresh = time.Now().UTC()
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		return subscriptionTokens{}, fmt.Errorf("subscription login changed during refresh; retry")
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(original, &document) != nil {
		return subscriptionTokens{}, fmt.Errorf("invalid subscription credentials")
	}
	var tokenDocument map[string]json.RawMessage
	if json.Unmarshal(document["tokens"], &tokenDocument) != nil {
		return subscriptionTokens{}, fmt.Errorf("invalid subscription tokens")
	}
	for key, value := range map[string]string{"access_token": auth.Tokens.AccessToken, "refresh_token": auth.Tokens.RefreshToken, "id_token": auth.Tokens.IDToken, "account_id": auth.Tokens.AccountID} {
		tokenDocument[key], _ = json.Marshal(value)
	}
	document["tokens"], _ = json.Marshal(tokenDocument)
	document["last_refresh"], _ = json.Marshal(auth.LastRefresh)
	data, _ := json.Marshal(document)
	if err := instance.WriteFileAtomic(path, ".auth-*", data); err != nil {
		return subscriptionTokens{}, fmt.Errorf("save subscription credentials: %w", err)
	}
	return auth.Tokens, nil
}

type subscriptionTransport struct {
	base          http.RoundTripper
	responsesURL  string
	authPath      string
	tokenURL      string
	refreshClient *http.Client
}

func (t *subscriptionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	allowed := t.responsesURL
	if allowed == "" {
		allowed = subscriptionBaseURL + "/responses"
	}
	if req.Method != http.MethodPost || req.URL.String() != allowed {
		return nil, fmt.Errorf("unsupported subscription request")
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(req.Body, 32<<20)).Decode(&body); err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	streaming := string(body["stream"]) == "true"
	body["stream"] = json.RawMessage("true")
	body["store"] = json.RawMessage("false")
	var instructions string
	_ = json.Unmarshal(body["instructions"], &instructions)
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(body["input"], &input); err != nil {
		return nil, fmt.Errorf("invalid subscription input")
	}
	kept := input[:0]
	for _, item := range input {
		var role string
		_ = json.Unmarshal(item["role"], &role)
		if role != "system" && role != "developer" {
			kept = append(kept, item)
			continue
		}
		var content []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item["content"], &content); err != nil {
			var text string
			if json.Unmarshal(item["content"], &text) != nil {
				return nil, fmt.Errorf("invalid subscription instructions")
			}
			content = append(content, struct {
				Text string `json:"text"`
			}{Text: text})
		}
		for _, part := range content {
			if instructions != "" {
				instructions += "\n\n"
			}
			instructions += part.Text
		}
	}
	body["instructions"], _ = json.Marshal(instructions)
	body["input"], _ = json.Marshal(kept)
	for _, key := range []string{"max_output_tokens", "temperature", "top_p", "truncation", "previous_response_id"} {
		delete(body, key)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(req.Context(), 40*time.Second)
	tokens, err := subscriptionCredential(ctx, t.authPath, t.tokenURL, t.refreshClient, "")
	cancel()
	if err != nil {
		return nil, err
	}
	send := func(tokens subscriptionTokens) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.Body = io.NopCloser(bytes.NewReader(data))
		clone.ContentLength = int64(len(data))
		clone.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		clone.Header.Set("ChatGPT-Account-Id", tokens.AccountID)
		clone.Header.Set("originator", "codex_cli_rs")
		clone.Header.Set("Accept", "text/event-stream")
		resp, err := t.base.RoundTrip(clone)
		if err != nil || resp.StatusCode != http.StatusOK || streaming {
			return resp, err
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event struct {
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				continue
			}
			if event.Type == "response.failed" || event.Type == "error" {
				return nil, fmt.Errorf("subscription response failed")
			}
			if event.Type != "response.completed" && event.Type != "response.incomplete" {
				continue
			}
			resp.Body = io.NopCloser(bytes.NewReader(event.Response))
			resp.ContentLength = int64(len(event.Response))
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("subscription stream ended without a response")
	}
	resp, err := send(tokens)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	_ = resp.Body.Close()
	ctx, cancel = context.WithTimeout(req.Context(), 40*time.Second)
	defer cancel()
	tokens, err = subscriptionCredential(ctx, t.authPath, t.tokenURL, t.refreshClient, tokens.AccessToken)
	if err != nil {
		return nil, err
	}
	return send(tokens)
}

func resolveSubscription(ctx context.Context, model string) (fantasy.LanguageModel, error) {
	home, err := subscriptionHome()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, "auth.json")
	if _, _, err := readSubscriptionAuth(path); err != nil {
		return nil, err
	}
	transport := &subscriptionTransport{base: http.DefaultTransport, authPath: path, tokenURL: subscriptionTokenURL, refreshClient: &http.Client{Timeout: 30 * time.Second}}
	provider, err := openai.New(openai.WithName(Subscription), openai.WithBaseURL(subscriptionBaseURL), openai.WithAPIKey("subscription"), openai.WithUseResponsesAPI(), openai.WithResponsesAPIFunc(func(string) bool { return true }), openai.WithHTTPClient(&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
	if err != nil {
		return nil, err
	}
	languageModel, err := provider.LanguageModel(ctx, strings.TrimSpace(model))
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return subscriptionModel{LanguageModel: languageModel}, nil
}
