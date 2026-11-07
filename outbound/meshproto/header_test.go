package meshproto

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHeaderEncodeParseRoundTrip(t *testing.T) {
	h := Header{
		From:   "dev-a",
		Class:  ClassAI,
		Target: "api.aliang.one:443",
		Via:    []string{"dev-a"},
		Hops:   0,
	}
	line, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Fatal("encoded header must end with newline")
	}
	got, consumed, err := Parse([]byte(line))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if consumed != len(line) {
		t.Fatalf("consumed = %d, want %d", consumed, len(line))
	}
	if got.From != h.From || got.Class != h.Class || got.Target != h.Target {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestParseRejectsOversizedHeader(t *testing.T) {
	long := strings.Repeat("x", MaxHeaderBytes)
	if _, _, err := Parse([]byte(long + "\n")); err == nil {
		t.Fatal("expected error for oversized header")
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, _, err := Parse([]byte("not-json\n")); err == nil {
		t.Fatal("expected error for malformed header")
	}
}

func TestAppendHopDetectsLoop(t *testing.T) {
	h := Header{From: "dev-a", Via: []string{"dev-a"}, Hops: 0}
	if err := h.AppendHop("dev-b", MaxHopsCap); err != nil {
		t.Fatalf("AppendHop dev-b: %v", err)
	}
	if err := h.AppendHop("dev-b", MaxHopsCap); err == nil {
		t.Fatal("expected loop error when appending duplicate device")
	}
}

func TestAppendHopEnforcesHopsLimit(t *testing.T) {
	h := Header{Via: []string{"dev-a"}, Hops: MaxHopsCap}
	if err := h.AppendHop("dev-b", MaxHopsCap); err == nil {
		t.Fatal("expected hops-limit error")
	}
}

func TestClassCarrierAllowed(t *testing.T) {
	// 用户自定义流量禁止走中继载体(design §4)
	if ClassCarrierAllowed(ClassCustom, CarrierRelay) {
		t.Fatal("custom class must not be allowed on relay carrier")
	}
	for _, c := range []string{ClassSelf, ClassAI, ClassCustom} {
		if !ClassCarrierAllowed(c, CarrierLAN) {
			t.Fatalf("class %q must be allowed on LAN carrier", c)
		}
	}
}

func TestParseRejectsNegativeHops(t *testing.T) {
	// 线上伪造 {"hops":-N} 不得绕过跳数上限
	line := `{"from":"dev-a","class":"ai","target":"t:1","hops":-1,"via":["dev-a"]}` + "\n"
	if _, _, err := Parse([]byte(line)); err == nil {
		t.Fatal("expected error for negative hops")
	}
}

func TestParseRejectsControlBytes(t *testing.T) {
	// JSON 转义可把控制字节(<0x20)送进 From/Class/Target;
	// design §7 依赖防环日志可读,不得让对端往日志注换行。
	for _, field := range []string{"from", "class", "target"} {
		m := map[string]any{
			"from":   "dev-a",
			"class":  "ai",
			"target": "t:1",
			"via":    []string{"dev-a"},
			"hops":   0,
		}
		m[field] = "bad\nvalue"
		line, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal %s: %v", field, err)
		}
		if _, _, err := Parse(append(line, '\n')); err == nil {
			t.Fatalf("expected control-byte error for field %s", field)
		}
	}
}

func TestEncodeRejectsOversizedHeader(t *testing.T) {
	h := Header{From: "dev-a", Class: ClassAI, Target: strings.Repeat("t", MaxHeaderBytes)}
	if _, err := h.Encode(); err == nil {
		t.Fatal("expected error for oversized header encode")
	}
}

func TestParseErrorBranches(t *testing.T) {
	// 4097 例手拼:Encode 本身会拒超限,产不出该行;tpl 量出固定开销后用 Target 填充到精确长度。
	tpl, err := json.Marshal(Header{From: "dev-a", Class: ClassAI, Target: "", Via: []string{"dev-a"}})
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	pad := MaxHeaderBytes - len(tpl) - 1

	mkLine := func(targetPad int) []byte {
		b, err := json.Marshal(Header{
			From:   "dev-a",
			Class:  ClassAI,
			Target: strings.Repeat("p", targetPad),
			Via:    []string{"dev-a"},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return append(b, '\n')
	}

	cases := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"no newline within limit", []byte("not-json"), "incomplete"},
		{"no newline over limit", bytes.Repeat([]byte("x"), MaxHeaderBytes+1), "exceeds"},
		{"total 4097 rejected", mkLine(pad + 1), "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseAcceptsExactMaxHeader(t *testing.T) {
	// 总长恰 4096(含 \n)必须接受(Encode/Parse 双侧边界 >,不是 >=)
	tpl, err := json.Marshal(Header{From: "dev-a", Class: ClassAI, Target: "", Via: []string{"dev-a"}})
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	pad := MaxHeaderBytes - len(tpl) - 1

	target := strings.Repeat("p", pad)
	line, err := (&Header{From: "dev-a", Class: ClassAI, Target: target, Via: []string{"dev-a"}}).Encode()
	if err != nil {
		t.Fatalf("Encode exact max: %v", err)
	}
	if len(line) != MaxHeaderBytes {
		t.Fatalf("line len = %d, want exactly %d", len(line), MaxHeaderBytes)
	}
	got, consumed, err := Parse([]byte(line))
	if err != nil {
		t.Fatalf("Parse exact max: %v", err)
	}
	if consumed != len(line) {
		t.Fatalf("consumed = %d, want %d", consumed, len(line))
	}
	if got.Target != target {
		t.Fatal("target mismatch")
	}
}

func TestViaNilDefaultsToFrom(t *testing.T) {
	// Encode 侧:Via=nil 自动补 [From]
	h := Header{From: "dev-a", Class: ClassAI, Target: "api.aliang.one:443"}
	line, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(line, `"via":["dev-a"]`) {
		t.Fatalf("encoded line = %s, want via auto-filled [from]", line)
	}
	// Parse 侧:线上省略 via 字段同样自动补 [From]
	got, _, err := Parse([]byte(`{"from":"dev-a","class":"ai","target":"t:1","hops":0}` + "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Via) != 1 || got.Via[0] != "dev-a" {
		t.Fatalf("parsed via = %v, want [dev-a]", got.Via)
	}
}
