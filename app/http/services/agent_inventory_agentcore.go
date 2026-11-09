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
	applyAgentCoreRenameOverlay(sessions)
	return sessions
}

// applyAgentCoreRenameOverlay 把 rename 缓存里按「会话 id」键落的用户改名
// 覆盖到 agentcore 清单卡上（优先级:rename 缓存标题 > journal 首条消息标题）。
// 与 claude 通道的 applyClaudeRenameNames 同构,但简单得多:agentcore 没有
// pid 记录观察通道,写侧只有 phone origin 一条来路（handleRemoteAIRename 的
// 别名键,G4 改名断链）。
//
// 必须放在 parse cache 之外:rename 不改 journal 文件的 (size,mtime),放缓存
// 读取点之内会让改名永远命中旧摘要——claude 通道的覆盖层同样在组装末端。
// 只认显式条目（agentRenameEntryExplicit:phone origin 恒显式）,derived/auto
// 类标签不覆盖标题——与服务端 title_updated_at 守卫同一防线。
func applyAgentCoreRenameOverlay(sessions []models.AgentVibeSession) {
	if len(sessions) == 0 {
		return
	}
	renameCache := loadAgentRenameCache()
	if len(renameCache) == 0 {
		return
	}
	for index := range sessions {
		entry, ok := renameCache[sessions[index].ID]
		if !ok || strings.TrimSpace(entry.Name) == "" || !agentRenameEntryExplicit(entry) {
			continue
		}
		sessions[index].Title = truncateAgentText(entry.Name, 200)
		if sessions[index].Summary == "" {
			sessions[index].Summary = truncateAgentText(entry.Name, 500)
		}
		sessions[index].TitleUpdatedAt = entry.UpdatedAt
		sessions[index].TitleSource = wireAgentRenameSource(entry)
	}
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
	// agentcore journal 落 user/assistant/turn/tool 多种行,MessageCount/displayCount
	// 数 msg 行与有正文的 turn 行（tool 行是轨迹不计对话计数）;仍单独维护
	// displayCount 保持与 codex/claude reader 相同的字段语义（残壳=0）。
	displayCount := 0
	// toolCount/turnCount 给 tool/turn 行造稳定唯一 ID;transcriptRow 是全转录
	// 行序号（msg+turn+tool 共用）,作 Index——各类行交错时若各用各的计数会
	// 撞号,统一计数器对无 tool/turn 行的旧文件与原 messageCount-1 口径数值
	// 恒等（向后兼容）。
	toolCount := 0
	turnCount := 0
	// endNoteCount 给 end(error) 折出的系统条造稳定唯一 ID（end-%06d）；系统条
	// 是轨迹性说明行,只进转录序不进对话计数(见 end case 注释)。
	endNoteCount := 0
	transcriptRow := 0
	// parsedAny：是否有任何一行真正解析成功（session 头行被识别,或 msg 行
	// 通过角色+内容校验）。整文件损坏时一行都不成立——文件名兜底会凭空造出
	// 一张 MessageCount=0 的幽灵卡（claude 通道 2026-10-18 husk 审计防御的同
	// 类问题），必须整体跳过。无头行文件不受影响：合法 msg 行会置位。
	parsedAny := false
	// 子agent轨迹聚合（spec subagent-panel §二）：entries 按 task_id 键合并
	// 同一子 run 的 start/finish/tool 行（有意语义——同 task_id 是同一次子
	// run，task_id 含回合 messageID 所以同名子agent跨回合绝不串条），
	// subagentOrder 保住行序（=时间序）输出。行在 truncateBefore 区间归属
	// 与 healMergeJournal 搬迁中随宿主 msg 行区间处理，与 tool/turn/end 同规
	// （写侧已保证）。
	subagentEntries := make(map[string]*models.AgentVibeSubagent)
	subagentOrder := make([]string, 0, 4)
	// sawEnd：会话是否出现过 run 终态行（success/stopped/error）。出现即代表
	// 主回合已收束——此刻仍未 finish 的子 agent 已被确证死亡（进程崩溃/断电
	// 留下的 start 无 finish 僵尸行），组装时降级 interrupted（审计 2026-11-09）。
	// 无 end 行的文件（进行中/写入中断）不降级，避免误杀活着的子 run。
	sawSubagentEnd := false
	forEachAgentSessionJSONLLine(path, maxLineBytes, func(line []byte) bool {
		var row struct {
			Kind           string                       `json:"kind"`
			SessionID      string                       `json:"session_id"`
			ProjectPath    string                       `json:"project_path"`
			Model          string                       `json:"model"`
			Effort         string                       `json:"effort"`
			ApprovalMode   string                       `json:"approval_mode"`
			CreatedAt      string                       `json:"created_at"`
			Origin         string                       `json:"origin"`
			Role           string                       `json:"role"`
			MessageID      string                       `json:"message_id"`
			Text           string                       `json:"text"`
			Attachments    []models.AgentVibeAttachment `json:"attachments,omitempty"`
			Thinking       string                       `json:"thinking"`
			Turn           int                          `json:"turn"`
			Tool           string                       `json:"tool"`
			Summary        string                       `json:"summary"`
			Ts             string                       `json:"ts"`
			Status         string                       `json:"status"`
			Error          string                       `json:"error"`
			Diagnostics    map[string]interface{}       `json:"diagnostics"`
			Event          string                       `json:"event"`
			TaskID         string                       `json:"task_id"`
			Name           string                       `json:"name"`
			Description    string                       `json:"description"`
			SubagentTaskID string                       `json:"subagent_task_id"`
			IsError        bool                         `json:"is_error"`
			Result         string                       `json:"result"`
			TotalTokens    int64                        `json:"total_tokens"`
			DurationMs     int64                        `json:"duration_ms"`
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
			session.Effort = firstNonEmpty(session.Effort, row.Effort)
			// 头行 approval_mode(2026-11-05 会话级审批模式):跨端续聊回填用,
			// 缺键(旧文件)=空=默认推荐。
			session.ApprovalMode = firstNonEmpty(session.ApprovalMode, row.ApprovalMode)
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
			// 写侧保证 text 非空或带 attachments（纯图行 text 空但附件非空,
			// spec 2026-11-05-chat-image-pipeline §3.3）,这里防损坏行：两者
			// 皆空的行不计数的语义与 codex reader 的 msg.Content != "" 守卫
			// 一致。
			content := truncateAgentText(row.Text, agentVibeTranscriptMaxContentRunes)
			if content == "" && len(row.Attachments) == 0 {
				return true
			}
			parsedAny = true
			ts := normalizeAgentTime(row.Ts)
			session.MessageCount++
			transcriptRow++
			if session.Title == "" && row.Role == "user" && content != "" && !isJunkAgentTitle(content) {
				session.Title = truncateAgentText(content, 200)
			}
			window.add(models.AgentVibeMessage{
				ID:          row.MessageID,
				Role:        row.Role,
				Content:     content,
				Timestamp:   ts,
				Index:       transcriptRow - 1,
				Attachments: row.Attachments,
			})
			displayCount++
			if ts != "" { // 空 ts 不赋值:防损坏行抹掉前一行已落下的有效值
				session.UpdatedAt = ts // msg 行按序追加,最后一条有效 ts 即 UpdatedAt
			}
			if session.CreatedAt == "" {
				session.CreatedAt = ts
			}
		case "turn":
			// 逐回合转录行(OnTurnEnd 落盘,text+thinking+stop_reason+tools)。
			// 2026-10-31 审计定谳前本 case 缺席,19 回合的内容在 detail/扫盘
			// 上报里永远只看得到每 run 一条 msg——这是「agentcore 会话手机
			// 端无历史」事故的内核侧根因。有正文的回合回放为 assistant 转录
			// 行;纯工具回合(text 空)无可渲染正文,Phase 2(工具/思考历史
			// 门控)前跳过。thinking 随 AgentVibeMessage.Thinking 新字段携带,
			// server zod 剥未知字段,手机契约未消费——先上梅,Phase 1/2 点亮。
			// 无头行文件(session.ID 空)与 tool 行同规跳过:turn 行没有
			// message_id,无归属依据;损失模式同 tool 行(steer 先到→无头行),
			// 有意接受。与直播已落库的 assistant 行文字相重时,server 侧
			// dropContentDuplicates(role+空白折叠 content 键,863fe6f)按存量
			// 份数吸收,不产生重复气泡。
			if session.ID == "" {
				return true
			}
			content := truncateAgentText(row.Text, agentVibeTranscriptMaxContentRunes)
			if content == "" {
				return true
			}
			parsedAny = true
			turnCount++
			transcriptRow++
			ts := normalizeAgentTime(row.Ts)
			session.MessageCount++
			window.add(models.AgentVibeMessage{
				ID:        fmt.Sprintf("turn-%06d", turnCount),
				Role:      "assistant",
				Content:   content,
				Thinking:  truncateAgentText(row.Thinking, agentVibeTranscriptMaxContentRunes),
				Timestamp: ts,
				Index:     transcriptRow - 1,
			})
			displayCount++
			if ts != "" { // 空 ts 不赋值:防损坏行抹掉前一行已落下的有效值
				session.UpdatedAt = ts
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
			// 子agent工具行（带 subagent_task_id）：归组进对应 entry.Tools,
			// 绝不进主转录——子 run 轨迹与主会话工具流分离是面板设计的前提
			// （spec subagent-panel）。entry 查不到（start 行写失败的边缘形态）
			// 同样不进主转录：混流比静默丢失更伤主时间线,与 tool/turn 行的
			// 无头行损失模式同款接受。此分支不占 toolCount/transcriptRow——
			// 转录 Index 是可见行的连续序号,不可见行不消费。
			if taskID := strings.TrimSpace(row.SubagentTaskID); taskID != "" {
				if entry, ok := subagentEntries[taskID]; ok {
					entry.Tools = append(entry.Tools, models.AgentVibeSubagentTool{
						Tool:    row.Tool,
						Summary: row.Summary,
						IsError: row.IsError,
						Ts:      normalizeAgentTime(row.Ts),
					})
				}
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
		case "subagent":
			// 子agent行（start/finish）：组装成 Subagents[],不进主转录、不计
			// MessageCount/displayCount/transcriptRow（同 tool 行的轨迹语义,
			// 不置 parsedAny——end 行同款,只 enrich 已解析的会话）。无头行文件
			// 里 session.ID 尚空,行无归属依据,与 tool/turn 行同款跳过。
			if session.ID == "" {
				return true
			}
			taskID := strings.TrimSpace(row.TaskID)
			if taskID == "" { // 损坏行：无归属依据
				return true
			}
			// entry 创建只发生在 start/finish 两个分支内:未知 event（旧二进制
			// 读新 journal 的常态演进,写侧将来加 progress/note 是自然方向）
			// 直接跳过不建 entry,否则会产出 Status/Name 双空的幽灵卡。
			switch row.Event {
			case "start":
				entry, ok := subagentEntries[taskID]
				if !ok {
					entry = &models.AgentVibeSubagent{TaskID: taskID}
					subagentEntries[taskID] = entry
					subagentOrder = append(subagentOrder, taskID)
				} else if entry.Status == "completed" || entry.Status == "failed" {
					// finish 先到的乱序形态（healMergeJournal 搬迁/时钟错位）：
					// 终态是更强事实,迟到的 start 回放不得把已定稿 entry 打回
					// started 矛盾态,也不回填 start 侧字段。
					return true
				}
				entry.Status = "started"
				entry.Name = row.Name
				entry.Description = row.Description
				entry.StartedAt = normalizeAgentTime(row.Ts)
			case "finish":
				entry, ok := subagentEntries[taskID]
				if !ok {
					entry = &models.AgentVibeSubagent{TaskID: taskID}
					subagentEntries[taskID] = entry
					subagentOrder = append(subagentOrder, taskID)
				}
				// status 由写侧保证 completed|failed,这里透传不造值——异常空值
				// 保持「已 start 未终态」的 started 降级口径比凭空补 completed
				// 更不误导（面板按 status 渲染终态徽标）。
				if status := strings.TrimSpace(row.Status); status != "" {
					entry.Status = status
				}
				entry.Summary = row.Summary
				entry.Result = row.Result
				entry.TotalTokens = row.TotalTokens
				entry.DurationMs = row.DurationMs
				entry.EndedAt = normalizeAgentTime(row.Ts)
			} // 其余 event 值未知:跳过且不建 entry（同未知 kind 的既有防线）
		case "end":
			// 终态行：失败真因 + 诊断块。诊断面板据此在重开旧会话时还原「这一轮
			// 为什么死」。行按序追加，多次 run 时最后一次 end 胜出。
			sawSubagentEnd = true
			// 注意不写 session.Status：那是列表视图的既有契约（默认 "closed"，手
			// 机按它映射），单次 run 的终态语义收进 Diagnostics.Status，两套口径
			// 不互相污染。
			// 不置 parsedAny：只有 end 行的文件（头行丢失的边缘形态）不应凭空
			// 造出 0 消息的幽灵卡——end 行只 enrich 已解析的会话。
			if row.Error != "" || len(row.Diagnostics) > 0 {
				session.Diagnostics = newAgentVibeDiagnosticsFromEndRow(row.Status, row.Error, row.Diagnostics)
			}
			// end(error) 行 → 系统条（G5 问题悬空修复，2026-11-05）：失败回合在
			// 转录里悬空（有提问无任何反馈）是用户「为什么没有回答」的直接来源。
			// 仅 error——success/stopped（正常完成/用户取消）不值得占位。无头行
			// 文件（session.ID 解析期尚空）与 tool/turn 同款跳过：行无归属依据，
			// 诊断 enrich 不受影响。计数照 tool 行口径：进转录序（Index 单调）但
			// 不计 MessageCount/displayCount——系统条是轨迹性说明行，PhoneServer
			// 入库边界本就按 role=system 过滤、消息数过滤宇宙亦排除它。
			if !strings.EqualFold(strings.TrimSpace(row.Status), "error") || session.ID == "" {
				break
			}
			note := agentCoreEndNoteText(row.Error, row.Diagnostics)
			if note == "" {
				break
			}
			endNoteCount++
			transcriptRow++
			window.add(models.AgentVibeMessage{
				ID:        fmt.Sprintf("end-%06d", endNoteCount),
				Role:      "system",
				Content:   truncateAgentText(note, agentVibeTranscriptMaxContentRunes),
				Timestamp: normalizeAgentTime(row.Ts),
				Index:     transcriptRow - 1,
			})
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
	if len(subagentOrder) > 0 {
		// 按 order 切片（行序=时间序）输出；缺 finish 的 entry 保持 started。
		// 列表/同步出口由 summarizeAgentVibeSessions 剥离（详情契约≠清单契约）。
		// 会话已收束（有 end 行）而子 agent 仍 started = 进程崩溃/断电僵尸
		// （正常取消由桥 defer 写 interrupted，进程级死亡则什么终态行都没有）
		// ——降级 interrupted，防面板永久「运行中」卡（审计 2026-11-09）。
		if sawSubagentEnd {
			for _, entry := range subagentEntries {
				if entry.Status == "started" {
					entry.Status = "interrupted"
				}
			}
		}
		subagents := make([]models.AgentVibeSubagent, 0, len(subagentOrder))
		for _, taskID := range subagentOrder {
			subagents = append(subagents, *subagentEntries[taskID])
		}
		session.Subagents = subagents
	}
	if !isSafeAgentProjectPath(session.ProjectPath) {
		session.ProjectPath = ""
	}
	if session.UpdatedAt == "" {
		session.UpdatedAt = fileUpdatedAt(path)
	}
	return session
}

// agentCoreEndNoteText 把 journal end(error) 行折成用户可读的系统条文案（G5
// 问题悬空修复）。供两层读侧共用：详情转录（本文件）与快照订阅
// （agent_ai_journal_history.go）。内核终止真因（loop.go 守卫出口）与宿主
// 错误文案（agentCoreEnsureUpstream 等）本身已是中文人话，直接缀在主句后；
// 「agentcore run failed」是宿主侧的笼统兜底占位（真因丢失时才出现），不得
// 进正文。diagnostics.reason（内核终态快照的真因键）优先于 error 键——宿主
// 升级后两者常同文，天然去重；无 reason 时保留 error 原文兜底。
func agentCoreEndNoteText(errMsg string, raw map[string]interface{}) string {
	reason := ""
	if v, ok := raw["reason"].(string); ok {
		reason = strings.TrimSpace(v)
	}
	detail := firstNonEmpty(reason, strings.TrimSpace(errMsg))
	if detail == "" || detail == "agentcore run failed" {
		return "这一轮执行失败"
	}
	return "这一轮执行失败：" + detail
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
