package services

// 会话级审批模式的「解析值 → 内存会话」写入链回归(2026-11-09):ai.session.create
// 新建/既有两分支与 ai.message 此前都只解析 approval_mode 不落内存,唯一消费者
// run.approvalMode 从 session 拷贝,恒空串 → 换挡对执行层零影响;且 create 既有
// 分支的 journal 同步被裹在 MCP/技能键门禁里,纯换挡连持久化都不落;ack 回显
// 读陈旧内存把前端 chip 静默弹回「默认推荐」。本文件锁死三条路的内存写入、
// journal 落盘与 preserve/显式清空语义。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 隔离 journal 落盘:newAgentAIManager 在 Setenv 之后构造,journal 目录随 HOME
// 落进临时目录(与 agent_ai_agentcore_run_test 同款隔离)。
func newIsolatedApprovalModeManager(t *testing.T) *agentAIManager {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return newAgentAIManager()
}

func ensureApprovalModeJournalHead(t *testing.T, m *agentAIManager, sid, approvalMode string) {
	t.Helper()
	m.coreJournal.ensureSessionHeader(sid, t.TempDir(), "glm-5.2", "high", "2026-11-09T00:00:00Z", "dashboard", nil, nil, approvalMode)
}

func readApprovalModeJournalHead(t *testing.T, m *agentAIManager, sid string) string {
	t.Helper()
	head, err := readAgentCoreJournalHeaderFrom(m.coreJournal.dir, sid)
	if err != nil {
		t.Fatalf("读 journal 头行失败: %v", err)
	}
	return head.approvalMode
}

func approvalModeFromCreatedAck(t *testing.T, mu *sync.Mutex, events *[]map[string]interface{}) string {
	t.Helper()
	created := findAIFrames(mu, events, models.AgentEventAISessionCreated)
	if len(created) != 1 {
		t.Fatalf("expect exactly one ai.session.created, got %d", len(created))
	}
	v, _ := created[0]["approval_mode"].(string)
	return v
}

// ① create 新建分支:载荷带 bypass_all 必须落内存(否则 run.approvalMode 恒空,
// 执行层永远走默认挡),ack 回显同步带值。
func TestCreateNewSessionAppliesApprovalMode(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-create-new"

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":    sid,
		"provider":      "agentcore",
		"project_path":  t.TempDir(),
		"approval_mode": "bypass_all",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("create 新建未注册会话")
	}
	if sess.approvalMode != "bypass_all" {
		t.Fatalf("create 新建未落内存 approvalMode: %q", sess.approvalMode)
	}
	if got := approvalModeFromCreatedAck(t, mu, events); got != "bypass_all" {
		t.Fatalf("ack 回显 approval_mode = %q, want bypass_all(陈旧回显会把前端 chip 弹回默认)", got)
	}
}

// ② create 既有分支(聊天页每条消息前重发的形态):不带 MCP/技能键的纯换挡
// 必须同时落内存与 journal 头行——此前内存不写、journal 同步被
// disabledMCPServersSet||disabledSkillsSet 门禁挡死。
func TestCreateExistingSessionAppliesApprovalModeAndJournal(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-create-existing"
	proj := t.TempDir()
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", projectPath: proj}
	m.mu.Unlock()
	ensureApprovalModeJournalHead(t, m, sid, "")

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":    sid,
		"provider":      "agentcore",
		"project_path":  proj,
		"approval_mode": "ask_all",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.approvalMode != "ask_all" {
		t.Fatalf("create 既有分支未落内存 approvalMode: %q", sess.approvalMode)
	}
	if got := approvalModeFromCreatedAck(t, mu, events); got != "ask_all" {
		t.Fatalf("ack 回显 approval_mode = %q, want ask_all", got)
	}
	if got := readApprovalModeJournalHead(t, m, sid); got != "ask_all" {
		t.Fatalf("纯换挡未同步 journal 头行: %q(被 MCP/技能键门禁挡住)", got)
	}
}

