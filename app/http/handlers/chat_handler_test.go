package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestChatHandler_Completions_NoAPIKeyReturnsFriendlyReply(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	h := NewChatHandler()

	reqBody := map[string]interface{}{
		"message": "hello",
		"history": []map[string]string{{
			"role":    "user",
			"content": "hello",
		}},
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.HandleCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("OPENAI_API_KEY")) {
		t.Fatalf("expected friendly missing-key message, got: %s", rec.Body.String())
	}
}

func TestChatHandler_Completions_InvalidMethod(t *testing.T) {
	h := NewChatHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/completions", nil)
	rec := httptest.NewRecorder()

	h.HandleCompletions(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for invalid method, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatHandler_Completions_EmptyMessage(t *testing.T) {
	_ = os.Unsetenv("OPENAI_API_KEY")
	h := NewChatHandler()

	reqBody := map[string]interface{}{
		"message": "   ",
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.HandleCompletions(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for empty message, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatHandler_Completions_RequestTooLarge(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	h := NewChatHandler()

	oversized := strings.Repeat("a", int(chatRequestMaxBytes)+1024)
	reqBody := map[string]interface{}{
		"message": oversized,
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.HandleCompletions(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for oversized request, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatHandler_Completions_UpstreamErrorDoesNotLeakDetails(t *testing.T) {
	oldTransport := http.DefaultTransport
	defer func() {
		http.DefaultTransport = oldTransport
	}()

	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.openai.com/v1/chat/completions" {
			t.Fatalf("unexpected upstream url: %s", req.URL.String())
		}

		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"secret upstream detail"}`)),
		}, nil
	})

	t.Setenv("OPENAI_API_KEY", "test-key")
	h := NewChatHandler()

	reqBody := map[string]interface{}{
		"message": "hello",
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.HandleCompletions(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for upstream failure, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, "secret upstream detail") {
		t.Fatalf("unexpected upstream detail leak in response body: %s", body)
	}
}

func TestChatHandler_Completions_HistoryIsCapped(t *testing.T) {
	oldTransport := http.DefaultTransport
	defer func() {
		http.DefaultTransport = oldTransport
	}()

	var captured openAIChatPayload
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("failed to parse upstream payload: %v", err)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)),
		}, nil
	})

	t.Setenv("OPENAI_API_KEY", "test-key")
	h := NewChatHandler()

	history := make([]map[string]string, 0, 50)
	for i := 0; i < 50; i++ {
		history = append(history, map[string]string{
			"role":    "user",
			"content": "m",
		})
	}

	reqBody := map[string]interface{}{
		"message": "hello",
		"history": history,
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.HandleCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	if len(captured.Messages) > chatHistoryMaxEntries+1 {
		t.Fatalf("expected capped history, got %d messages", len(captured.Messages))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
