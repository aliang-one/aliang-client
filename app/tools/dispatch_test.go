package tools

import (
	"strings"
	"testing"
	"time"
)

func okPayload(msg map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "x.y.result", "request_id": msg["request_id"], "ok": true}
}

func TestDispatch_PanicBecomesErrorPayload(t *testing.T) {
	tool := &Tool{ID: "boom", Event: "x.boom", Description: "d",
		Handler: func(map[string]interface{}) map[string]interface{} { panic("kaboom") }}
	res := SafeCall(tool, map[string]interface{}{"request_id": "r1"})
	if res["error"] == nil || res["request_id"] != "r1" {
		t.Fatalf("panic should convert to error payload, got %v", res)
	}
	if res["type"] != "x.boom.result" {
		t.Fatalf("error payload type = %v", res["type"])
	}
}

func TestDispatch_OnErrorPreferred(t *testing.T) {
	tool := &Tool{ID: "f", Event: "file.x", Description: "d",
		Handler: func(map[string]interface{}) map[string]interface{} { panic("kaboom") },
		OnError: func(requestID string, err error) map[string]interface{} {
			return map[string]interface{}{"type": "file.error", "request_id": requestID, "error": "family:" + err.Error()}
		}}
	res := SafeCall(tool, map[string]interface{}{"request_id": "r2"})
	if res["error"] != "family:kaboom" {
		t.Fatalf("OnError payload not used: %v", res)
	}
}

func TestRunWithGuards_Timeout(t *testing.T) {
	slow := &Tool{ID: "slow", Event: "x.slow", Description: "d",
		Caps: Caps{TimeoutMs: 50},
		Handler: func(map[string]interface{}) map[string]interface{} {
			time.Sleep(2 * time.Second)
			return okPayload(nil)
		}}
	var wrote map[string]interface{}
	RunWithGuards(slow, map[string]interface{}{"request_id": "r3"}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if wrote["error"] != "tool_timeout" {
		t.Fatalf("expected tool_timeout, got %v", wrote)
	}
}

func TestRunWithGuards_OutputCapTruncates(t *testing.T) {
	big := &Tool{ID: "big", Event: "x.big", Description: "d",
		Caps: Caps{TimeoutMs: 5000, MaxOutputBytes: 64},
		Handler: func(map[string]interface{}) map[string]interface{} {
			return map[string]interface{}{"type": "x.big.result", "blob": strings.Repeat("x", 4096)}
		}}
	var wrote map[string]interface{}
	RunWithGuards(big, map[string]interface{}{"request_id": "r4"}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if wrote["error"] != "output_truncated" || wrote["truncated"] != true {
		t.Fatalf("expected output_truncated + truncated flag, got %v", wrote)
	}
}

func TestRunWithGuards_NormalPath(t *testing.T) {
	tool := &Tool{ID: "ok", Event: "x.ok", Description: "d",
		Caps:    Caps{TimeoutMs: 5000, MaxOutputBytes: 65536},
		Handler: okPayload}
	var wrote map[string]interface{}
	RunWithGuards(tool, map[string]interface{}{"request_id": "r5"}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if wrote["ok"] != true {
		t.Fatalf("normal path should pass through, got %v", wrote)
	}
}
