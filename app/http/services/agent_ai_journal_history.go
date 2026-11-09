package services

// journal 读回（2026-10-28 跨端续聊，spec §4.3）：agentcore 会话不在内存时，
// 从 journal 文件重建 history 装回内存再跑；订阅快照（emitSessionSnapshot）
// 也用它取转录。读回的是「journal 所见」——斜杠命令/半截输出在旧文件里本就
// 无痕（spec §9 已声明）。残行/非法行跳过。

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// agentVibeAttachmentsFromRow 把 journal 行的 attachments 原始 JSON 值转成
// wire 视图（models.AgentVibeAttachment 形状，spec §3.3）。缺键/形状不符
// 返回 nil——老行（2026-11-05 前落盘）无此键，天然兼容。
func agentVibeAttachmentsFromRow(raw interface{}) []models.AgentVibeAttachment {
	list, ok := raw.([]interface{})
	if !ok || len(list) == 0 {
		return nil
	}
	out := make([]models.AgentVibeAttachment, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		ref, _ := entry["ref"].(string)
		if strings.TrimSpace(ref) == "" {
			continue
		}
		view := models.AgentVibeAttachment{Name: ref, Ref: ref}
		if name, ok := entry["name"].(string); ok && name != "" {
			view.Name = name
		}
		if mime, ok := entry["mime"].(string); ok {
			view.Mime = mime
		}
		if bytes, ok := entry["bytes"].(float64); ok {
			view.Bytes = int64(bytes)
		}
		out = append(out, view)
	}
	return out
}

type agentCoreJournalHead struct {
	projectPath string
	model       string
	// origin：头行来源（phone|dashboard|unknown），写侧 ensureSessionHeader
	// 落盘；旧文件/补写前的文件缺席=空串，本解析器自然容忍。
	origin string
	// disabledMCPServers/disabledSkills：会话级 MCP/技能开关的持久化值
	// （syncSessionSwitches 落头行，2026-10-31）。缺键=nil（未设置，行为同
	// 全部启用）；懒建恢复时载荷没带键就用它兜底，避免重启后静默回滚。
	disabledMCPServers []string
	disabledSkills     []string
	// approvalMode:会话级审批模式持久化值(2026-11-05)。缺键=空(默认)。
	approvalMode string
}

// readAgentCoreJournalHistory 便捷包装：扫默认 journal 根。
func readAgentCoreJournalHistory(sessionID string) ([]agentAIMessage, agentCoreJournalHead, error) {
	return readAgentCoreJournalHistoryFrom(agentCoreJournalDir(), sessionID)
}

// readAgentCoreJournalHistoryFrom 解析 journal 全部 msg 行为内存 history
// （顺序=文件顺序）。os.ErrNotExist 透传（调用方回退 lazy-create）；其余 IO
// 错误也返回 error（调用方发 history_restore_failed 提示）。dir 为空（journal
// 禁用）视同不存在。head 取自文件头行（kind=session），文件缺失时为零值。
//
// 本函数是**模型上下文回装口径**：只回 user/assistant 对话行（含 turn 行），
// end 行不折系统条（includeEndNotes=false）——回装结果要进模型 prompt，
// 「这一轮执行失败」的事实说明喂回去会污染续聊上下文。快照订阅读侧（展示面）
// 走 readAgentCoreJournalTranscriptFrom(true)。
func readAgentCoreJournalHistoryFrom(dir, sessionID string) ([]agentAIMessage, agentCoreJournalHead, error) {
	return readAgentCoreJournalTranscriptFrom(dir, sessionID, false)
}

