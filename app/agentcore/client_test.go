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

// 上游发了一个 text delta 后中途断开连接（不是 message_stop）：此刻内核已经
// 把事件下发给调用方，streamTurn 的内容累积和宿主已收到的帧都在。若还按
// 「连接错误属瞬态」整轮重试，第二次尝试的 delta 会拼在第一次的半截文本
// 后面，缝出一段从未被完整说出过的内容。必须一次都不重试、原样报错。
//
// 变异守卫：删掉 Stream 里的 delivered 守卫，本条的 attempts 断言即红。
func TestStreamNoRetryAfterEventDelivered(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cut\",\"model\":\"x\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"半截\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 中途断开：不写 chunked 终止块直接中止连接（模拟 reset/EOF 形态，
		// 而非 message_stop）。net/http 对 ErrAbortHandler 静默处理、直接拆连接，
		// 客户端在已读到事件之后得到读取错误。
		panic(http.ErrAbortHandler)
	})

	var sawText bool
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(ev StreamEvent) error {
		if ev.Kind == EventText {
			sawText = true
		}
		return nil
	})
	if err == nil {
		t.Fatal("流中途断开应返回错误")
	}
	var idleErr *streamIdleError
	if errors.As(err, &idleErr) {
		t.Fatalf("err = %v, 不应被判定为 streamIdleError（断开不是块间停滞）", err)
	}
	if !sawText {
		t.Error("断开前下发的 text delta 应已到达调用方（delivered 守卫的前提）")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("已下发事件后不应重试，attempts = %d, want 1", n)
	}
}

// 首字节前断开（连接被接受但一个字节都不写就断）仍走重试：此时没有任何
// 事件下发过，瞬态重试语义不丢。
func TestStreamRetriesWhenCutBeforeFirstByte(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			// 接受连接后立刻断开、零字节。POST 走的是全新连接，transport
			// 不会替我们重试，这个错误必然到达 Stream 的重试循环。
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("无法 hijack 连接")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_retry","model":"x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"完整"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	})

	var texts int
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(ev StreamEvent) error {
		if ev.Kind == EventText {
			texts++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("首字节前的断开应重试成功: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Fatalf("attempts = %d, want 2（首断 + 重试成功）", n)
	}
	if texts != 1 {
		t.Errorf("text delta 数 = %d, want 1（重试成功的完整回合，不是缝接）", texts)
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

// 32000 的默认输出上限遇到只支持 16k 的模型会被 400。此时必须降级重试，
// 而不是把「max_tokens 太大」甩给用户整轮失败。
func TestStreamReducesMaxTokensOnRejection(t *testing.T) {
	var seen []int
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req MessagesRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, req.MaxTokens)
		if req.MaxTokens > 16000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 32000 > 16000, which is the maximum allowed number of output tokens for claude-x"}}`))
			return
		}
		writeSSE(w, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	})

	if _, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"}); err != nil {
		t.Fatalf("降级后应成功，实际: %v", err)
	}
	if len(seen) < 2 || seen[0] != defaultMaxTokens || seen[1] != 16000 {
		t.Fatalf("max_tokens 序列 = %v，期望以 [%d 16000 ...] 开头", seen, defaultMaxTokens)
	}
}

