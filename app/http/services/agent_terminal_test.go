package services

import (
	"bytes"
	"testing"
	"time"
)

func TestTerminalOutputMeter_WatchPacedStreamNeverTrips(t *testing.T) {
	// A continuous, human-paced command such as `watch` emits a few KB every
	// couple of seconds. It must stream indefinitely (within the idle timeout)
	// without tripping the flood limiter.
	meter := newTerminalOutputMeter()
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 600; i++ { // ~20 minutes of `watch -n 2` at 4 KB/refresh
		now := base.Add(time.Duration(i*2) * time.Second)
		if meter.add(4096, now) {
			t.Fatalf("watch-paced output tripped the limiter at tick %d (total=%d bytes)", i, meter.total)
		}
	}
}

func TestTerminalOutputMeter_BurstFloodTripsWithinWindow(t *testing.T) {
	// A runaway command dumping ~1 MB chunks back-to-back must be stopped once
	// the sustained rate exceeds the per-window threshold.
	meter := newTerminalOutputMeter()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ { // 8 MiB exactly == limit, not yet over
		if meter.add(1<<20, now) {
			t.Fatalf("limiter tripped too early after %d MiB", i+1)
		}
	}
	if !meter.add(1<<20, now) { // 9 MiB in the window -> over the 8 MiB threshold
		t.Fatalf("expected the limiter to trip on a sustained >%d-byte burst", agentTerminalOutputRateBytes)
	}
}

func TestTerminalOutputMeter_RateWindowSlides(t *testing.T) {
	meter := newTerminalOutputMeter()
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	// 7 MiB at t=0 stays under the 8 MiB window limit.
	if meter.add(7<<20, base) {
		t.Fatalf("7 MiB should not trip the limiter")
	}
	// Another 7 MiB at t=6s: the t=0 sample has aged out of the 5s window, so
	// the recent total resets and the stream survives.
	if meter.add(7<<20, base.Add(6*time.Second)) {
		t.Fatalf("limiter should have slid its window; old bytes must expire")
	}
	// 2 MiB more within the new window -> 9 MiB recent -> trip.
	if !meter.add(2<<20, base.Add(6*time.Second)) {
		t.Fatalf("expected the limiter to trip once recent output exceeds the window threshold")
	}
}

func TestTerminalOutputMeter_LifetimeCapBackstop(t *testing.T) {
	// With a tiny lifetime cap and a huge rate, only the cap path can trip.
	meter := &terminalOutputMeter{
		window:  5 * time.Second,
		rateMax: 1 << 30,
		capMax:  100,
	}
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	if meter.add(60, now) {
		t.Fatalf("60 bytes is under the 100-byte cap")
	}
	if !meter.add(50, now) { // 110 > 100
		t.Fatalf("expected the lifetime cap backstop to trip")
	}
}

func TestTerminalOutputEncoder_KeepsRuneBoundariesAcrossFrames(t *testing.T) {
	enc := newTerminalOutputEncoder()

	// "héllo" with é = 0xC3 0xA9, split across two reads.
	first := enc.push([]byte("h\xc3"))
	if string(first) != "h" {
		t.Fatalf("first chunk = %q, want %q (é lead byte must be carried)", first, "h")
	}
	if len(enc.carry) != 1 || enc.carry[0] != 0xc3 {
		t.Fatalf("carry = % x, want [c3]", enc.carry)
	}
	second := enc.push([]byte("\xa9llo"))
	if string(second) != "éllo" {
		t.Fatalf("second chunk = %q, want %q", second, "éllo")
	}
	if len(enc.carry) != 0 {
		t.Fatalf("carry should be empty after completing the rune, got % x", enc.carry)
	}
}

func TestTerminalOutputEncoder_ThreeByteRuneSplit(t *testing.T) {
	// '€' = 0xE2 0x82 0xAC (3 bytes), split 2|1.
	enc := newTerminalOutputEncoder()
	if got := enc.push([]byte("x\xe2\x82")); string(got) != "x" {
		t.Fatalf("chunk = %q, want %q", got, "x")
	}
	if got := enc.push([]byte("\xacz")); string(got) != "€z" {
		t.Fatalf("chunk = %q, want %q", got, "€z")
	}
}

func TestTerminalOutputEncoder_PreservesInvalidBytesInline(t *testing.T) {
	enc := newTerminalOutputEncoder()
	// 0xFF is an invalid UTF-8 start byte; it must be emitted as-is (becoming
	// U+FFFD on marshal) rather than buffered or dropped, and it must not poison
	// a following valid rune.
	out := enc.push([]byte("a\xffb"))
	if !bytes.Equal(out, []byte("a\xffb")) {
		t.Fatalf("output = %q, want %q", out, "a\xffb")
	}
	if len(enc.carry) != 0 {
		t.Fatalf("no carry expected after fully-invalid input, got % x", enc.carry)
	}
}

func TestTerminalOutputEncoder_PassesValidStreamUntouched(t *testing.T) {
	enc := newTerminalOutputEncoder()
	// A run of complete, valid runes must pass through with no carry.
	in := []byte("hello 世界 🚀 terminal")
	out := enc.push(in)
	if !bytes.Equal(out, in) {
		t.Fatalf("valid stream was altered: got %q want %q", out, in)
	}
	if len(enc.carry) != 0 {
		t.Fatalf("unexpected carry % x", enc.carry)
	}
}
