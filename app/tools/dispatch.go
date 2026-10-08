package tools

import (
	"encoding/json"
	"fmt"
	"time"
)

// SafeCall runs a tool handler with panic recovery. A panic becomes the
// family error payload (OnError) or the generic <event>.result error shape —
// a panicking handler must never take down the WS read loop's goroutine pool
// with an unrecovered panic or return nothing.
func SafeCall(t *Tool, msg map[string]interface{}) (res map[string]interface{}) {
	requestID := requestIDOf(msg)
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("%v", r)
			if t.OnError != nil {
				res = t.OnError(requestID, err)
				return
			}
			res = genericErrorPayload(t, requestID, err.Error())
		}
	}()
	return t.Handler(msg)
}

// RunWithGuards wraps SafeCall with the registry-level execution guards:
// per-tool timeout watchdog + serialized output-size cap. A slow handler's
// goroutine is abandoned (handlers are internally bounded — fixed-subcommand
// git probes, bounded walks, byte-capped reads); the caller gets a
// tool_timeout payload instead of hanging the tool loop.
func RunWithGuards(t *Tool, msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := requestIDOf(msg)
	done := make(chan map[string]interface{}, 1)
	go func() { done <- SafeCall(t, msg) }()
	timeout := time.Duration(t.Caps.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	select {
	case res := <-done:
		_ = writeJSON(enforceOutputCap(t, res, requestID))
	case <-time.After(timeout):
		if t.OnError != nil {
			_ = writeJSON(t.OnError(requestID, fmt.Errorf("tool_timeout")))
			return
		}
		_ = writeJSON(genericErrorPayload(t, requestID, "tool_timeout"))
	}
}

// enforceOutputCap replaces an oversized result with an error payload (flagged
// truncated:true). The spec's "merge truncated into the top level" is realized
// as replacement here: oversized payloads are single-blob fields where merging
// would keep the oversized bytes — replacement is the only honest cap.
func enforceOutputCap(t *Tool, res map[string]interface{}, requestID string) map[string]interface{} {
	limit := t.Caps.MaxOutputBytes
	if limit <= 0 {
		limit = 32768
	}
	raw, err := json.Marshal(res)
	if err == nil && len(raw) <= limit {
		return res
	}
	var payload map[string]interface{}
	if t.OnError != nil {
		payload = t.OnError(requestID, fmt.Errorf("output_truncated"))
	} else {
		payload = genericErrorPayload(t, requestID, "output_truncated")
	}
	payload["truncated"] = true
	return payload
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
	return fmt.Sprint(msg["request_id"])
}
