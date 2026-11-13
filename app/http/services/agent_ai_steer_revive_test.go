package services

// steer 通道的「休眠会话复活」回归（2026-11-13）：agent 重启/升级后内存注册表
// 清空，手机对休眠会话插话命中 ai.steer 的 "ai session not found"——同一场景
// ai.message 走懒建+journal 历史恢复（spec §4.3），两条通道语义不一致。
// steerReviveViaMessage 把有 journal 留痕的 agentcore 会话插话转正为普通消息，
// 委托 message() 的懒建恢复路径；本文件钉死四条行为：
//  ① journal 无记录的真未知会话保持 not_running（不静默造会话）
//  ② 有 journal 头行（含 project_path）→ 转正：session 注册、steerRevived
//     notice + queued ack 双帧、历史装回、载荷无 project_path 也能跑（头行恢复）
//  ③ 头行缺 project_path 的旧 journal → 明确报错（revive_unsupported），不猜目录
//  ④ 非 agentcore 绑定（confirmed 指向 claude CLI）→ 不接管，保持 not_running

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// reviveTestManager 隔离 HOME/journal 根并关记忆提取（与 writechain 测试同款）。
func reviveTestManager(t *testing.T) *agentAIManager {
	t.Helper()
	setTestAgentHome(t)
	return newAgentAIManager()
}

// seedReviveJournal 造一个「重启前跑过」的 agentcore 会话 journal：头行带
// project_path/model，再落两行历史（user 提问 + assistant 回答）。
func seedReviveJournal(t *testing.T, m *agentAIManager, sid, proj string) {
	t.Helper()
	m.coreJournal.ensureSessionHeader(sid, proj, "glm-5.2", "high", "2026-11-13T00:00:00Z", "phone", nil, nil, "")
	m.coreJournal.appendUser(sid, "msg-old-1", "上次聊到哪了", "2026-11-13T00:01:00Z")
	m.coreJournal.appendAssistant(sid, "assistant_msg-old-1", "上次做完了 413 修复", "2026-11-13T00:02:00Z")
}

func steerAckFrom(t *testing.T, frames []map[string]interface{}) map[string]interface{} {
	t.Helper()
	for _, f := range frames {
		if f["type"] == models.AgentEventAISteerAck {
			return f
		}
	}
	return nil
}

// ① 真未知会话（journal 无记录、无绑定）不得被转正——保持原 not_running 报错。
// 这是防「静默造会话」的底线：没有磁盘证据的会话 id 插话就是错误输入。
func TestSteerReviveSkippedWhenJournalMissing(t *testing.T) {
	m := reviveTestManager(t)
	const sid = "s-revive-unknown"

	rec := &recordingWriter{}
	m.steer(map[string]interface{}{
		"type":       models.AgentEventAISteerAck,
		"session_id": sid,
		"message_id": "msg-steer-unknown",
		"content":    "继续",
	}, rec.write)

	ack := steerAckFrom(t, rec.snapshot())
	if ack == nil {
		t.Fatalf("无 ack 帧: %v", rec.snapshot())
	}
	if ack["result"] != "not_running" {
		t.Fatalf("result = %v, want not_running", ack["result"])
	}
	if !strings.Contains(ack["error"].(string), "ai session not found") {
		t.Fatalf("error = %v, want ai session not found", ack["error"])
	}
	// 会话不得被注册。
	m.mu.Lock()
	registered := m.sessions[sid] != nil
	m.mu.Unlock()
	if registered {
		t.Fatal("未知会话被静默注册了")
	}
}

