package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResponseWriterWrapperImplementsFlusher guards SSE/streaming handlers: the
// logging middleware wraps http.ResponseWriter, and that wrapper MUST satisfy
// http.Flusher (delegating to the underlying writer) or handlers that key off
// w.(http.Flusher) fail with "streaming unsupported" 500.
func TestResponseWriterWrapperImplementsFlusher(t *testing.T) {
	innerRan := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("wrapped ResponseWriter does not implement http.Flusher (SSE would 500)")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: ok\n\n")
		flusher.Flush() // must not panic / block
		innerRan = true
	})

	srv := httptest.NewServer(LoggingMiddleware(inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (streaming unsupported means wrapper lacks Flusher)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "data: ok") {
		t.Fatalf("body = %q, want it to contain the streamed line", string(body))
	}
	if !innerRan {
		t.Fatal("inner handler did not run")
	}
}
