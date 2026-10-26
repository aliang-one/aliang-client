package services

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
)

// 宿主侧的图片接线。
//
// 内核测试（app/agentcore/image_test.go）已经证明「给内核一个 image 块，它会
// 正确序列化」。本文件锁的是更前面的一段：**宿主有没有把手机的图片变成那个
// 块**。这两件事失败时的表现完全一样——模型回一句「我看不到图片」——所以必须
// 分开钉。
//
// 三条通路各锁一组：
//
//	1. agentCoreMessages：本轮附件（run.attachments）→ 最后一条 user 消息的图块
//	2. agentCoreSteerMessage：运行中插话携带的图片 → 队列 → 下一轮上下文
//	3. runAgentCore 端到端：上游请求体里真的有那个块

// agentCoreTinyPNG 是带 NUL 的假 PNG 头。宿主不解码像素，只做 base64，假字节
// 足够；带 NUL 是为了同时证明「图片路径没有走文本二进制探测」。
var agentCoreTinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}

// writeAgentCoreImage 在临时目录里落一张图，返回绝对路径。
func writeAgentCoreImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("写图片失败: %v", err)
	}
	return p
}

// imageBlocksOf 从消息的 Content 里取出所有 image 块。
//
// Content 是 interface{}：纯文本消息是 string，块消息是 []ContentBlock。
// 这里只认后者，前者返回 nil——这本身就是断言的一部分。
func imageBlocksOf(t *testing.T, msg agentcore.Message) []agentcore.ContentBlock {
	t.Helper()
	blocks, ok := msg.Content.([]agentcore.ContentBlock)
	if !ok {
		return nil
	}
	var out []agentcore.ContentBlock
	for _, b := range blocks {
		if b.Type == agentcore.BlockTypeImage {
			out = append(out, b)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// agentCoreMessages：本轮附件
// ---------------------------------------------------------------------------

// TestAgentCoreMessagesAttachesImageToLastUserMessage 是最常见的那条路：
// 用户带着一张图说了一句话。
//
// 形状要求：最后一条 user 消息被升级成块消息，**文字在前、图片在后**。顺序
// 反了模型会先看到图再读指令，容易答非所问。
func TestAgentCoreMessagesAttachesImageToLastUserMessage(t *testing.T) {
	m := newAgentAIManager()
	const sessionID = "conv-image-user-last"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:     sessionID,
		runSeq: runSeq,
		history: []agentAIMessage{
			{Role: "user", Content: "先看下代码"},
			{Role: "assistant", Content: "好的"},
			{Role: "user", Content: "这张图里的报错是什么意思"},
		},
	})

	path := writeAgentCoreImage(t, "shot.png", agentCoreTinyPNG)
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		attachments: []agentAIAttachment{{Type: "image", Name: "shot.png", Path: path}},
	}

	msgs := m.agentCoreMessages(run)
	if len(msgs) != 3 {
		t.Fatalf("消息数 = %d, want 3（历史三条，图不新增消息）", len(msgs))
	}

	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("末条角色 = %q, want user", last.Role)
	}
	blocks, ok := last.Content.([]agentcore.ContentBlock)
	if !ok {
		t.Fatalf("带图的末条消息应当是块消息，实际 %T", last.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("块数 = %d, want 2（文字 + 图片）", len(blocks))
	}
	if blocks[0].Type != agentcore.BlockTypeText || !strings.Contains(blocks[0].Text, "报错") {
		t.Errorf("首块 = %+v, want 用户原文的文本块", blocks[0])
	}
	if blocks[1].Type != agentcore.BlockTypeImage || blocks[1].Source == nil {
		t.Fatalf("次块 = %+v, want 图片块", blocks[1])
	}
	if blocks[1].Source.MediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", blocks[1].Source.MediaType)
	}
	if blocks[1].Source.Data != base64.StdEncoding.EncodeToString(agentCoreTinyPNG) {
		t.Error("source.data 不是文件的 base64")
	}
	// 编码成功时清单里不该再出现图片路径：模型会照着去 read，白跑一轮。
	if strings.Contains(blocks[0].Text, path) {
		t.Errorf("图片已经进 image 块，文本里不该再列路径: %q", blocks[0].Text)
	}
}