// ③ ai.message 换挡:解析值必须写进既有会话内存(此前只同步 journal 不落内存,
// run.approvalMode 从 session 拷到的是陈旧空串)。无 model 的回合会异步报
// 「未指定模型」,但字段赋值在同步段,断言不受影响。
func TestMessageAppliesApprovalMode(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-message"
	proj := t.TempDir()
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", projectPath: proj}
	m.mu.Unlock()
	ensureApprovalModeJournalHead(t, m, sid, "")

	_, _, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":    sid,
		"message_id":    "msg-approval-1",
		"content":       "hi",
		"provider":      "agentcore",
		"project_path":  proj,
		"approval_mode": "bypass_all",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.approvalMode != "bypass_all" {
		t.Fatalf("ai.message 未落内存 approvalMode: %q", sess.approvalMode)
	}
	if got := readApprovalModeJournalHead(t, m, sid); got != "bypass_all" {
		t.Fatalf("ai.message 未同步 journal 头行: %q", got)
	}
}

// ④ preserve 守卫:不带键的 create(老客户端/壳重发形态)不得把既有挡位冲成
// 默认。本用例在修复前即通过(缺陷形态是「从不写入」,天然不冲值),作用是
// 防止修复时把赋值写成无条件覆写。
func TestCreateWithoutApprovalModeKeyPreservesExisting(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-preserve"
	proj := t.TempDir()
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", projectPath: proj, approvalMode: "bypass_all"}
	m.mu.Unlock()
	ensureApprovalModeJournalHead(t, m, sid, "bypass_all")

	_, _, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":   sid,
		"provider":     "agentcore",
		"project_path": proj,
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.approvalMode != "bypass_all" {
		t.Fatalf("缺键 create 冲掉了既有挡位: %q", sess.approvalMode)
	}
	if got := readApprovalModeJournalHead(t, m, sid); got != "bypass_all" {
		t.Fatalf("缺键 create 冲掉了 journal 头行挡位: %q", got)
	}
}

// ⑤ 显式空串 = 切回默认推荐:合法带键,必须清内存与 journal 头行。
func TestCreateExplicitEmptyApprovalModeClears(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-clear"
	proj := t.TempDir()
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", projectPath: proj, approvalMode: "bypass_all"}
	m.mu.Unlock()
	ensureApprovalModeJournalHead(t, m, sid, "bypass_all")

	_, _, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":    sid,
		"provider":      "agentcore",
		"project_path":  proj,
		"approval_mode": "",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.approvalMode != "" {
		t.Fatalf("显式空串未清内存挡位: %q", sess.approvalMode)
	}
	if got := readApprovalModeJournalHead(t, m, sid); got != "" {
		t.Fatalf("显式空串未清 journal 头行挡位: %q", got)
	}
}

// ⑥ 重启兜底:agent 重启后 create 重建会话、载荷不带键时,必须从 journal 头行
// 回填挡位(message() 懒建分支一直有此回填,create 侧此前缺失→重启即静默回默认)。
func TestCreateRestartBackfillApprovalModeFromJournalHead(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-backfill"
	ensureApprovalModeJournalHead(t, m, sid, "ask_all")

	_, _, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":   sid,
		"provider":     "agentcore",
		"project_path": t.TempDir(),
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("create 未注册会话")
	}
	if sess.approvalMode != "ask_all" {
		t.Fatalf("重启后 create 未从 journal 头行回填挡位: %q", sess.approvalMode)
	}
}