// readAgentCoreJournalTranscriptFrom journal 读侧共用解析器。includeEndNotes
// 为 true 时（快照订阅展示面，agent_ai_sockets.go emitSessionSnapshot），
// end(error) 行额外折成 role=system 的系统条进转录（G5 问题悬空修复）——
// 失败回合在快照里悬空（有提问无任何反馈）是用户「为什么没有回答」的直接
// 来源。系统条照常吃 agentAISnapshotTranscriptCap 限幅（失败信息值得占位，
// 计划钉死）。false 时 end 行完全跳过（模型上下文回装口径）。
func readAgentCoreJournalTranscriptFrom(dir, sessionID string, includeEndNotes bool) ([]agentAIMessage, agentCoreJournalHead, error) {
	if dir == "" || sessionID == "" {
		return nil, agentCoreJournalHead{}, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName(sessionID)))
	if err != nil {
		return nil, agentCoreJournalHead{}, err
	}
	var head agentCoreJournalHead
	var hist []agentAIMessage
	for _, line := range splitJSONLLines(data) {
		var row map[string]interface{}
		if json.Unmarshal(line, &row) != nil {
			continue // 残行容忍
		}
		switch row["kind"] {
		case "session":
			head.projectPath, _ = row["project_path"].(string)
			head.model, _ = row["model"].(string)
			head.origin, _ = row["origin"].(string)
			head.approvalMode, _ = row["approval_mode"].(string)
			head.disabledMCPServers = headerStringList(row["disabled_mcp_servers"])
			head.disabledSkills = headerStringList(row["disabled_skills"])
		case "msg":
			// kind=tool/approval 行由 inventory 侧消费，此处仅 msg 行进历史。
			role, _ := row["role"].(string)
			text, _ := row["text"].(string)
			msgID, _ := row["message_id"].(string)
			// 附件透传（spec §3.3）：text 空但带附件的纯图行保留——这是手机图
			// 网页显示链的读侧半段。
			atts := agentVibeAttachmentsFromRow(row["attachments"])
			if (role != "user" && role != "assistant") || (text == "" && len(atts) == 0) {
				continue
			}
			msg := agentAIMessage{Role: role, MessageID: msgID, Content: text, Attachments: atts}
			if ts, ok := row["ts"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					msg.CreatedAt = t
				}
			}
			hist = append(hist, msg)
		case "turn":
			// 逐回合转录行(OnTurnEnd)：assistant 回答的唯一载体——msg 行只有
			// user(appendUser)，回答经 appendAssistantTurn/appendAssistant 全部
			// 落 kind=turn。缺席则快照转录永远只有 user 行：重进页订阅快照、
			// 内存续聊恢复全部拿不到回答（2026-11-05 仪表盘刷新丢回答事故的
			// 内核侧根因；inventory 侧 2026-10-31 已补，本读侧漏网）。turn 行
			// 无 message_id（inventory 同款接受），thinking 字段快照契约未
			// 消费，不进历史。turn 行本批次无附件（内核回答纯文本）。
			role, _ := row["role"].(string)
			text, _ := row["text"].(string)
			if (role != "user" && role != "assistant") || text == "" {
				continue
			}
			msg := agentAIMessage{Role: role, Content: text}
			if ts, ok := row["ts"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					msg.CreatedAt = t
				}
			}
			hist = append(hist, msg)
		case "end":
			// end(error) 行 → 系统条（G5 问题悬空修复，2026-11-05）：终态真因
			// 人话化后以 role=system 下发，前端按既有系统条先例渲染居中弱化胶囊。
			// 仅 error——success/stopped（正常完成/用户取消）不值得占位；无正文
			// 的边缘形态（agentCoreEndNoteText 恒有主句，防御性守卫）跳过。
			// 位置照 journal 文件序：失败插在它的提问之后、后续成功回合之前。
			if !includeEndNotes {
				continue
			}
			status, _ := row["status"].(string)
			if !strings.EqualFold(strings.TrimSpace(status), "error") {
				continue
			}
			errMsg, _ := row["error"].(string)
			diag, _ := row["diagnostics"].(map[string]interface{})
			note := agentCoreEndNoteText(errMsg, diag)
			if note == "" {
				continue
			}
			noteMsg := agentAIMessage{Role: "system", Content: note}
			if ts, ok := row["ts"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					noteMsg.CreatedAt = t
				}
			}
			hist = append(hist, noteMsg)
		}
	}
	return hist, head, nil
}

// headerStringList 从头行取字符串数组；缺键/类型不符返回 nil（=未设置）。
func headerStringList(v interface{}) []string {
	raw, _ := v.([]interface{})
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readAgentCoreJournalHeaderFrom 只读会话 journal 的头行（kind=session），
// 供显式改键点取「当前持久化的开关名单」用——比整文件读便宜（bufio 单行），
// 语义上与 readAgentCoreJournalHistoryFrom 的头行解析一致。文件缺失/无头行
// 返回 ErrNotExist 透传。
func readAgentCoreJournalHeaderFrom(dir, sessionID string) (agentCoreJournalHead, error) {
	if dir == "" || sessionID == "" {
		return agentCoreJournalHead{}, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(dir, agentCoreJournalFileName(sessionID)))
	if err != nil {
		return agentCoreJournalHead{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return agentCoreJournalHead{}, os.ErrNotExist
	}
	var row map[string]interface{}
	if json.Unmarshal(sc.Bytes(), &row) != nil || row["kind"] != "session" {
		return agentCoreJournalHead{}, os.ErrNotExist
	}
	var head agentCoreJournalHead
	head.projectPath, _ = row["project_path"].(string)
	head.model, _ = row["model"].(string)
	head.origin, _ = row["origin"].(string)
	head.disabledMCPServers = headerStringList(row["disabled_mcp_servers"])
	head.disabledSkills = headerStringList(row["disabled_skills"])
	head.approvalMode, _ = row["approval_mode"].(string)
	return head, nil
}

// splitJSONLLines 切 JSONL：按 \n 分行，跳过空行；不完整 JSON 由调用方
// Unmarshal 失败时丢弃。
func splitJSONLLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}
