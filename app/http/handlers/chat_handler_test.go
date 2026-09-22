package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// injectChatMTLSClient pins the handler's singleton mTLS client to one backed
// by rt, so tests can capture the gateway forward (URL, payload) and stub
// replies without real client certificates. The returned func restores the
// singleton. Same-package only: it fires the client's sync.Once directly.
func injectChatMTLSClient(rt http.RoundTripper) func() {
	ResetChatMTLSClient()
	chatMTLSClientOnce.Do(func() {
		chatMTLSClient = &http.Client{Transport: rt, Timeout: 5 * time.Second}
		chatMTLSClientErr = nil
	})
	return ResetChatMTLSClient
}

// withGatewayConfig points core.api_server at apiServer for the duration of a
// test. An empty apiServer clears the config (no gateway configured).
func withGatewayConfig(apiServer string) func() {
	prev := config.GetGlobalConfig()
	if apiServer == "" {
		config.ResetGlobalConfigForTest()
	} else {
		config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: apiServer}})
	}
	return func() {
		if prev == nil {
			config.ResetGlobalConfigForTest()
		} else {
			config.SetGlobalConfig(prev)
		}
	}
}

func postChat(t *testing.T, reqBody map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	NewChatHandler().HandleCompletions(rec, req)
	return rec
}

// Since the 8c4f755 rewrite the handler is a pure mTLS gateway forwarder: it
// has no local OPENAI_API_KEY branch. Without a configured gateway it must
// fail with a server error instead of fabricating a friendly local reply.
func TestChatHandler_Completions_NoGatewayReturnsServerError(t *testing.T) {
	restoreCfg := withGatewayConfig("")
	defer restoreCfg()
	ResetChatMTLSClient()
	defer ResetChatMTLSClient()

	rec := postChat(t, map[string]interface{}{"message": "hello"})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 without a configured gateway, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "OPENAI_API_KEY") {
		t.Fatalf("handler must not fabricate a local missing-key reply, got: %s", rec.Body.String())
	}
}

func TestChatHandler_Completions_InvalidMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/chat/completions", nil)
	rec := httptest.NewRecorder()

	NewChatHandler().HandleCompletions(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for invalid method, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatHandler_Completions_EmptyMessage(t *testing.T) {
	rec := postChat(t, map[string]interface{}{"message": "   "})

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for empty message, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatHandler_Completions_RequestTooLarge(t *testing.T) {
	rec := postChat(t, map[string]interface{}{
		"message": strings.Repeat("a", int(chatRequestMaxBytes)+1024),
	})

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for oversized request, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// The handler forwards the sanitized, capped history plus the current message
// as an OpenAI-style payload to <core.api_server>/v1/chat/completions, and
// relays the gateway's reply content back to the client.
func TestChatHandler_Completions_ForwardsCappedHistoryToGateway(t *testing.T) {
	const apiServer = "https://gateway.example.com"
	var captured openAIChatPayload
	var gotURL string
	restoreClient := injectChatMTLSClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		body, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("failed to parse gateway payload: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)),
		}, nil
	}))
	defer restoreClient()
	restoreCfg := withGatewayConfig(apiServer)
	defer restoreCfg()

	history := make([]map[string]string, 0, 50)
	for i := 0; i < 50; i++ {
		history = append(history, map[string]string{
			"role":    "user",
			"content": "m",
		})
	}
	rec := postChat(t, map[string]interface{}{
		"message": "hello",
		"history": history,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", rec.Code, rec.Body.String())
	}
	if want := apiServer + "/v1/chat/completions"; gotURL != want {
		t.Fatalf("forwarded to %q, want %q", gotURL, want)
	}
	if captured.Model != "gpt-4o-mini" {
		t.Fatalf("forwarded model = %q, want gpt-4o-mini", captured.Model)
	}
	if len(captured.Messages) != chatHistoryMaxEntries+1 {
		t.Fatalf("gateway payload carried %d messages, want %d (capped history + current message)",
			len(captured.Messages), chatHistoryMaxEntries+1)
	}
	last := captured.Messages[len(captured.Messages)-1]
	if last.Role != "user" || last.Content != "hello" {
		t.Fatalf("last forwarded message = %+v, want the current user message", last)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"reply":"ok"`)) {
		t.Fatalf("gateway reply content must be relayed to the client, got: %s", rec.Body.String())
	}
}

// A non-2xx gateway response must surface as a server error without leaking
// the upstream response body.
func TestChatHandler_Completions_UpstreamErrorDoesNotLeakDetails(t *testing.T) {
	restoreClient := injectChatMTLSClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"secret upstream detail"}`)),
		}, nil
	}))
	defer restoreClient()
	restoreCfg := withGatewayConfig("https://gateway.example.com")
	defer restoreCfg()

	rec := postChat(t, map[string]interface{}{"message": "hello"})

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for upstream failure, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "secret upstream detail") {
		t.Fatalf("unexpected upstream detail leak in response body: %s", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