// ⑦ 上报面:覆盖层快照必须携带挡位——hello/sync 与 VibeSessions 列表是手机端
// 唯一实时回显通道,此前 Model/Effort 都填而 ApprovalMode 漏填,手机永远看不到
// 桌面端换挡(2026-11-09 手机同步展示补链)。
func TestVibeSessionsSnapshotCarriesApprovalMode(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-snapshot"
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", approvalMode: "bypass_all"}
	m.mu.Unlock()

	items := m.vibeSessionsSnapshot(true) // includeIdle 纳管 create-only 会话
	var found *models.AgentVibeSession
	for i := range items {
		if items[i].ID == sid {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatal("快照未包含目标会话")
	}
	if found.ApprovalMode != "bypass_all" {
		t.Fatalf("快照未携带 approval_mode: %q(手机端将永远看到旧挡位)", found.ApprovalMode)
	}
}

// ⑦b 快照 JSON 契约:approval_mode 不带 omitempty——显式空串(切回默认)必须
// 出现在上报里,否则上游「缺键=保存量」契约会把桌面端的重置洗回旧挡位。
func TestVibeSessionsSnapshotApprovalModeEmptyStillSerialized(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-snapshot-empty"
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore"}
	m.mu.Unlock()

	items := m.vibeSessionsSnapshot(true)
	var found *models.AgentVibeSession
	for i := range items {
		if items[i].ID == sid {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatal("快照未包含目标会话")
	}
	raw, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"approval_mode":""`) {
		t.Fatalf("空挡位必须恒序列化(去 omitempty 契约): %s", raw)
	}
}

// ③b ai.message 显式空串:切回默认必须清内存与 journal(换挡主要通道之一,
// 与 create 同语义)。
func TestMessageExplicitEmptyApprovalModeClears(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-msg-clear"
	proj := t.TempDir()
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", projectPath: proj, approvalMode: "bypass_all"}
	m.mu.Unlock()
	ensureApprovalModeJournalHead(t, m, sid, "bypass_all")

	_, _, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":    sid,
		"message_id":    "msg-approval-clear",
		"content":       "hi",
		"provider":      "agentcore",
		"project_path":  proj,
		"approval_mode": "",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.approvalMode != "" {
		t.Fatalf("ai.message 显式空串未清内存挡位: %q", sess.approvalMode)
	}
	if got := readApprovalModeJournalHead(t, m, sid); got != "" {
		t.Fatalf("ai.message 显式空串未清 journal 头行: %q", got)
	}
}

// ③c ai.message 缺键:preserve 语义,不得冲掉既有挡位(普通回合不带键,零影响)。
func TestMessageWithoutApprovalModeKeyPreserves(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	const sid = "s-approval-msg-preserve"
	proj := t.TempDir()
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", projectPath: proj, approvalMode: "ask_all"}
	m.mu.Unlock()
	ensureApprovalModeJournalHead(t, m, sid, "ask_all")

	_, _, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":   sid,
		"message_id":   "msg-approval-preserve",
		"content":      "hi",
		"provider":     "agentcore",
		"project_path": proj,
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.approvalMode != "ask_all" {
		t.Fatalf("ai.message 缺键冲掉了既有挡位: %q", sess.approvalMode)
	}
	if got := readApprovalModeJournalHead(t, m, sid); got != "ask_all" {
		t.Fatalf("ai.message 缺键冲掉了 journal 头行: %q", got)
	}
}

// 兜底保证:journal 目录确实随 HOME 落进临时目录(隔离护栏自检,与用例逻辑
// 无关;防止未来 agentHome 解析改动把测试写穿到真机目录)。
func TestApprovalModeWritechainJournalDirIsolated(t *testing.T) {
	m := newIsolatedApprovalModeManager(t)
	if m.coreJournal.dir == "" {
		t.Skip("journal 目录解析为空=禁用,无需隔离")
	}
	if !filepath.IsAbs(m.coreJournal.dir) {
		t.Fatalf("journal 目录应为绝对路径: %q", m.coreJournal.dir)
	}
	if _, err := os.Stat(m.coreJournal.dir); err == nil {
		if real, rerr := filepath.EvalSymlinks(m.coreJournal.dir); rerr == nil {
			homeReal, _ := filepath.EvalSymlinks(os.TempDir())
			if homeReal != "" && !filepath.HasPrefix(real, homeReal) && real != homeReal {
				t.Fatalf("journal 目录逃出临时区: %q", real)
			}
		}
	}
}