// ② 主路径：休眠会话（journal 有头行+历史，内存无）插话 → 转正为 ai.message
// → 懒建注册 + notice + queued ack + journal 历史装回；载荷不带 project_path
// 时从头行恢复。模型未配置时回合会异步报错，不影响同步段断言。
func TestSteerReviveViaMessageRestoresSession(t *testing.T) {
	m := reviveTestManager(t)
	const sid = "s-revive-dormant"
	proj := t.TempDir()
	seedReviveJournal(t, m, sid, proj)

	rec := &recordingWriter{}
	m.steer(map[string]interface{}{
		"session_id": sid,
		"message_id": "msg-steer-revive",
		"content":    "继续上次的工作",
		// 载荷刻意不带 project_path：头行必须兜底（手机重发帧常缺字段）。
	}, rec.write)

	// 转正后 message() 同步段必须注册会话。
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatalf("休眠会话未被复活注册: %v", rec.snapshot())
	}
	if sess.projectPath != proj {
		// resolveAgentAICWD 做 symlink 解析（macOS /var → /private/var），头行存
		// 原路径、懒建落物理路径——两者必须是同一目录。
		if resolved, err := filepath.EvalSymlinks(proj); err != nil || resolved != sess.projectPath {
			t.Fatalf("projectPath = %q, want 头行的 %q", sess.projectPath, proj)
		}
	}
	if len(sess.history) < 2 {
		t.Fatalf("journal 历史未装回: %d 条", len(sess.history))
	}
	if sess.history[0].Content != "上次聊到哪了" {
		t.Fatalf("history[0] = %+v, want 恢复的 user 行", sess.history[0])
	}

	frames := rec.snapshot()
	ack := steerAckFrom(t, frames)
	if ack == nil || ack["result"] != "queued" {
		t.Fatalf("queued ack 缺失或错误: %v", ack)
	}
	var hasNotice bool
	for _, f := range frames {
		if f["type"] == models.AgentEventAIRunNotice && f["code"] == "steer_revived_as_message" {
			hasNotice = true
		}
	}
	if !hasNotice {
		t.Fatalf("缺 steer_revived_as_message notice: %v", frames)
	}
	// 转正帧落 journal 的 user 行用原 message_id（回放对账锚点）。
	m.mu.Lock()
	last := sess.history[len(sess.history)-1]
	m.mu.Unlock()
	if last.Role != "user" || last.MessageID != "msg-steer-revive" {
		t.Fatalf("新消息未按原 id 入史: %+v", last)
	}
}

// ③ 头行缺 project_path 的旧 journal：无法安全定位工作目录，明确报错而不是拿
// 当前帧猜（可能落到错误目录执行）。
func TestSteerReviveRejectsHeadWithoutProjectPath(t *testing.T) {
	m := reviveTestManager(t)
	const sid = "s-revive-no-proj"
	// 手写无 project_path 的头行（ensureSessionHeader 必带，直写文件）。
	m.coreJournal.ensureSessionHeader(sid, t.TempDir(), "", "", "", "", nil, nil, "")
	data, err := os.ReadFile(filepath.Join(m.coreJournal.dir, agentCoreJournalFileName(sid)))
	if err != nil {
		t.Fatalf("读 journal: %v", err)
	}
	lines := strings.SplitN(string(data), "\n", 2)
	var head map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("解析头行: %v", err)
	}
	delete(head, "project_path")
	rewritten, _ := jsonMarshalLine(head)
	body := ""
	if len(lines) > 1 {
		body = lines[1]
	}
	if err := os.WriteFile(filepath.Join(m.coreJournal.dir, agentCoreJournalFileName(sid)),
		append(rewritten, body...), 0o600); err != nil {
		t.Fatalf("重写头行: %v", err)
	}

	rec := &recordingWriter{}
	m.steer(map[string]interface{}{
		"session_id": sid,
		"message_id": "msg-steer-noproj",
		"content":    "继续",
	}, rec.write)

	ack := steerAckFrom(t, rec.snapshot())
	if ack == nil || ack["result"] != "error" || ack["code"] != "revive_unsupported" {
		t.Fatalf("期望 revive_unsupported 报错: %v", ack)
	}
}

// ④ 绑定表 confirmed 指向 claude CLI 的会话同样复活：provider 从绑定表取
// claude、绑定原生 id 供种 resume（≠ stranded）。断言三点：会话被注册、
// provider 族 = claude、resume 锚 = 绑定的原生 id。
func TestSteerReviveUsesBindingProviderForClaude(t *testing.T) {
	m := reviveTestManager(t)
	const sid = "s-revive-claude"
	proj := t.TempDir()
	seedReviveJournal(t, m, sid, proj)
	m.mu.Lock()
	m.bindings[sid] = agentAIBindingRecord{
		ConversationID:  sid,
		Provider:        "claude",
		NativeSessionID: "native-claude-1",
		State:           "confirmed",
	}
	m.mu.Unlock()

	rec := &recordingWriter{}
	m.steer(map[string]interface{}{
		"session_id": sid,
		"message_id": "msg-steer-claude",
		"content":    "继续",
	}, rec.write)

	m.mu.Lock()
	sess := m.sessions[sid]
	resume := ""
	if sess != nil {
		resume = sess.resumeSessionID
	}
	m.mu.Unlock()
	if sess == nil {
		t.Fatalf("claude 绑定的休眠会话未被复活: %v", rec.snapshot())
	}
	if !providersEquivalent(sess.provider, "claude") {
		t.Fatalf("provider = %q, want claude（绑定权威）", sess.provider)
	}
	if resume != "native-claude-1" {
		t.Fatalf("resumeSessionID = %q, want 绑定原生 id native-claude-1", resume)
	}
}
