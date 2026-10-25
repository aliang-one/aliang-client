package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sseServer 起一个假上游，按 handler 返回 SSE。
func sseServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

// writeSSE 把若干帧按 SSE 格式写出。
func writeSSE(w http.ResponseWriter, frames ...string) {
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, f := range frames {
		fmt.Fprintf(w, "data: %s\n\n", f)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// collect 跑一次 Stream，把事件收集起来。
func collect(t *testing.T, client *Client, req *MessagesRequest) ([]StreamEvent, error) {
	t.Helper()
	var got []StreamEvent
	err := client.Stream(context.Background(), req, func(ev StreamEvent) error {
		got = append(got, ev)
		return nil
	})
	return got, err
}

func TestStreamTextTurn(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Errorf("anthropic-version = %q, want %q", got, anthropicVersion)
		}
		if got := r.Header.Get("authorization"); got != "" {
			t.Errorf("走平台代理时不应带 Authorization，实际 %q", got)
		}
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_abc","model":"claude-x","role":"assistant"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"，世界"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		)
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{
		Model:    "claude-x",
		Messages: []Message{TextMessage("user", "hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text strings.Builder
	var messageID, stopReason string
	for _, ev := range got {
		switch ev.Kind {
		case EventStart:
			messageID = ev.MessageID
		case EventText:
			text.WriteString(ev.Text)
		case EventStop:
			stopReason = ev.StopReason
		}
	}
	if messageID != "msg_abc" {
		t.Errorf("message id = %q, want msg_abc", messageID)
	}
	if text.String() != "你好，世界" {
		t.Errorf("text = %q, want 你好，世界", text.String())
	}
	if stopReason != StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", stopReason)
	}
}

func TestStreamToolUseAccumulation(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_tool","model":"claude-x"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
			`{"type":"message_stop"}`,
		)
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var toolID, toolName, partial strings.Builder
	var stopReason string
	for _, ev := range got {
		switch ev.Kind {
		case EventToolUseStart:
			toolID.WriteString(ev.ID)
			toolName.WriteString(ev.Name)
		case EventToolInput:
			partial.WriteString(ev.PartialJSON)
		case EventStop:
			stopReason = ev.StopReason
		}
	}
	if toolID.String() != "toolu_1" || toolName.String() != "read" {
		t.Errorf("tool = %q/%q, want toolu_1/read", toolID.String(), toolName.String())
	}
	if partial.String() != `{"path":"a.go"}` {
		t.Errorf("partial json = %q", partial.String())
	}
	if stopReason != StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", stopReason)
	}
}

func TestStreamMaxTokensTruncation(t *testing.T) {
	// 截断时必须把 stop_reason 如实报成 max_tokens，让内核的截断守卫
	// 丢弃这一批 tool_use（参数不完整，执行会写坏文件）。
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_trunc","model":"claude-x"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"write"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\",\"content\":\"unfin"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
		)
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var stopReason string
	for _, ev := range got {
		if ev.Kind == EventStop {
			stopReason = ev.StopReason
		}
	}
	if stopReason != StopReasonMaxTokens {
		t.Fatalf("stop reason = %q, want max_tokens", stopReason)
	}
}

func TestStreamRetriesOn500(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_retry","model":"claude-x"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err != nil {
		t.Fatalf("Stream 应在 500 后重试成功: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("attempts = %d, want 2", n)
	}
	if len(got) == 0 || got[0].Kind != EventStart {
		t.Errorf("重试后应拿到正常事件流，实际 %+v", got)
	}
}

func TestStreamNoRetryOn400(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	})

	_, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err == nil {
		t.Fatal("400 应当返回错误")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("400 不应重试，attempts = %d", n)
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusBadRequest {
		t.Errorf("err = %v, want 400 httpStatusError", err)
	}
}

func TestStreamErrorEvent(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_e","model":"claude-x"}}`,
			`{"type":"error","error":{"type":"overloaded_error","message":"上游过载"}}`,
		)
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sawError bool
	for _, ev := range got {
		if ev.Kind == EventError {
			sawError = true
			if !strings.Contains(ev.Err.Error(), "上游过载") {
				t.Errorf("error = %v, want 含上游消息", ev.Err)
			}
		}
	}
	if !sawError {
		t.Fatal("应产出 EventError")
	}
}

func TestStreamContextCancel(t *testing.T) {
	// 上游挂住不返回，ctx 取消后 Stream 必须及时退出（进程内内核没有子进程
	// 可杀，取消只能靠自己轮询）。
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := NewClient(srv.URL).Stream(ctx, &MessagesRequest{Model: "claude-x"}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("ctx 超时后 Stream 应返回错误")
	}
}

func TestStreamAbortFromCallback(t *testing.T) {
	// onEvent 返回错误（如用户取消/工具失败）应立即中止，不重试。
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_abort","model":"claude-x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`,
		)
	})

	sentinel := errors.New("用户取消")
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "claude-x"}, func(ev StreamEvent) error {
		if ev.Kind == EventText {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("回调中止不应重试，attempts = %d", n)
	}
}

func TestStreamIgnoresMalformedFrame(t *testing.T) {
	// 上游偶发插入非 JSON 保活帧，不该整轮失败。
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: not-json\n\n")
		fmt.Fprint(w, ": heartbeat\n\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_ok","model":"claude-x"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err != nil {
		t.Fatalf("坏帧不应导致失败: %v", err)
	}
	if len(got) == 0 || got[0].MessageID != "msg_ok" {
		t.Errorf("应跳过坏帧继续解析，实际 %+v", got)
	}
}

func TestStreamDefaultMaxTokens(t *testing.T) {
	var captured MessagesRequest
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeSSE(w, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	})

	if _, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if captured.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want %d", captured.MaxTokens, defaultMaxTokens)
	}
	if !captured.Stream {
		t.Error("stream 必须为 true")
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":    0,
		"3":   3 * time.Second,
		"-1":  0,
		"abc": 0,
	}
	for raw, want := range cases {
		if got := parseRetryAfter(raw); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", raw, got, want)
		}
	}
}
