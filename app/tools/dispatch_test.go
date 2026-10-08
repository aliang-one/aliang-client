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

func TestDispatch_OnErrorPanicsFallsBackToGeneric(t *testing.T) {
	tool := &Tool{ID: "y", Event: "x.y", Description: "d",
		Handler: func(map[string]interface{}) map[string]interface{} { panic("kaboom") },
		OnError: func(requestID string, err error) map[string]interface{} {
			panic("OnError exploded")
		}}
	res := SafeCall(tool, map[string]interface{}{"request_id": "r6"})
	if res["error"] != "kaboom" {
		t.Fatalf("panicking OnError should fall back to generic payload, got %v", res)
	}
	if res["type"] != "x.y.result" || res["request_id"] != "r6" {
		t.Fatalf("generic fallback shape wrong: %v", res)
	}
}

func TestRequestIDOf_MissingKeyReturnsEmpty(t *testing.T) {
	if got := requestIDOf(map[string]interface{}{}); got != "" {
		t.Fatalf("missing request_id should be empty, got %q", got)
	}
	if got := requestIDOf(map[string]interface{}{"request_id": nil}); got != "" {
		t.Fatalf("nil request_id should be empty, got %q", got)
	}
	if got := requestIDOf(map[string]interface{}{"request_id": 42}); got != "" {
		t.Fatalf("non-string request_id should be empty, got %q", got)
	}
}

func TestDispatch_NilResultBecomesErrorPayload(t *testing.T) {
	tool := &Tool{ID: "n", Event: "x.n", Description: "d",
		Handler: func(map[string]interface{}) map[string]interface{} { return nil }}
	res := SafeCall(tool, map[string]interface{}{"request_id": "r7"})
	if res["error"] != "tool_returned_nil" {
		t.Fatalf("nil handler result should become error payload, got %v", res)
	}
	if res["request_id"] != "r7" {
		t.Fatalf("request_id should be preserved, got %v", res)
	}
}

func TestRunWithGuards_UnserializableOutputReportsError(t *testing.T) {
	bad := &Tool{ID: "bad", Event: "x.bad", Description: "d",
		Caps: Caps{TimeoutMs: 5000, MaxOutputBytes: 65536},
		Handler: func(map[string]interface{}) map[string]interface{} {
			return map[string]interface{}{"type": "x.bad.result", "ch": make(chan int)}
		}}
	var wrote map[string]interface{}
	RunWithGuards(bad, map[string]interface{}{"request_id": "r8"}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if wrote["error"] != "output_not_serializable" {
		t.Fatalf("expected output_not_serializable, got %v", wrote)
	}
	if _, has := wrote["truncated"]; has {
		t.Fatalf("marshal failure must not set truncated flag, got %v", wrote)
	}
}
