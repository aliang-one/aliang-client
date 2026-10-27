package services

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 2026-10-27 收窄后的口径：手机行为契约只随「注入正在运行的 TUI」的文本下发，
// headless 通道（claude -p / codex / agentcore）一律不注入。

func TestWrapAgentAIPhoneContractForSurface(t *testing.T) {
	content := "帮我看看这个仓库"
	if got := wrapAgentAIPhoneContractForSurface("", content); got != content {
		t.Fatalf("非 phone 面不应注入契约, got %.60s", got)
	}
	if got := wrapAgentAIPhoneContractForSurface("tui", content); got != content {
		t.Fatalf("未知 surface 不应注入契约, got %.60s", got)
	}
	got := wrapAgentAIPhoneContractForSurface(agentAISurfacePhone, content)
	if !strings.Contains(got, agentAIPhoneContractMarker) || !strings.HasSuffix(got, content) {
		t.Fatalf("phone 面应注入契约并保留原文, got %.60s", got)
	}
	if again := wrapAgentAIPhoneContractForSurface(agentAISurfacePhone, got); again != got {
		t.Fatal("二次注入应幂等")
	}
}

// TestRunUserMessageHeadlessPromptOmitsPhoneContract 锁死 bug 修复：surface=phone
// 的 headless 回合（claude -p 的 freshPrompt 与 --resume prompt）不得把
// system-reminder 契约塞进用户内容——那会被写进原生 transcript。
func TestRunUserMessageHeadlessPromptOmitsPhoneContract(t *testing.T) {
	cases := []struct {
		name   string
		resume string
	}{
		{"fresh", ""},
		{"resume", "native-sess-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binDir := t.TempDir()
			argvFile := filepath.Join(dir, "argv.txt")
			// 只在真实回合（带 --print）时记录 argv；--help/--version 探针不落盘，
			// 避免与 spawn 抢占同一个文件。
			writeFakeExecutable(t, binDir, "claude",
				"#!/bin/sh\n"+
					"case \"$*\" in *--print*) printf '%s\\n' \"$@\" > "+argvFile+" ;; esac\n"+
					"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"session_id\":\"headless-sess\"}'\n")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HOME", t.TempDir()) // 无 live TUI 记录 → 不会命中注入路由

			m := newAgentAIManager()
			defer m.closeAll()
			var mu sync.Mutex
			write := func(v any) error {
				mu.Lock()
				defer mu.Unlock()
				return nil
			}
			session := &agentAISession{
				id: "sess-h-" + tc.name, projectPath: t.TempDir(), provider: "claude",
				resumeSessionID: tc.resume, mode: "vibe", surface: agentAISurfacePhone,
			}
			m.registerAISessionLocked(session)
			emitter := m.runEmitter(agentAIRun{runID: "run-h", sessionID: session.id, messageID: "m-h"}, write)

			if err := m.runUserMessage(session, "run-h", "m-h", "手机消息", "claude", nil, nil, emitter, nil); err != nil {
				t.Fatalf("runUserMessage error = %v", err)
			}

			var argv string
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if raw, err := os.ReadFile(argvFile); err == nil && strings.Contains(string(raw), "--print") {
					argv = string(raw)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if argv == "" {
				t.Fatal("headless 回合没有启动（argv 未落盘）")
			}
			if strings.Contains(argv, agentAIPhoneContractMarker) {
				t.Fatalf("headless -p 提示词不应带手机契约:\n%s", argv)
			}
			if !strings.Contains(argv, "手机消息") {
				t.Fatalf("观测点无效：argv 里没有用户内容:\n%s", argv)
			}
		})
	}
}

// TestRunUserMessageTUIInjectionCarriesPhoneContract 锁死保留侧：TUI 注入回合
// （另一个进程里的 CC 看不到宿主设置）必须把契约随注入文本带进去，而会话
// history 只存用户原文。
func TestRunUserMessageTUIInjectionCarriesPhoneContract(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":`+jsonString(content)+`}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":`+jsonString("收到："+content)+`}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	writeTranscriptFixture(t, home, "sess-1")
	t.Setenv("HOME", home)

	m := newAgentAIManager()
	var mu sync.Mutex
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		return nil
	}
	session := &agentAISession{
		id: "sess-1", projectPath: t.TempDir(), provider: "claude",
		resumeSessionID: "sess-1", mode: "vibe", surface: agentAISurfacePhone,
	}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-c", sessionID: "sess-1", messageID: "m-c"}, write)

	if err := m.runUserMessage(session, "run-c", "m-c", "手机消息", "claude", nil, nil, emitter, nil); err != nil {
		t.Fatalf("runUserMessage error = %v", err)
	}

	select {
	case frame := <-frames:
		if !strings.Contains(frame, agentAIPhoneContractMarker) {
			t.Fatalf("TUI 注入帧缺少手机契约: %.200s", frame)
		}
		if !strings.Contains(frame, "手机消息") {
			t.Fatalf("TUI 注入帧缺少用户内容: %.200s", frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未注入任何帧——路由没走到 TUI 执行器")
	}

	m.mu.Lock()
	last := agentAIMessage{}
	if n := len(session.history); n > 0 {
		last = session.history[n-1]
	}
	m.mu.Unlock()
	if last.Content != "手机消息" {
		t.Fatalf("history 应只存原文（契约不进历史）, got %q", last.Content)
	}
}

// TestSteerTUITurnInjectionCarriesPhoneContract 锁死插话侧：注册表内 TUI 回合的
// steer 之前传的是裸 content，契约漏注入；修复后注入帧带契约、ack 仍 queued、
// history 只存原文。
func TestSteerTUITurnInjectionCarriesPhoneContract(t *testing.T) {
	pid := os.Getpid()
	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {})
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var mu sync.Mutex
	var acks []map[string]any
	session := &agentAISession{
		id: "sess-1", projectPath: t.TempDir(), provider: "claude",
		resumeSessionID: "sess-1", surface: agentAISurfacePhone,
	}
	m.registerAISessionLocked(session)
	m.mu.Lock()
	session.cancel = func() {}
	session.tuiTurn = &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}
	m.mu.Unlock()

	m.steer(map[string]interface{}{
		"session_id": "sess-1", "message_id": "m-1", "content": "插话",
	}, func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			acks = append(acks, p)
		}
		return nil
	})

	mu.Lock()
	if len(acks) != 1 || acks[0]["result"] != "queued" {
		mu.Unlock()
		t.Fatalf("ack = %v, want result=queued", acks)
	}
	mu.Unlock()

	select {
	case frame := <-frames:
		if !strings.Contains(frame, agentAIPhoneContractMarker) {
			t.Fatalf("TUI 插话帧缺少手机契约: %.200s", frame)
		}
		if !strings.Contains(frame, "插话") {
			t.Fatalf("TUI 插话帧缺少内容: %.200s", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未注入任何帧")
	}

	m.mu.Lock()
	n := len(session.history)
	last := agentAIMessage{}
	if n > 0 {
		last = session.history[n-1]
	}
	m.mu.Unlock()
	if n != 1 || last.Content != "插话" {
		t.Fatalf("history 应只存原文, n=%d last=%q", n, last.Content)
	}
}
