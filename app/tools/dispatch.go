package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SafeCall runs a tool handler with panic recovery. A panic becomes the
// family error payload (OnError) or the generic <event>.result error shape —
// a panicking handler must never take down the WS read loop's goroutine pool
// with an unrecovered panic or return nothing. A nil result map is converted
// to a tool_returned_nil error payload so null never flows to the wire.
func SafeCall(t *Tool, ctx context.Context, msg map[string]interface{}) (res map[string]interface{}) {
	requestID := requestIDOf(msg)
	defer func() {
		if r := recover(); r != nil {
			res = safeOnError(t, requestID, fmt.Errorf("%v", r))
		}
	}()
	res = t.Handler(ctx, msg)
	if res == nil {
		return genericErrorPayload(t, requestID, "tool_returned_nil")
	}
	return res
}

// RunWithGuards wraps SafeCall with the registry-level execution guards:
// per-tool timeout watchdog + output-size cap. The guard window is a real
// cancellation signal: on timeout (tool_timeout) or parent/session cancel
// (tool_cancelled) the handler's ctx goes Done, so bounded-but-slow handlers
// (peer_exec 这类真实远端调用) stop their local wait instead of orphaning.
func RunWithGuards(ctx context.Context, t *Tool, msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := requestIDOf(msg)
	timeout := time.Duration(t.Caps.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// guardCtx 把护栏窗口变成 handler 可感知的 ctx：超时不再只是「调用方
	// 放弃后孤儿 goroutine 继续跑」，handler（peer_exec 这类真实远端调用）
	// 会在窗口到期/会话取消时收到取消信号（此前五个 peer handler 用
	// context.Background() 自立门户，run 停了也照跑最长 630s）。
	guardCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan map[string]interface{}, 1)
	go func() { done <- SafeCall(t, guardCtx, msg) }()
	select {
	case res := <-done:
		_ = writeJSON(enforceOutputCap(t, res, requestID))
	case <-guardCtx.Done():
		if err := ctx.Err(); err != nil {
			_ = writeJSON(safeOnError(t, requestID, errors.New("tool_cancelled")))
			return
		}
		_ = writeJSON(safeOnError(t, requestID, fmt.Errorf("tool_timeout")))
	}
}

// enforceOutputCap replaces an oversized result with an error payload (flagged
// truncated:true). The spec's "merge truncated into the top level" is realized
// as replacement here: oversized payloads are single-blob fields where merging
// would keep the oversized bytes — replacement is the only honest cap. A
// result that cannot be JSON-serialized at all is reported as
// output_not_serializable without the truncated flag — it was not cut short,
// it was never wire-shaped.
func enforceOutputCap(t *Tool, res map[string]interface{}, requestID string) map[string]interface{} {
	limit := t.Caps.MaxOutputBytes
	if limit <= 0 {
		limit = 32768
	}
	raw, mErr := json.Marshal(res)
	if mErr == nil && len(raw) <= limit {
		return res
	}
	err := fmt.Errorf("output_truncated")
	if mErr != nil {
		err = fmt.Errorf("output_not_serializable")
	}
	payload := safeOnError(t, requestID, err)
	if mErr == nil {
		payload["truncated"] = true
	}
	return payload
}

// safeOnError invokes the tool's family error payload builder with panic
// recovery — a panicking OnError must degrade to the generic payload, never
// take down the process (an unrecovered panic in the dispatch goroutine would
// kill the whole agent).
func safeOnError(t *Tool, requestID string, err error) (payload map[string]interface{}) {
	if t.OnError == nil {
		return genericErrorPayload(t, requestID, err.Error())
	}
	defer func() {
		if r := recover(); r != nil {
			payload = genericErrorPayload(t, requestID, err.Error())
		}
	}()
	return t.OnError(requestID, err)
}

func genericErrorPayload(t *Tool, requestID, errMsg string) map[string]interface{} {
	return map[string]interface{}{
		"type":       t.Event + ".result",
		"request_id": requestID,
		"error":      errMsg,
	}
}

func requestIDOf(msg map[string]interface{}) string {
	if msg == nil {
		return ""
	}
	if s, ok := msg["request_id"].(string); ok {
		return s
	}
	return ""
}
