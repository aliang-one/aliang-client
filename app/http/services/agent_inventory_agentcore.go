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
	// agentcore journal 落 user/assistant/tool 三种行,MessageCount/displayCount
	// 只数 msg 行（tool 行是轨迹不计对话计数）;仍单独维护 displayCount 保持与
	// codex/claude reader 相同的字段语义（残壳=0）。
	displayCount := 0
	// toolCount 给 tool 行造稳定唯一 ID;transcriptRow 是全转录行序号（msg+tool
	// 共用）,作 Index——msg 与 tool 交错时若各用各的计数会撞号,统一计数器对
	// 无 tool 行的旧文件与原 messageCount-1 口径数值恒等（向后兼容）。
	toolCount := 0
	transcriptRow := 0
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
			Origin      string `json:"origin"`
			Role        string `json:"role"`
			MessageID   string `json:"message_id"`
			Text        string `json:"text"`
			Tool        string `json:"tool"`
			Summary     string `json:"summary"`
			Ts          string `json:"ts"`
			Status      string                 `json:"status"`
			Error       string                 `json:"error"`
			Diagnostics map[string]interface{} `json:"diagnostics"`
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
			// 头行 origin（phone/dashboard/terminal…）忠实回填摘要条目：跨端续聊
			// 分流靠它判断该会话由哪一端发起。旧文件（头行无 origin 键）与无头行
			// 文件保持空串（omitempty 不下发）。
			session.Origin = firstNonEmpty(session.Origin, row.Origin)
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
			transcriptRow++
			if session.Title == "" && row.Role == "user" && !isJunkAgentTitle(content) {
				session.Title = truncateAgentText(content, 200)
			}
			window.add(models.AgentVibeMessage{
				ID:        row.MessageID,
				Role:      row.Role,
				Content:   content,
				Timestamp: ts,
				Index:     transcriptRow - 1,
			})
			displayCount++
			if ts != "" { // 空 ts 不赋值:防损坏行抹掉前一行已落下的有效值
				session.UpdatedAt = ts // msg 行按序追加,最后一条有效 ts 即 UpdatedAt
			}
			if session.CreatedAt == "" {
				session.CreatedAt = ts
			}
		case "tool":
			// 工具轨迹行：渲染进 Transcript 但不计 MessageCount/displayCount。
			// 无头行文件（steer 先到）里 session.ID 尚空,tool 行没有会话归属,跳过。
			// 损失模式落账:steer 先到→无头行→run 追加后头行永远补不上（写侧
			// ensureSessionHeader 只在文件不存在时写头行）→该场景 tool 轨迹静默
			// 丢失,与 msg 行的文件名兜底不一致——有意接受,损失面窄。
			if session.ID == "" {
				return true
			}
			// 防损坏行：tool/summary 双空渲染不出任何内容,跳过（同 msg 空 text 守卫）。
			if row.Tool == "" && row.Summary == "" {
				return true
			}
			toolCount++
			transcriptRow++
			// reader 不信任写侧:超长 summary 与 msg 行同规截断（家规）。
			content := truncateAgentText(strings.TrimSpace(row.Tool+" "+row.Summary), agentVibeTranscriptMaxContentRunes)
			window.add(models.AgentVibeMessage{
				ID:        fmt.Sprintf("tool-%06d", toolCount),
				Role:      "tool",
				Content:   content,
				Timestamp: normalizeAgentTime(row.Ts),
				Index:     transcriptRow - 1,
			})
		case "end":
			// 终态行：失败真因 + 诊断块。诊断面板据此在重开旧会话时还原「这一轮
			// 为什么死」。行按序追加，多次 run 时最后一次 end 胜出。
			// 注意不写 session.Status：那是列表视图的既有契约（默认 "closed"，手
			// 机按它映射），单次 run 的终态语义收进 Diagnostics.Status，两套口径
			// 不互相污染。
			// 不置 parsedAny：只有 end 行的文件（头行丢失的边缘形态）不应凭空
			// 造出 0 消息的幽灵卡——end 行只 enrich 已解析的会话。
			if row.Error != "" || len(row.Diagnostics) > 0 {
				session.Diagnostics = newAgentVibeDiagnosticsFromEndRow(row.Status, row.Error, row.Diagnostics)
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

// newAgentVibeDiagnosticsFromEndRow 把 journal end 行的散字段折叠成诊断结构。
// json.Unmarshal 到 map 的数值恒为 float64、其余为 string/bool，逐键缺省容忍
// ——旧行没有 diagnostics 键时只有 Status/Error 有值，照样成卡。
func newAgentVibeDiagnosticsFromEndRow(status, errMsg string, raw map[string]interface{}) *models.AgentVibeDiagnostics {
	d := &models.AgentVibeDiagnostics{Status: status, Error: errMsg}
	num := func(key string) float64 {
		if v, ok := raw[key].(float64); ok {
			return v
		}
		return 0
	}
	str := func(key string) string {
		if v, ok := raw[key].(string); ok {
			return v
		}
		return ""
	}
	d.Turns = int(num("turns"))
	d.ContextTokens = int(num("context_tokens"))
	d.ContextWindow = int(num("context_window"))
	d.DupStreak = int(num("dup_streak"))
	d.ErrStreak = int(num("err_streak"))
	d.TruncStreak = int(num("trunc_streak"))
	d.ElapsedMs = int64(num("elapsed_ms"))
	d.StopReason = str("stop_reason")
	d.Reason = str("reason")
	if v, ok := raw["terminal"].(bool); ok {
		d.Terminal = v
	}
	return d
}
