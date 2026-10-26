package services

// agentcore（AliangCode）调用记录读侧：扫描 <agentHome()>/.aliang/ai-sessions
// 生成 AI 会话摘要 + 详情转录。与 codex 通道同构。设计文档：
// docs/superpowers/specs/2026-10-26-agentcore-journal-design.md §7。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

// collectAgentCoreVibeSessions 返回 agentcore 会话摘要（新→旧），复用全局
// recency-cap walker 与 (size,mtime) 解析缓存。
func collectAgentCoreVibeSessions(scanDirs []string) []models.AgentVibeSession {
	root := agentCoreJournalDir()
	if root == "" {
		return nil
	}
	files, dropped, truncated := findRecentAgentFilesWithStats(root, "*.jsonl", agentVibeSessionFileScanLimit)
	if dropped > 0 || truncated {
		logger.Debug(fmt.Sprintf("[AGENT-INVENTORY] agentcore_scan_truncated dropped=%d walk_truncated=%t", dropped, truncated))
	}
	sessions := make([]models.AgentVibeSession, 0, len(files))
	for _, path := range files {
		session := readAgentCoreVibeSummary(path, scanDirs)
		if session.ID == "" {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// readAgentCoreVibeSummary 带解析缓存的摘要读取（同 readAgentVibeCodexSummary 模式）。
func readAgentCoreVibeSummary(path string, scanDirs []string) models.AgentVibeSession {
	info, err := os.Stat(path)
	if err != nil {
		return models.AgentVibeSession{}
	}
	key := agentVibeParseCacheKey(path, scanDirs, agentVibeTranscriptMaxMessages)
	if entry, ok := agentVibeParseCacheLookup(key, info.Size(), info.ModTime()); ok {
		return entry.session
	}
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, ScanDirs: scanDirs})
	agentVibeParseCacheStore(key, info.Size(), info.ModTime(), session, false)
	return session
}

// readAgentCoreSessionMetaWithOptions 解析单个 agentcore journal 文件。
// 行格式见 agent_ai_agentcore_journal.go；无头行（steer 先到/头行写失败）时
// ID 兜底取文件名（去 .jsonl），CreatedAt/UpdatedAt 回落消息 ts。
func readAgentCoreSessionMetaWithOptions(path string, options agentVibeSessionReadOptions) models.AgentVibeSession {
	options = normalizeAgentVibeSessionReadOptions(options)
	window := newAgentVibeTranscriptWindow(options)
	session := models.AgentVibeSession{
		Provider: "agentcore",
		Tool:     "agentcore",
		Mode:     "vibe",
		Status:   "closed",
	}
	// 早退（ScanDirs 丢弃）必须整体返回空结构体：session.ID 在过滤点之前可能
	// 已被 session 头行赋值，不能让循环后处理把它当成有效会话放行（同 codex）。
	discard := false
	maxLineBytes := options.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = agentVibeJSONLMaxLineBytes
	}
	// agentcore journal 只落 user/assistant 行,MessageCount 即可渲染数;仍单独
	// 维护 displayCount 保持与 codex/claude reader 相同的字段语义（残壳=0）。
	displayCount := 0
	// parsedAny：是否有任何一行真正解析成功（session 头行被识别,或 msg 行
	// 通过角色+内容校验）。整文件损坏时一行都不成立——文件名兜底会凭空造出
	// 一张 MessageCount=0 的幽灵卡（claude 通道 2026-10-18 husk 审计防御的同
	// 类问题），必须整体跳过。无头行文件不受影响：合法 msg 行会置位。
	parsedAny := false
	forEachAgentSessionJSONLLine(path, maxLineBytes, func(line []byte) bool {
		var row struct {
			Kind        string `json:"kind"`
			SessionID   string `json:"session_id"`
			ProjectPath string `json:"project_path"`
			Model       string `json:"model"`
			CreatedAt   string `json:"created_at"`
			Role        string `json:"role"`
			MessageID   string `json:"message_id"`
			Text        string `json:"text"`
			Ts          string `json:"ts"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return true // 半行容忍：跳过损坏行
		}
		switch row.Kind {
		case "session":
			parsedAny = true
			session.ID = firstNonEmpty(session.ID, row.SessionID)
			session.ProjectPath = firstNonEmpty(session.ProjectPath, cleanAgentProjectPath(row.ProjectPath))
			session.Model = firstNonEmpty(session.Model, row.Model)
			session.CreatedAt = firstNonEmpty(session.CreatedAt, normalizeAgentTime(row.CreatedAt))
			// 早过滤：扫描目录限制开启时,project_path 不在任一目录内则丢弃整个
			// 会话,不读 transcript（同 codex 的 session_meta 分支）。
			if len(options.ScanDirs) > 0 && session.ProjectPath != "" && !pathUnderAnyScanDir(session.ProjectPath, options.ScanDirs) {
				discard = true
				return false
			}
		case "msg":
			if row.Role != "user" && row.Role != "assistant" {
				return true
			}
			// 写侧保证 text 非空,这里防损坏行：空文本不计数的语义与 codex
			// reader 的 msg.Content != "" 守卫一致。
			content := truncateAgentText(row.Text, agentVibeTranscriptMaxContentRunes)
			if content == "" {
				return true
			}
			parsedAny = true
			ts := normalizeAgentTime(row.Ts)
			session.MessageCount++
			if session.Title == "" && row.Role == "user" && !isJunkAgentTitle(content) {
				session.Title = truncateAgentText(content, 200)
			}
			window.add(models.AgentVibeMessage{
				ID:        row.MessageID,
				Role:      row.Role,
				Content:   content,
				Timestamp: ts,
				Index:     session.MessageCount - 1,
			})
			displayCount++
			if ts != "" { // 空 ts 不赋值:防损坏行抹掉前一行已落下的有效值
				session.UpdatedAt = ts // msg 行按序追加,最后一条有效 ts 即 UpdatedAt
			}
			if session.CreatedAt == "" {
				session.CreatedAt = ts
			}
		}
		return true
	})
	if discard {
		return models.AgentVibeSession{}
	}
	if !parsedAny {
		// 整文件损坏：没有任何一行是可识别的 journal 行,文件名兜底只会造出
		// MessageCount=0 的幽灵卡,整体跳过。
		return models.AgentVibeSession{}
	}
	if session.ID == "" {
		// 无头行（steer 先到）：ID 兜底取文件名。此时 ProjectPath 恒空,
		// scanDirs 过滤会穿透（空 path 永不触发 discard）——这与 codex reader
		// 对缺 cwd 元数据行的行为同构,是有意为之。兜底 ID 是净化后文件名,
		// 含非法字符的 sessionID 会与活跃覆盖层原始 ID 分叉（接受：sessionID
		// 由服务端下发,现实为 UUID 形态,净化为恒等映射）。
		base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if base == "" || base == ".jsonl" {
			return models.AgentVibeSession{}
		}
		session.ID = base
	}
	session.DisplayMessageCount = &displayCount
	session.Transcript, session.TranscriptPage = window.page(session.MessageCount, options.IncludePageMeta)
	if !isSafeAgentProjectPath(session.ProjectPath) {
		session.ProjectPath = ""
	}
	if session.UpdatedAt == "" {
		session.UpdatedAt = fileUpdatedAt(path)
	}
	return session
}
