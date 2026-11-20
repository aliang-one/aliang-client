package services

// 纯图消息放行（2026-11-20 审计⑦）：ai.message 的 content 空守卫旧实现
// 「content 空即拒」在解析 attachments 之前开火——纯图消息（空文本+附件）
// 客户端放行、服务端必拒，气泡直接渲染英文 ai.error。修复口径=镜像 journal
// 写侧（2026-11-05 已改）「text 空且附件空才拒」：此处只看原始载荷在场，
// 实质解析仍归 resolveAgentAIAttachments（解析错误走它自己的 ai.error 分支）。
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestAgentAIMessageHasAttachments(t *testing.T) {
	if agentAIMessageHasAttachments(nil) {
		t.Fatal("nil 不算带附件")
	}
	if agentAIMessageHasAttachments("x") {
		t.Fatal("标量不算带附件")
	}
	if agentAIMessageHasAttachments([]interface{}{}) {
		t.Fatal("空数组不算带附件")
	}
	if !agentAIMessageHasAttachments([]interface{}{map[string]interface{}{"path": "/x/a.png"}}) {
		t.Fatal("非空附件数组算带附件")
	}
}

// TestMessagePureImageAccepted 端到端：content 空带一张真图 → 回合正常起跑
// 到 done、全程零 ai.error。修复前此形态在 attachments 解析前就被
// 「content is empty」拒死。
func TestMessagePureImageAccepted(t *testing.T) {
	setTestAgentHome(t)
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("图收到", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	defer func() { _ = up }()

	// 附件必须在 project_path 内（resolveAgentAIAttachments 的越界拒绝）：
	// 图直接落进项目目录。
	projectDir := t.TempDir()
	imgPath := filepath.Join(projectDir, "pure.png")
	if err := os.WriteFile(imgPath, agentCoreTinyPNG, 0o644); err != nil {
		t.Fatalf("写图片失败: %v", err)
	}
	m := newAgentAIManager()
	const sid = "s-pure-image"
	mu, events, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":   sid,
		"message_id":   "msg-pure-1",
		"content":      "",
		"attachments":  []interface{}{map[string]interface{}{"path": imgPath, "name": "pure.png", "type": "image"}},
		"provider":     "agentcore",
		"model":        "glm-5.2",
		"project_path": projectDir,
	}, writer)

	waitForAgentEvent(t, mu, events, models.AgentEventAIDone, nil)
	if errs := findAIFrames(mu, events, models.AgentEventAIError); len(errs) != 0 {
		t.Fatalf("纯图消息不应有 ai.error: %v", errs)
	}
}

// TestMessageEmptyContentNoAttachmentsRejected 反向钉：真正空消息（无附件）
// 依旧拒——守卫不许被放松到放行垃圾帧。
func TestMessageEmptyContentNoAttachmentsRejected(t *testing.T) {
	setTestAgentHome(t)
	m := newAgentAIManager()
	const sid = "s-empty-message"
	mu, events, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":   sid,
		"message_id":   "msg-empty-1",
		"content":      "",
		"provider":     "agentcore",
		"model":        "glm-5.2",
		"project_path": t.TempDir(),
	}, writer)

	waitForAgentEvent(t, mu, events, models.AgentEventAIError, func(e map[string]interface{}) bool {
		return strings.Contains(e["error"].(string), "content is empty")
	})
}
