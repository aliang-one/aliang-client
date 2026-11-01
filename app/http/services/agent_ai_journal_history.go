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
	"time"
)

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
}

// readAgentCoreJournalHistory 便捷包装：扫默认 journal 根。
func readAgentCoreJournalHistory(sessionID string) ([]agentAIMessage, agentCoreJournalHead, error) {
	return readAgentCoreJournalHistoryFrom(agentCoreJournalDir(), sessionID)
}

// readAgentCoreJournalHistoryFrom 解析 journal 全部 msg 行为内存 history
// （顺序=文件顺序）。os.ErrNotExist 透传（调用方回退 lazy-create）；其余 IO
// 错误也返回 error（调用方发 history_restore_failed 提示）。dir 为空（journal
// 禁用）视同不存在。head 取自文件头行（kind=session），文件缺失时为零值。
func readAgentCoreJournalHistoryFrom(dir, sessionID string) ([]agentAIMessage, agentCoreJournalHead, error) {
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
			head.disabledMCPServers = headerStringList(row["disabled_mcp_servers"])
			head.disabledSkills = headerStringList(row["disabled_skills"])
		case "msg":
			// kind=tool/approval 行由 inventory 侧消费，此处仅 msg 行进历史。
			role, _ := row["role"].(string)
			text, _ := row["text"].(string)
			msgID, _ := row["message_id"].(string)
			if (role != "user" && role != "assistant") || text == "" {
				continue
			}
			msg := agentAIMessage{Role: role, MessageID: msgID, Content: text}
			if ts, ok := row["ts"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					msg.CreatedAt = t
				}
			}
			hist = append(hist, msg)
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
