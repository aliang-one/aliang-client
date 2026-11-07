package meshproto

import (
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