func TestReduceMaxTokens(t *testing.T) {
	badRequest := func(body string) error {
		return &httpStatusError{status: http.StatusBadRequest, body: body}
	}
	cases := []struct {
		name    string
		err     error
		current int
		want    int
		ok      bool
	}{
		{
			name:    "输出上限被拒——减半",
			err:     badRequest(`{"error":{"message":"max_tokens: 32000 > 16000"}}`),
			current: 32000,
			want:    16000,
			ok:      true,
		},
		{
			name:    "减半低于下限——钳到 8192",
			err:     badRequest(`{"error":{"message":"maximum output tokens is 8192"}}`),
			current: 12000,
			want:    minMaxTokens,
			ok:      true,
		},
		{
			name:    "已在下限——不再降级",
			err:     badRequest(`{"error":{"message":"max_tokens too large"}}`),
			current: minMaxTokens,
			ok:      false,
		},
		{
			name:    "与输出上限无关的 400——不降级",
			err:     badRequest(`{"error":{"message":"messages: roles must alternate"}}`),
			current: 32000,
			ok:      false,
		},
		{
			name:    "429 限流——不降级（降上限没用）",
			err:     &httpStatusError{status: http.StatusTooManyRequests, body: "max_tokens ..."},
			current: 32000,
			ok:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := reduceMaxTokens(tc.err, tc.current)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("reduceMaxTokens = (%d, %v)，期望 (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
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

// 真实网关限流时回的是 HTTP 200 + 裸 JSON，既不是非 2xx 也不是 SSE。
// 不识别它，用户就只会看到一句笼统的空回复。
func TestParseSSEDetectsUpstreamErrorBody(t *testing.T) {
	const compact = `{"error":{"message":"Upstream rate limit exceeded, please retry later","type":"rate_limit_error"},"type":"error"}`

	cases := map[string]string{
		"单行":     compact,
		"pretty": "{\n  \"error\": {\n    \"message\": \"Upstream rate limit exceeded, please retry later\",\n    \"type\": \"rate_limit_error\"\n  },\n  \"type\": \"error\"\n}",
		"心跳在前":   ": ping\n\n" + compact,
		"前后空白":   "  " + compact + "  ",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var events []StreamEvent
			err := parseSSE(context.Background(), strings.NewReader(body), func(ev StreamEvent) error {
				events = append(events, ev)
				return nil
			})
			if err == nil {
				t.Fatalf("裸 JSON 错误体应返回错误，实际 nil（事件 %+v）", events)
			}
			var upErr *upstreamError
			if !errors.As(err, &upErr) {
				t.Fatalf("err = %T %v, want *upstreamError", err, err)
			}
			if upErr.kind != "rate_limit_error" {
				t.Errorf("kind = %q, want rate_limit_error", upErr.kind)
			}
			if !strings.Contains(err.Error(), "Upstream rate limit exceeded") {
				t.Errorf("错误信息应带上游原文，实际 %q", err.Error())
			}
			if len(events) != 0 {
				t.Errorf("不应产出任何事件，实际 %+v", events)
			}
		})
	}
}

// 非错误形状的 JSON（比如网关塞了别的东西）不能被当成错误，也不能吞掉后续 SSE。
func TestParseSSEIgnoresNonErrorJSON(t *testing.T) {
	// 末尾给一个终点帧：EOF 侧现在要求流正常收尾（见 streamUnterminatedError），
	// 本条只验证「非错误 JSON 不误伤、后续 SSE 照常解析」。
	body := "{\"ok\":true}\n\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"glm-5.2\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	var events []StreamEvent
	if err := parseSSE(context.Background(), strings.NewReader(body), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	// {"ok":true} 不能变成事件（尤其不能变成错误），后续 message_start 必须照常解析。
	if len(events) == 0 || events[0].Kind != EventStart {
		t.Fatalf("events = %+v, want 首个事件是 start", events)
	}
	for _, ev := range events {
		if ev.Kind == EventError {
			t.Errorf("非错误形状的 JSON 不应产出错误事件: %+v", ev)
		}
	}
}

// 干净 EOF 但没有任何终点信号 = 上游把回答断在了半截。EOF 侧必须把它与
// 「见到 message_stop 的正常收尾」区分开，否则半截文本带着 IsError=false
// 静默落库，用户以为模型说完了。
func TestStreamEOFWithoutTerminationIsError(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		// handler 正常返回 → chunked 终止块写出 → 客户端读到干净 EOF。
		// 全程没有 message_delta，也没有 message_stop。
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_fin","model":"x"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半截回答"}}`,
			`{"type":"content_block_stop","index":0}`,
		)
	})

	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(StreamEvent) error {
		return nil
	})
	if err == nil {
		t.Fatal("无终点信号的干净 EOF 应返回错误")
	}
	var unterm *streamUnterminatedError
	if !errors.As(err, &unterm) {
		t.Fatalf("err = %T %v, want *streamUnterminatedError", err, unterm)
	}
	if !strings.Contains(err.Error(), "未正常终止") {
		t.Errorf("错误信息应能区分「上游流未正常终止」，实际 %q", err.Error())
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("已下发事件后不应重试，attempts = %d, want 1", n)
	}
}

// 反向钉住：以 message_delta(end_turn) + message_stop 正常收尾的流仍是成功。
func TestStreamCleanTerminationStillSucceeds(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_ok","model":"x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"完整"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		)
	})

	var text strings.Builder
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(ev StreamEvent) error {
		if ev.Kind == EventText {
			text.WriteString(ev.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("正常收尾不应报错: %v", err)
	}
	if text.String() != "完整" {
		t.Errorf("text = %q, want 完整", text.String())
	}
}

// 端到端钉死回合语义：上游发半截内容后直接结束响应（FIN，无 message_stop），
// 回合必须以错误收尾、IsError=true、错误信息带可辨识标记——不能把半截当
// 完整回答成功落库。
//
// 变异守卫：删掉 parseSSE 的 terminated 校验，本条与
// TestStreamEOFWithoutTerminationIsError 即红。
func TestRunFailsTurnWhenUpstreamEndsWithoutTermination(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_half","model":"claude-x"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"接下来我看看"}}`,
			`{"type":"content_block_stop","index":0}`,
		)
	})

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Emit:      rec.emit,
		SessionID: "sess-half",
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "hi")})
	if err == nil {
		t.Fatal("半截流应让 Run 返回错误")
	}
	if res == nil {
		t.Fatal("Run 仍应返回结果供宿主收敛状态")
	}
	if !res.IsError {
		t.Error("半截流收尾必须 IsError=true，不能当完整回答")
	}

	frames := rec.frames(t)
	rf := resultFrame(t, frames)
	if isErr, _ := rf["is_error"].(bool); !isErr {
		t.Error("result 帧 is_error 必须为 true")
	}
	if sub, _ := rf["subtype"].(string); !strings.HasPrefix(sub, "error_") {
		t.Errorf("result.subtype = %q, want error_ 前缀（宿主靠它转 ai.error）", sub)
	}
	if txt, _ := rf["result"].(string); !strings.Contains(txt, "未正常终止") {
		t.Errorf("错误正文应含「未正常终止」标记，实际 %q", txt)
	}
}