// TestAgentCoreMessagesAppendsImageWhenHistoryEndsWithAssistant 覆盖「历史以
// assistant 收尾」的情形（续聊后用户只发图）。
//
// Anthropic 不接受 assistant 消息里的 image 块，所以不能硬塞进末条——必须补
// 一条只含图片的 user 消息，否则整个请求会被上游 400，整轮对话一起拖死。
func TestAgentCoreMessagesAppendsImageWhenHistoryEndsWithAssistant(t *testing.T) {
	m := newAgentAIManager()
	const sessionID = "conv-image-assistant-last"
	const runSeq = 2
	m.registerAISessionLocked(&agentAISession{
		id:     sessionID,
		runSeq: runSeq,
		history: []agentAIMessage{
			{Role: "user", Content: "写个函数"},
			{Role: "assistant", Content: "写好了"},
		},
	})

	path := writeAgentCoreImage(t, "a.jpg", []byte("jpeg-bytes"))
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		attachments: []agentAIAttachment{{Type: "image", Name: "a.jpg", Path: path}},
	}

	msgs := m.agentCoreMessages(run)
	if len(msgs) != 3 {
		t.Fatalf("消息数 = %d, want 3（历史两条 + 新增的图片消息）", len(msgs))
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("assistant 消息被改动了: %+v", msgs[1])
	}
	if _, ok := msgs[1].Content.(string); !ok {
		t.Errorf("assistant 消息不该被改成块消息，实际 %T", msgs[1].Content)
	}
	appended := msgs[2]
	if appended.Role != "user" {
		t.Fatalf("追加消息角色 = %q, want user", appended.Role)
	}
	blocks, ok := appended.Content.([]agentcore.ContentBlock)
	if !ok {
		t.Fatalf("追加消息应当是块消息，实际 %T", appended.Content)
	}
	// 没有文字可拼时不该造一个空 text 块（上游会拒）。
	if len(blocks) != 1 || blocks[0].Type != agentcore.BlockTypeImage {
		t.Fatalf("块 = %+v, want 单个图片块", blocks)
	}
	if blocks[0].Source.MediaType != "image/jpeg" {
		t.Errorf("media_type = %q, want image/jpeg", blocks[0].Source.MediaType)
	}
}

// TestAgentCoreMessagesDegradesWhenImageUnreadable 是降级路径。
//
// 图片读不出来（下载被清、临时文件被删、权限不足）时，整轮 run 不能因此挂掉：
// 用户至少该拿到文字回复。降级后图片路径要进清单——内核的 Read 已经支持图片，
// 模型还能自己按路径去取，而不是彻底看不见。
func TestAgentCoreMessagesDegradesWhenImageUnreadable(t *testing.T) {
	m := newAgentAIManager()
	const sessionID = "conv-image-missing"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "看看这张图"}},
	})

	missing := filepath.Join(t.TempDir(), "gone.png")
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		attachments: []agentAIAttachment{{Type: "image", Name: "gone.png", Path: missing}},
	}

	msgs := m.agentCoreMessages(run)
	if len(msgs) != 1 {
		t.Fatalf("消息数 = %d, want 1（降级不该新增消息）", len(msgs))
	}
	if imgs := imageBlocksOf(t, msgs[0]); len(imgs) != 0 {
		t.Fatalf("读不出来时不该有图片块，实际 %d 个", len(imgs))
	}
	text, ok := msgs[0].Content.(string)
	if !ok {
		t.Fatalf("降级后应当是纯文本消息，实际 %T", msgs[0].Content)
	}
	if !strings.Contains(text, "看看这张图") {
		t.Errorf("用户原文丢了: %q", text)
	}
	// 关键：降级后路径必须出现在清单里，模型才有第二次机会。
	if !strings.Contains(text, missing) {
		t.Errorf("降级后应把图片路径列进清单，实际 %q", text)
	}
}

// TestAgentCoreMessagesWithoutAttachmentsKeepsStringContent 是形状回归守卫。
//
// 没有附件时所有消息必须还是纯字符串。升级成数组不会报错，但会改变每一次
// 普通对话的请求形状——不必要的面。
func TestAgentCoreMessagesWithoutAttachmentsKeepsStringContent(t *testing.T) {
	m := newAgentAIManager()
	const sessionID = "conv-image-none"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:     sessionID,
		runSeq: runSeq,
		history: []agentAIMessage{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好"},
		},
	})

	msgs := m.agentCoreMessages(agentAIRun{sessionID: sessionID, runSeq: runSeq})
	if len(msgs) != 2 {
		t.Fatalf("消息数 = %d, want 2", len(msgs))
	}
	for i, msg := range msgs {
		if _, ok := msg.Content.(string); !ok {
			t.Errorf("第 %d 条 content 应当是 string，实际 %T", i, msg.Content)
		}
	}
}

// ---------------------------------------------------------------------------
// agentCoreSteerMessage：运行中插话带图
// ---------------------------------------------------------------------------

