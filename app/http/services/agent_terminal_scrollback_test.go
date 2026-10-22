package services

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeAgentTerminalScrollbackLimit(t *testing.T) {
	if got := normalizeAgentTerminalScrollbackLimit(0); got != agentTerminalScrollbackDefaultBytes {
		t.Fatalf("normalize(0) = %d, want default %d", got, agentTerminalScrollbackDefaultBytes)
	}
	if got := normalizeAgentTerminalScrollbackLimit(-1); got != agentTerminalScrollbackDefaultBytes {
		t.Fatalf("normalize(-1) = %d, want default", got)
	}
	if got := normalizeAgentTerminalScrollbackLimit(1024); got != 1024 {
		t.Fatalf("normalize(1024) = %d, want 1024", got)
	}
	if got := normalizeAgentTerminalScrollbackLimit(agentTerminalScrollbackMaxBytes); got != agentTerminalScrollbackMaxBytes {
		t.Fatalf("normalize(cap) = %d, want the cap itself %d", got, agentTerminalScrollbackMaxBytes)
	}
	if got := normalizeAgentTerminalScrollbackLimit(1 << 30); got != agentTerminalScrollbackMaxBytes {
		t.Fatalf("normalize(huge) = %d, want cap %d", got, agentTerminalScrollbackMaxBytes)
	}
}

func TestAgentTerminalScrollbackLiveSession(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(4096)
	ring.push([]byte("hello scrollback"))
	m.sessions["s1"] = &agentTerminalSession{id: "s1", ring: ring}

	got, ok := m.scrollbackSnapshot("s1", 0)
	if !ok {
		t.Fatal("expected live session snapshot")
	}
	if got.Status != terminalReplayStatusLive || got.Data != "hello scrollback" || got.Truncated {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Encoding != "text" || got.SessionID != "s1" {
		t.Fatalf("unexpected envelope: %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.CapturedAt); err != nil {
		t.Fatalf("captured_at not RFC3339: %v", err)
	}
}

func TestAgentTerminalScrollbackTombstone(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(4096)
	ring.push([]byte("old output"))
	m.history["s2"] = &agentTerminalHistory{shell: "bash", exitedAt: time.Now(), ring: ring}

	got, ok := m.scrollbackSnapshot("s2", 0)
	if !ok || got.Status != terminalReplayStatusExited || got.Data != "old output" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestAgentTerminalScrollbackMissing(t *testing.T) {
	m := newAgentTerminalManager()
	if _, ok := m.scrollbackSnapshot("nope", 0); ok {
		t.Fatal("expected ok=false for unknown session")
	}
}

func TestAgentTerminalScrollbackTruncatesTailAndKeepsUTF8(t *testing.T) {
	ring := newTerminalRingBuffer(8192)
	payload := strings.Repeat("你", 40) // 120 bytes of 3-byte runes
	ring.push([]byte(payload))

	m := newAgentTerminalManager()
	m.sessions["s3"] = &agentTerminalSession{id: "s3", ring: ring}

	got, ok := m.scrollbackSnapshot("s3", 50)
	if !ok {
		t.Fatal("expected snapshot")
	}
	if !got.Truncated {
		t.Fatalf("expected truncated=true, got %+v", got)
	}
	// The tail-keep cut lands at offset 70 — mid-rune (rune boundaries sit at
	// 0 mod 3) — so the trim loop must drop the 2 partial bytes: 50-2 == 48.
	if len(got.Data) != 48 {
		t.Fatalf("data len %d, want 48 (limit 50 minus 2 partial rune bytes)", len(got.Data))
	}
	if !utf8.ValidString(got.Data) {
		t.Fatalf("data must be valid UTF-8, got %q", got.Data)
	}
	if !strings.HasSuffix(payload, got.Data) {
		t.Fatalf("data %q must be a suffix of payload", got.Data)
	}
}

func TestAgentTerminalScrollbackConcurrentWithOutput(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(1 << 16)
	session := &agentTerminalSession{id: "s4", ring: ring}
	m.sessions["s4"] = session
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			// Mirror the production writer (copy loop): outputGate held
			// across each ring push, so the snapshot path genuinely
			// contends the gate instead of racing around it.
			session.outputGate.Lock()
			session.ring.push([]byte("0123456789"))
			session.outputGate.Unlock()
		}
	}()
	for i := 0; i < 50; i++ {
		if _, ok := m.scrollbackSnapshot("s4", 0); !ok {
			t.Fatal("expected snapshot while output streaming")
		}
	}
	<-done
}

// 仪表盘 scrollback 是纯文本视图，ANSI 颜色/光标控制码必须滤成可读文本
// （2026-10-22 用户实测「[0m」字面量糊满输出）。手机端 replay 不走这里。
func TestAgentTerminalScrollbackStripsANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"sgr颜色", "\x1b[32mgreen\x1b[0m plain", "green plain"},
		{"CSI光标与清屏", "\x1b[2J\x1b[Hready", "ready"},
		{"OSC标题-BEL", "\x1b]0;my title\x07after", "after"},
		{"OSC标题-ST", "\x1b]2;my title\x1b\\after", "after"},
		{"字符集声明", "\x1b(Bok", "ok"},
		{"CRLF归一", "a\r\nb", "a\nb"},
		{"裸CR丢弃", "25%\r50%\r75%\n", "25%50%75%\n"},
		{"孤立ESC丢弃", "ok\x1b", "ok"},
		{"无转义原样", "plain output\n", "plain output\n"},
		{"真实提示符", "liang@dev:~$ \x1b[0m\x1b[?2004h", "liang@dev:~$ "},
	}
	m := newAgentTerminalManager()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := newTerminalRingBuffer(1 << 16)
			ring.push([]byte(tc.in))
			m.sessions["s-ansi"] = &agentTerminalSession{id: "s-ansi", ring: ring}
			got, ok := m.scrollbackSnapshot("s-ansi", 0)
			if !ok {
				t.Fatalf("snapshot failed")
			}
			if got.Data != tc.want {
				t.Fatalf("data = %q, want %q", got.Data, tc.want)
			}
			if got.Encoding != "text" {
				t.Fatalf("encoding = %q, want text", got.Encoding)
			}
			delete(m.sessions, "s-ansi")
		})
	}
}