// 限流是可重试的：第一次回 200+错误信封，第二次回正常流，用户不该看到失败。
func TestStreamRetriesUpstreamRateLimit(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"error":{"message":"Upstream rate limit exceeded, please retry later","type":"rate_limit_error"},"type":"error"}`)
			return
		}
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_retry","model":"glm-5.2"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	})

	got, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "glm-5.2"})
	if err != nil {
		t.Fatalf("限流后应重试成功: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("attempts = %d, want 2", n)
	}
	if len(got) == 0 || got[0].Kind != EventStart {
		t.Errorf("重试后应拿到正常事件流，实际 %+v", got)
	}
}

// 参数类错误重试也没用，必须一次就失败，并且把上游原因原样带给用户。
func TestStreamNoRetryOnUpstreamInvalidRequest(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"error":{"message":"model not found","type":"invalid_request_error"},"type":"error"}`)
	})

	_, err := collect(t, NewClient(srv.URL), &MessagesRequest{Model: "claude-x"})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("invalid_request_error 不应重试，attempts = %d", n)
	}
	var upErr *upstreamError
	if !errors.As(err, &upErr) || upErr.kind != "invalid_request_error" {
		t.Errorf("err = %v, want invalid_request_error upstreamError", err)
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("错误信息应带上游原文，实际 %q", err.Error())
	}
}