// TestAgentCoreSteerMessageCarriesImage 锁的是「插话里的图片不会在队列里丢」。
//
// 图片在 steer 入口就已经 base64 编码（见 agentAISteerMessage.ImageBlocks 的
// 注释），所以队列里只可能丢在转换这一步——之前正是如此。
func TestAgentCoreSteerMessageCarriesImage(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(agentCoreTinyPNG)
	log := &agentCoreEventLog{}
	control := newAgentAICoreSteerControl(agentAIRun{sessionID: "conv-steer-image", runSeq: 1}, log.write)

	if _, err := control.deliver(agentAISteerMessage{
		MessageID: "steer-img",
		Content:   "看这张图，按它的样式改",
		ImageBlocks: []map[string]interface{}{{
			"type": "image",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": "image/png",
				"data":       encoded,
			},
		}},
	}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	msgs := control.drain()
	if len(msgs) != 1 {
		t.Fatalf("drain 取到 %d 条，want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "user" {
		t.Fatalf("插话角色 = %q, want user", msg.Role)
	}
	blocks, ok := msg.Content.([]agentcore.ContentBlock)
	if !ok {
		t.Fatalf("带图插话应当是块消息，实际 %T", msg.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("块数 = %d, want 2（文字 + 图片）", len(blocks))
	}
	if blocks[0].Type != agentcore.BlockTypeText || blocks[0].Text != "看这张图，按它的样式改" {
		t.Errorf("首块 = %+v, want 插话原文的文本块", blocks[0])
	}
	if blocks[1].Type != agentcore.BlockTypeImage || blocks[1].Source == nil {
		t.Fatalf("次块 = %+v, want 图片块", blocks[1])
	}
	if blocks[1].Source.MediaType != "image/png" || blocks[1].Source.Data != encoded {
		t.Errorf("图片 source = %+v, 内容与投递时不一致", blocks[1].Source)
	}
}

// TestAgentCoreSteerMessageTextOnlyStaysString 是形状回归守卫：
// 纯文字插话必须仍是 TextMessage，与加入图片支持之前完全一致。
func TestAgentCoreSteerMessageTextOnlyStaysString(t *testing.T) {
	msg := agentCoreSteerMessage(agentAISteerMessage{MessageID: "steer-txt", Content: "顺便跑下测试"})
	if msg.Role != "user" {
		t.Errorf("角色 = %q, want user", msg.Role)
	}
	text, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("纯文字插话应当是 string，实际 %T", msg.Content)
	}
	if text != "顺便跑下测试" {
		t.Errorf("内容 = %q", text)
	}
}

// TestAgentCoreSteerMessageDropsBlockWithoutSource 覆盖畸形输入。
//
// 没有 source 的 image 块发出去只会变成 {"type":"image"} 这种上游认不出的
// 东西，整条请求 400。宁可丢掉那一块、保住文字。
func TestAgentCoreSteerMessageDropsBlockWithoutSource(t *testing.T) {
	msg := agentCoreSteerMessage(agentAISteerMessage{
		MessageID:   "steer-bad",
		Content:     "还是这句话",
		ImageBlocks: []map[string]interface{}{{"type": "image"}},
	})
	if text, ok := msg.Content.(string); !ok || text != "还是这句话" {
		t.Fatalf("无 source 的块应被丢弃、只留文字，实际 %T %+v", msg.Content, msg.Content)
	}
}

// ---------------------------------------------------------------------------
// 端到端：图片真的进了上游请求体
// ---------------------------------------------------------------------------

// TestRunAgentCoreSendsImageAttachmentUpstream 是最关键的一条。
//
// 事件流看起来正常、历史也正常，但请求体里没有图片块——模型照样「看不到图」。
// 只有在这里断言上游收到的 MessagesRequest 才能钉住。
func TestRunAgentCoreSendsImageAttachmentUpstream(t *testing.T) {
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("图里是", "一个报错"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-image"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "这张图里是什么"}},
	})

	path := writeAgentCoreImage(t, "shot.png", agentCoreTinyPNG)
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-image-1",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
		attachments: []agentAIAttachment{{Type: "image", Name: "shot.png", Path: path}},
	}
	m.runAgentCore(context.Background(), run, log.write)

	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	msgs := reqs[0].Messages
	if len(msgs) == 0 {
		t.Fatal("上游请求没有 messages")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("末条消息角色 = %q, want user", last.Role)
	}
	// 请求体经 JSON 往返，[]ContentBlock 解成 []interface{}。
	rawBlocks, ok := last.Content.([]interface{})
	if !ok {
		t.Fatalf("末条消息 content 应当是数组，实际 %T", last.Content)
	}

	wantData := base64.StdEncoding.EncodeToString(agentCoreTinyPNG)
	foundImage := false
	foundText := false
	for _, raw := range rawBlocks {
		block, _ := raw.(map[string]interface{})
		if block == nil {
			continue
		}
		switch block["type"] {
		case agentcore.BlockTypeText:
			if s, _ := block["text"].(string); strings.Contains(s, "这张图里是什么") {
				foundText = true
			}
		case agentcore.BlockTypeImage:
			src, _ := block["source"].(map[string]interface{})
			if src == nil {
				continue
			}
			if src["media_type"] == "image/png" && src["data"] == wantData {
				foundImage = true
			}
		}
	}
	if !foundText {
		t.Errorf("末条消息里没有用户原文: %+v", rawBlocks)
	}
	if !foundImage {
		t.Errorf("末条消息里没有图片块——模型看不到图（blocks=%+v）", rawBlocks)
	}

	// 顺带确认这轮 run 本身是健康的：图片支持不该破坏终止契约。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（事件 %v）", got, log.filter(models.AgentEventAIError))
	}
}
