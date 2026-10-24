package services

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 外部 TUI 会话 steer(2026-10-24):导入/桌面终端自发生长的 Claude Code 回合
// 不在 agent 注册表,steer() 此前在第一道门就 ack not_running。现在凭
// ~/.claude/sessions/<pid>.json 的 live busy 记录 + MessagingSocketPath
// (CC >= 2.1.224)把插话经 CC 跨会话 UDS 收件箱排队——与 stop 的外部 SIGINT
// 同一份注册表,与 TUI 注入回合(steerTUITurn)同 ack 语义。
//
// 注:held 观测窗 3s 内无 held 标记才放行,注入类用例各 ~3s。

// externalSteerFixture 与 externalInterruptFixture 同构,但记录带 busy/idle
// 状态和指向测试 UDS 的 messagingSocketPath(ccPeerShortSocketPath 只生成
// 路径,startFakeTUI 才绑定,所以路径可以先于 fixture 确定)。
func externalSteerFixture(t *testing.T, sid, status, sock string) string {
	t.Helper()
	extra := `"status":"` + status + `","entrypoint":"cli","messagingSocketPath":"` + sock + `"`
	return externalInterruptFixture(t, sid, livePidRecord(t, sid, extra))
}

// stubExternalTargetMatches 放开 pid 身份门(测试进程不是真 claude TUI,
// 与 stubExternalInterrupt 同一旋钮)。
func stubExternalTargetMatches(t *testing.T) {
	t.Helper()
	previous := externalInterruptTargetMatches
	externalInterruptTargetMatches = func(int) bool { return true }
	t.Cleanup(func() { externalInterruptTargetMatches = previous })
}

// dispatchExternalSteer sends one ai.steer through the manager, capturing acks.
func dispatchExternalSteer(t *testing.T, payload map[string]interface{}) []map[string]any {
	t.Helper()
	m := newAgentAIManager()
	var mu sync.Mutex
	var acks []map[string]any
	m.steer(payload, func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			acks = append(acks, p)
		}
		return nil
	})
	return acks
}

func requireSingleAck(t *testing.T, acks []map[string]any, wantResult string) map[string]any {
	t.Helper()
	require.Len(t, acks, 1)
	require.Equal(t, wantResult, acks[0]["result"])
	return acks[0]
}

func TestExternalSteerQueuedIntoSocket(t *testing.T) {
	const sid = "ext-steer-queued"
	stubExternalTargetMatches(t)
	sock := ccPeerShortSocketPath(t)
	home := externalSteerFixture(t, sid, "busy", sock)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {})
	writeTranscriptFixture(t, home, sid)

	acks := dispatchExternalSteer(t, map[string]interface{}{
		"session_id": "ai_unknown_row", "message_id": "m-1", "content": "外部插话",
		"source_session_id": sid,
	})
	requireSingleAck(t, acks, "queued")
	select {
	case frame := <-frames:
		if !strings.Contains(frame, "外部插话") {
			t.Fatalf("注入帧缺少内容: %s", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未注入任何帧")
	}
}

func TestExternalSteerHeldReported(t *testing.T) {
	const sid = "ext-steer-held"
	stubExternalTargetMatches(t)
	sock := ccPeerShortSocketPath(t)
	home := externalSteerFixture(t, sid, "busy", sock)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {
		// ccPeerWatchHeld 以字节匹配「Held peer message」,原样写一行。
		appendTranscriptLine(t, transcriptFixturePath(home, sid), `{"note":"Held peer message"}`)
	})
	writeTranscriptFixture(t, home, sid)

	acks := dispatchExternalSteer(t, map[string]interface{}{
		"session_id": "ai_unknown_row", "message_id": "m-2", "content": "会被扣留的插话",
		"source_session_id": sid,
	})
	ack := requireSingleAck(t, acks, "held")
	require.NotEmpty(t, ack["error"])
}

func TestExternalSteerIdleRecordNotRunning(t *testing.T) {
	const sid = "ext-steer-idle"
	stubExternalTargetMatches(t)
	sock := ccPeerShortSocketPath(t)
	home := externalSteerFixture(t, sid, "idle", sock)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {})
	writeTranscriptFixture(t, home, sid)

	acks := dispatchExternalSteer(t, map[string]interface{}{
		"session_id": "ai_unknown_row", "message_id": "m-3", "content": "插话",
		"source_session_id": sid,
	})
	// idle 记录 = 回合已结束,应落回 not_running(手机走普通新消息),不得注入。
	ack := requireSingleAck(t, acks, "not_running")
	require.Contains(t, ack["error"], "not found")
	select {
	case <-frames:
		t.Fatal("idle 记录不应注入任何帧")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestExternalSteerUnknownSessionNotRunning(t *testing.T) {
	_ = externalInterruptFixture(t, "ext-steer-ghost", "")

	acks := dispatchExternalSteer(t, map[string]interface{}{
		"session_id": "ai_unknown_row", "message_id": "m-4", "content": "插话",
		"source_session_id": "no-such-native-id",
	})
	requireSingleAck(t, acks, "not_running")
}

func TestExternalSteerAttachmentsRejected(t *testing.T) {
	const sid = "ext-steer-attach"
	stubExternalTargetMatches(t)
	sock := ccPeerShortSocketPath(t)
	home := externalSteerFixture(t, sid, "busy", sock)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {})
	writeTranscriptFixture(t, home, sid)

	acks := dispatchExternalSteer(t, map[string]interface{}{
		"session_id": "ai_unknown_row", "message_id": "m-5", "content": "带图插话",
		"source_session_id": sid,
		"attachments":       []interface{}{map[string]interface{}{"name": "a.png"}},
	})
	ack := requireSingleAck(t, acks, "error")
	require.Equal(t, "unsupported_attachment", ack["code"])
	select {
	case <-frames:
		t.Fatal("附件被拒后不应注入任何帧")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCapabilitiesAdvertiseExternalSteerFlag(t *testing.T) {
	// 能力位与实现解耦(纯函数):hasClaudeExternalSteer=true → ai_steer_external
	// 在列;false → 不在列。版本探测(CC >= 2.1.224)由 agentAICapabilities 的
	// probeClaudeCodeVersion 分支负责,不在这里重复。
	withFlag := agentAICapabilitiesForTools(true, false, false, false, false, true, true)
	assert.Contains(t, withFlag, "ai_steer_external")
	withoutFlag := agentAICapabilitiesForTools(true, false, false, false, false, true, false)
	assert.NotContains(t, withoutFlag, "ai_steer_external")
}