// 重试可观测：内核确定要重试时必须发一帧通知（经 WithRetryNotice），让宿主
// decoder markProgress、扛住 stale 扫描器 5min 判据。首尝试 503、第二次成功
// → 恰好一帧、attempt=1、类别对齐真 claude-code 的 server_error。
//
// 变异守卫：删掉 Stream 里 notifyRetry(attempt+1, err) 的可重试分支调用，本条即红。
func TestStreamRetryNoticeOnTransientFailureThenSuccess(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"upstream overloaded","type":"overloaded_error"},"type":"error"}`)
			return
		}
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_retry_ok","model":"x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"重试后成功"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	})

	var notices []RetryNotice
	var texts int
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(ev StreamEvent) error {
		if ev.Kind == EventText {
			texts++
		}
		return nil
	}, WithRetryNotice(func(n RetryNotice) { notices = append(notices, n) }))
	if err != nil {
		t.Fatalf("503 后重试应成功: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Fatalf("attempts = %d, want 2（503 + 重试成功）", n)
	}
	if texts != 1 {
		t.Errorf("text delta 数 = %d, want 1（重试成功的完整回合）", texts)
	}
	if len(notices) != 1 {
		t.Fatalf("retry 帧数 = %d, want 1", len(notices))
	}
	n := notices[0]
	if n.Attempt != 1 {
		t.Errorf("attempt = %d, want 1（首尝试失败后的第一次重试）", n.Attempt)
	}
	if n.ErrorStatus != http.StatusServiceUnavailable {
		t.Errorf("error_status = %d, want 503", n.ErrorStatus)
	}
	if n.ErrorKind != "server_error" {
		t.Errorf("error_kind = %q, want server_error", n.ErrorKind)
	}
	if n.Delay <= 0 {
		t.Errorf("delay = %v, want > 0", n.Delay)
	}
}

// 全部尝试都失败 → 帧数必须等于 maxRetries（不是 maxRetries+1）：最后一次
// 失败直接返回错误，没有「第 4 次重试」这回事。
func TestStreamRetryNoticeCountEqualsMaxRetriesOnExhaustion(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":{"message":"bad gateway","type":"api_error"},"type":"error"}`)
	})

	var notices []RetryNotice
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(StreamEvent) error {
		return nil
	}, WithRetryNotice(func(n RetryNotice) { notices = append(notices, n) }))
	if err == nil {
		t.Fatal("重试耗尽后应返回错误")
	}
	if n := atomic.LoadInt32(&attempts); n != int32(maxRetries)+1 {
		t.Fatalf("attempts = %d, want %d（maxRetries 次重试 + 首次）", n, maxRetries+1)
	}
	if len(notices) != maxRetries {
		t.Fatalf("retry 帧数 = %d, want %d（最后一次失败不发帧）", len(notices), maxRetries)
	}
	for i, n := range notices {
		if want := i + 1; n.Attempt != want {
			t.Errorf("notices[%d].Attempt = %d, want %d（重试序号从 1 连续递增）", i, n.Attempt, want)
		}
		if n.ErrorStatus != http.StatusBadGateway {
			t.Errorf("notices[%d].ErrorStatus = %d, want 502", i, n.ErrorStatus)
		}
		if n.ErrorKind != "server_error" {
			t.Errorf("notices[%d].ErrorKind = %q, want server_error（对齐真 claude-code 类别）", i, n.ErrorKind)
		}
		if n.Delay <= 0 {
			t.Errorf("notices[%d].Delay = %v, want > 0（宿主要靠它展示 retry_delay_ms）", i, n.Delay)
		}
	}
	last := notices[len(notices)-1]
	if last.Attempt != maxRetries {
		t.Errorf("最后一帧 attempt = %d, want %d", last.Attempt, maxRetries)
	}
}

// 非重试类失败（400 且与输出上限无关）一次就返回，一帧都不该发：没有重试
// 就没有重试通知，宿主不该看到幽灵进度。
func TestStreamNoRetryNoticeOnNonRetryableFailure(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error"},"type":"error"}`)
	})

	var notices []RetryNotice
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(StreamEvent) error {
		return nil
	}, WithRetryNotice(func(n RetryNotice) { notices = append(notices, n) }))
	if err == nil {
		t.Fatal("认证错误应直接失败")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("attempts = %d, want 1（401 不重试）", n)
	}
	if len(notices) != 0 {
		t.Fatalf("retry 帧数 = %d, want 0（未重试不发帧）", len(notices))
	}
}
