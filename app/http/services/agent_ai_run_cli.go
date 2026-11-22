package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func (m *agentAIManager) runCLIPass(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, allowResume bool) (agentAIRunOutcome, agentAIEffortRejection) {
	resumeID := run.resumeSessionID
	newSessionID := run.reservedNativeSessionID
	if !allowResume {
		if newSessionID == "" {
			newSessionID = resumeID
			// 身份续接是有意设计(绑定/下一轮 --resume 依赖同一 native id),
			// 但副作用是一份新 jsonl 会顶着旧身份落盘——旧文件若日后回流,
			// 扫描会把两份历史折在同一个 UUID 下。这里留一行日志让该形态
			// 可诊断(2026-10-15 事故里正是这条路径造出了 28 行 stub)。
			if resumeID != "" {
				logger.Info(fmt.Sprintf(
					"ai.run.resume.weld: session=%s runSeq=%d fresh pass reuses native id %q (resume unresolvable or fresh retry)",
					run.sessionID, run.runSeq, resumeID))
			}
		}
		resumeID = ""
	}

	// spawn 前的第二道 revive 保险（runStart 入口是第一道）：真 fresh
	// （resumeSessionID 为空）且 reserved 身份的 jsonl 已落盘时，直接升级
	// --resume 复用既有历史，而不是撞 "Session ID already in use"。weld 路径
	// （resume 已被判不可解）不翻转，避免把已知打不开的 resume 再翻回去。
	if !allowResume && newSessionID != "" && strings.TrimSpace(run.resumeSessionID) == "" &&
		(providersEquivalent(run.provider, "claudecode") || strings.EqualFold(run.provider, "auto")) {
		if jsonl := ccPeerSessionJSONL(externalTUIHome(), newSessionID); jsonl != "" {
			logger.Info(fmt.Sprintf("ai.run.resume.revive: session=%s runSeq=%d reserved id %q jsonl 已落盘(%s)，fresh 自动升级 --resume", run.sessionID, run.runSeq, newSessionID, jsonl))
			resumeID = newSessionID
			newSessionID = ""
			allowResume = true
		}
	}

	var tool *agentAITool
	var err error
	if run.readOnly || len(run.goalIdentity) > 0 {
		tool, err = resolveGoalAgentAITool(run.prompt, run.provider, run.model, run.effort, resumeID, newSessionID)
	} else {
		tool, err = resolveAgentAITool(run.prompt, run.provider, run.model, run.effort, resumeID, newSessionID)
	}
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	// spawn 前探测上游可达性:56432 run service 运行态不持久化,core/owner
	// 重启后可能无人拉起——不可达先自愈,仍失败则人话快速失败,替代 CLI 侧
	// 10 次重试 3 分钟后的 error_during_execution(10-13/10-15 两次事故)。
	if (tool.id == "claude" || tool.id == "claudecode") && !run.readOnly {
		if err := ensureAIUpstreamReachableForRun(ctx); err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone, agentAIEffortRejection{}
		}
	}
	// 该二进制已知不认 --effort(探针确证或上次运行学到):spawn 前直接去档
	// 重解析,别让回合死在参数校验上(2026-09-29 事故:~/.local/bin claude 2.1.17)。
	// 去档是静默降级,打一行日志让它可诊断(评审确认:探测抖动窗口内可能
	// 选错二进制连带走档,日志是唯一可见痕迹)。
	if err == nil {
		if dropped := effortDropForBinary(run.effort, tool.path); dropped != run.effort {
			logger.Info(fmt.Sprintf(
				"ai.effort.dropped: session=%s runSeq=%d path=%q version=%q requested=%q (binary known to lack --effort)",
				run.sessionID, run.runSeq, tool.path, cachedCLIVersion(tool.path), run.effort))
			run.effort = dropped
			if run.readOnly || len(run.goalIdentity) > 0 {
				tool, err = resolveGoalAgentAITool(run.prompt, run.provider, run.model, dropped, resumeID, newSessionID)
			} else {
				tool, err = resolveAgentAITool(run.prompt, run.provider, run.model, dropped, resumeID, newSessionID)
			}
			if err != nil {
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
				return agentAIRunDone, agentAIEffortRejection{}
			}
		}
	}
	if run.readOnly {
		tool = withAgentReadOnlyPolicy(tool)
		if tool == nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New("provider does not support enforced read-only mode")))
			return agentAIRunDone, agentAIEffortRejection{}
		}
	} else if len(run.goalIdentity) > 0 {
		tool = withGoalExecutionPolicy(tool)
		// 强化 goal task 的 report 输出指令（provider 经代理常漏 ALIANG_GOAL_REPORT
		// 行，导致 goal 无故失败）。仅 claude stream-json 路径下 --append-system-prompt
		// 已挂载；这里把它扩展为原值 + goal report 强制段。
		tool = withGoalTaskReportSystemPrompt(tool)
	}
	tool = withAgentAIAttachments(tool, run.attachments)
	// Claude 的图片只能走 stream-json 的 image block。老版本 CLI 没有
	// --input-format（stdinStreamJSON=false）时没有降级通道：明确报错，
	// 而不是把图片悄悄降级成「prompt 里一行 URL」（模型读不到，用户以为发了）。
	var claudeImageBlocks []map[string]interface{}
	if tool.outputFormat == agentAIOutputClaudeStreamJSON && hasImageAttachments(run.attachments) {
		if !tool.stdinStreamJSON {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID,
				errors.New("this claude CLI does not support --input-format stream-json, which is required to send images; please upgrade claude")))
			return agentAIRunDone, agentAIEffortRejection{}
		}
		blocks, err := buildClaudeImageBlocks(run.attachments)
		if err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone, agentAIEffortRejection{}
		}
		claudeImageBlocks = blocks
	}
	cleanupClaudePolicy := func() {}
	effectiveRun := run
	if tool.outputFormat == agentAIOutputClaudeStreamJSON && !run.readOnly {
		// 平台通道缺失的 isolated→sanitized 回退必须在版本守卫之前：守卫要对
		// 提升后的档位继续行使职权（提升不得撤销版本守门的降级）。
		effectiveRun.claudePolicy = promoteIsolatedTierForPlatformFallback(effectiveRun.claudePolicy, run.sessionID)
		effectiveRun.claudePolicy = applyClaudeTierVersionGuard(tool, effectiveRun.claudePolicy, run.sessionID)
		var policyNotice map[string]interface{}
		tool, cleanupClaudePolicy, policyNotice = withClaudeRemotePolicy(tool, effectiveRun)
		// Fold must precede withClaudeApprovalHook: the hook re-derives
		// --setting-sources from trustTier, so a skipped fold would re-open
		// the sources a degrade just closed.
		effectiveRun.claudePolicy = applyClaudePolicyNotice(effectiveRun.claudePolicy, policyNotice)
		defer cleanupClaudePolicy()
		tool = withClaudeApprovalHook(tool, effectiveRun)
		// 自有交互 MCP server(tool_ask bridge):per-run token + 合并 --mcp-config。
		// 注入失败只降级不阻断回合——aliang-options 围栏兜底仍在;readOnly/goal
		// fork 不注入(无交互)。
		if toolAskInjected, cleanupToolAsk, toolAskErr := m.withAgentToolAskBridge(tool, effectiveRun, writeJSON); toolAskErr == nil {
			tool = toolAskInjected
			innerCleanup := cleanupClaudePolicy
			cleanupClaudePolicy = func() { cleanupToolAsk(); innerCleanup() }
		} else {
			logger.Warn(fmt.Sprintf("tool-ask: bridge injection failed, falling back to fence-only interactions session=%s err=%v", run.sessionID, toolAskErr))
		}
	}

	// 上下文窗口钉死放到最后：前面任何一次 tool 重解析(capability 去档)都会
	// 换出新的 args 副本，提前挂的话可能被覆盖掉。手机没选窗口时不加参数，
	// 保持 CLI 自己的默认行为。
	tool = withClaudeAutoCompact(tool, run.contextWindow)

	cmd := newBackgroundCommandContext(ctx, tool.path, tool.args...)
	cmd.Dir = run.projectPath
	cmd.Env = agentChildProcessEnv()
	var claudeSteer *agentAIClaudeSteerControl
	if tool.stdinStreamJSON {
		// 常驻 stdin：prompt 与后续 steer 都写成 stream-json 的 user 行。
		// 管道必须在 Start 之前建好并挂到会话上，否则启动窗口内到达的
		// steer 会误判为 unsupported。
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone, agentAIEffortRejection{}
		}
		claudeSteer = newAgentAIClaudeSteerControl(run, stdinPipe, writeJSON)
		m.attachClaudeSteer(run.sessionID, run.runSeq, claudeSteer)
		run.claudeSteer = claudeSteer
	} else if tool.stdinPrompt != "" {
		// Windows 批处理 shim 的 prompt 走 stdin（claudePromptViaStdinForOS）。
		cmd.Stdin = strings.NewReader(tool.stdinPrompt)
	}
	if len(tool.env) > 0 {
		cmd.Env = append(cmd.Env, tool.env...)
	}
	logger.Info(fmt.Sprintf(
		"ai.run.cli: session=%s runSeq=%d provider=%s path=%q version=%q cwd=%q allowResume=%t resume=%t model=%q effort=%q output=%s hook_base=%q args=%v env=%s",
		run.sessionID,
		run.runSeq,
		tool.id,
		tool.path,
		cachedCLIVersion(tool.path),
		run.projectPath,
		allowResume,
		strings.TrimSpace(resumeID) != "",
		normalizeAgentAIModel(run.model),
		strings.TrimSpace(run.effort),
		tool.outputFormat,
		currentAgentAIApprovalHookBaseURL(),
		agentAIToolDiagnosticArgs(tool),
		agentAIEnvDiagnostic(),
	))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	if err := cmd.Start(); err != nil {
		if claudeSteer != nil {
			claudeSteer.close("error", err.Error(), "start_failed")
		}
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	if claudeSteer != nil {
		// 首条 prompt 必须以 stream-json user 行写入；写完才放行排队中的 steer。
		claudeSteer.start(tool.stdinPrompt, claudeImageBlocks)
		// 本次 pass 结束时兜底关闸：resume-missing/effort-rejected 会在同一
		// run 里再起一次 runCLIPass，若不在返回时关闭，旧管道的写端会一直
		// 挂着，且旧通道在重试挂载新通道前仍可被 ai.steer 命中。
		defer claudeSteer.close("not_running", "claude run ended before steer was applied", "turn_ended")
	}

	started := map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     tool.id,
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	}
	if effectiveRun.claudePolicy.policyNotice != nil {
		started["policy_notice"] = effectiveRun.claudePolicy.policyNotice
	}
	_ = writeJSON(started)

	var wg sync.WaitGroup
	var outMu sync.Mutex
	var output strings.Builder
	var stderrBuf strings.Builder
	// Live "files touched this run" tracking: the stdout stream goroutine feeds
	// file-mutating tool_use paths into filesTouched via fileSink; a progress
	// ticker reads it. Locals (not on the by-value run) so the mutex is not copied.
	var filesMu sync.Mutex
	filesTouched := make(map[string]struct{})
	fileSink := func(fp string) {
		if fp = strings.TrimSpace(fp); fp == "" {
			return
		}
		filesMu.Lock()
		filesTouched[fp] = struct{}{}
		filesMu.Unlock()
		// A file written/edited is real progress, not just activity.
		run.activity.markProgress()
	}
	progressStop := make(chan struct{})
	defer close(progressStop)
	// CLI 通道诊断聚合器（与 agentcore 路径的 agentCoreDiagnostics 对应）：
	// stdout goroutine 经 streamStructuredAIDelta 写，这里 ticker 与收尾终帧
	// 读。挂载在 run 上（指针字段），流解析副本共享同一聚合器。
	run.claudeDiag = newClaudeRunDiagnostics(run.contextWindow)
	// 终态诊断帧：CLI 进程退出后收尾显式补一帧带 terminal 标记的诊断——
	// server 归一化落库后，面板重开会话回放显示的就是这份终值。注册在
	// progressStop 的 defer 之后（LIFO 先执行），终帧先于 ticker 关闭发出。
	defer func() {
		if run.claudeDiag == nil {
			return
		}
		// 无 result 事件（进程被杀/崩溃）时补终态标记，reason 留空——结构化
		// 真因已由 ai.error 通道单独下发，这里不重复拼文案。
		run.claudeDiag.finish("")
		frame := map[string]interface{}{
			"type":       models.AgentEventAIRunProgress,
			"session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID),
		}
		if diag := run.claudeDiag.payload(); diag != nil {
			frame["diagnostics"] = diag
			_ = writeJSON(frame)
		}
	}()
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		emit := func() {
			filesMu.Lock()
			touched := len(filesTouched)
			filesMu.Unlock()
			frame := map[string]interface{}{
				"type":                models.AgentEventAIRunProgress,
				"session_id":          run.sessionID,
				"message_id":          agentAssistantMessageID(run.messageID),
				"files_touched_count": touched,
				"git_changed_count":   countGitChanged(run.projectPath),
			}
			// 逐回合诊断快照（回合数/上下文用量/停止原因），诊断面板的 CLI
			// 实时数据源。尚无回合时为 nil，键不下发（与 agentcore 同约定：
			// server 端 isRecord 判定后归一化，空壳不覆盖旧值）。
			if diag := run.claudeDiag.payload(); diag != nil {
				frame["diagnostics"] = diag
			}
			_ = writeJSON(frame)
		}
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				emit()
			}
		}
	}()
	limiter := &agentAIOutputLimiter{meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes))}
	// lastRetry is written by the stdout goroutine (on each Claude api_retry
	// event) and read here after wg.Wait(), so the terminal ai.error can carry a
	// structured cause. wg.Wait() provides the happens-before edge.
	var lastRetry claudeRetryInfo
	// capturedResumeSessionID is written by the stdout goroutine (from the
	// provider's structured events) and read after wg.Wait(), which gives the
	// happens-before edge — same pattern as lastRetry. Pinned on the session
	// post-run so the next CLI turn resumes instead of starting fresh.
	var capturedResumeSessionID string
	var bindingOnce sync.Once
	var bindingErr error
	onNativeSessionID := func(nativeSessionID string) {
		bindingOnce.Do(func() {
			record, err := m.confirmBinding(
				run.sessionID,
				tool.id,
				nativeSessionID,
				run.bindingVersion,
			)
			if err != nil {
				bindingErr = err
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
			_ = writeJSON(map[string]interface{}{
				"type":              models.AgentEventAISessionBound,
				"session_id":        run.sessionID,
				"message_id":        run.messageID,
				"provider":          tool.id,
				"native_session_id": record.NativeSessionID,
				"source_session_id": record.NativeSessionID,
				"binding_version":   record.BindingVersion,
				"binding_state":     record.State,
			})
		})
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamAgentAIStdout(stdout, tool.outputFormat, run, writeJSON, limiter, func(text string) {
			outMu.Lock()
			appendAgentAIHistoryCapture(&output, text)
			outMu.Unlock()
		}, fileSink, &lastRetry, &capturedResumeSessionID, onNativeSessionID)
	}()
	go func() {
		defer wg.Done()
		// Capture the head of stderr (capped) so a stale/missing --resume
		// session can be detected and the run retried fresh; the remainder is
		// drained to keep memory bounded.
		captureAgentAIStderr(stderr, &stderrBuf)
	}()
	wg.Wait()
	// StdoutPipe/StderrPipe must be fully drained before Wait. Calling Wait first
	// lets a short-lived CLI close the pipes while the reader goroutines still
	// have buffered JSON events, which intermittently dropped final deltas.
	waitErr := cmd.Wait()
	if bindingErr != nil {
		return agentAIRunDone, agentAIEffortRejection{}
	}

	if ctx.Err() != nil {
		status, errMsg := agentAIRunStoppedStatus(run.activity, limiter)
		// 先收敛存活的子 agent 再发终态：emitter 是终态屏障，ai.status 之后再
		// 发的 interrupted 会被静默丢弃。
		finalizeRunSubagents(run, writeJSON)
		statusPayload := map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		}
		if errMsg != "" {
			statusPayload["error"] = errMsg
		}
		_ = writeJSON(statusPayload)
		return agentAIRunDone, agentAIEffortRejection{}
	}
	if waitErr != nil {
		// The referenced --resume session is not resolvable in this cwd (e.g.
		// an imported session, or one created under a different project path).
		// Signal the caller to retry without --resume instead of erroring.
		if allowResume && isAgentAIResumeMissing(stderrBuf.String()) {
			return agentAIRunResumeMissing, agentAIEffortRejection{}
		}
		// --session-id 撞已落盘 jsonl（CLI 启动期身份校验拒绝，无 assistant 输
		// 出）：上报专用 outcome，让调用方释放 reserved 绑定、去掉 --session-id
		// 重跑一次 fresh，而不是把可自愈的失败直接抛给用户（2026-10-29
		// already-in-use 事故的兜底）。
		if !allowResume && newSessionID != "" && isAgentAISessionIDInUse(stderrBuf.String()) {
			return agentAIRunSessionIDInUse, agentAIEffortRejection{}
		}
		// CLI 在参数校验阶段拒绝了 --effort:极老世代连 flag 都没有(unknown
		// option),旧世代硬拒值并枚举合法清单。此时不可能有 assistant 输出,
		// 上报 agentAIRunEffortRejected 让调用方去 flag/按清单钳制后重试,而不是
		// 把这个可自愈的失败直接抛给用户。
		outMu.Lock()
		emittedAssistantOutput := output.Len() > 0
		outMu.Unlock()
		if rej := isAgentAIEffortRejected(stderrBuf.String()); rej.rejected && !emittedAssistantOutput {
			// 学进探测缓存:后续回合 spawn 前直接去档,不再重演第一次死亡。
			if rej.flagUnsupported {
				rememberClaudeEffortFlagUnsupported(tool.path)
			}
			return agentAIRunEffortRejected, rej
		}
		// Surface WHY the CLI exited. Prefer the structured cause derived from
		// the last api_retry (gateway status + retry count) when available; the
		// bare waitErr ("exit status 1") carries no reason on its own. The stderr
		// capture goes into "detail" (phone renders the structured cause, not the
		// raw stderr). Mirrors the codex app-server path's stderr enrichment.
		cause := claudeFailureCause(waitErr, lastRetry, stderrBuf.String())
		payload := agentAIErrorPayloadWithRetry(run.sessionID, run.messageID, errors.New(cause), lastRetry)
		if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
			payload["detail"] = truncateForCloud(stderrText)
		}
		finalizeRunSubagents(run, writeJSON)
		_ = writeJSON(payload)
		return agentAIRunDone, agentAIEffortRejection{}
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	filesMu.Lock()
	filesTouchedCount := len(filesTouched)
	filesMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, capturedResumeSessionID)
	// 谱系标记：手机回合（sdk-cli）若分叉于交互链，追加 cli 谱系标记让下次
	// resume 直接可见（2026-09-28 3d06bb04 实测病灶；详见 cc_session_lineage.go）。
	if tool.outputFormat == agentAIOutputClaudeStreamJSON {
		if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" {
			if jsonl := ccPeerSessionJSONL(externalTUIHome(), sid); jsonl != "" {
				if err := appendLineageMarker(jsonl, sid, run.projectPath, agentAILineageNotice); err != nil {
					logger.Info(fmt.Sprintf("lineage marker: session=%s error=%v", sid, err))
				}
			}
		}
	}
	if blocks := extractAgentAIOptionBlocks(assistantOutput); len(blocks) > 0 {
		m.emitOptionRequest(run, writeJSON, blocks)
	}
	done := map[string]interface{}{
		"type":                models.AgentEventAIDone,
		"session_id":          run.sessionID,
		"message_id":          agentAssistantMessageID(run.messageID),
		"files_touched_count": filesTouchedCount,
		"git_changed_count":   countGitChanged(run.projectPath),
	}
	// Echo provider-specific CLI session ids so the server can persist them
	// (sourceSessionId) and feed them back as resume_session_id after an agent
	// restart — keeping the conversation on one continuous local session.
	if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" && tool.outputFormat == agentAIOutputClaudeStreamJSON {
		done["claude_session_id"] = sid
	}
	if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" && tool.outputFormat == agentAIOutputOpenCodeJSON {
		done["opencode_session_id"] = sid
		done["source_session_id"] = sid
	}
	// 正常完成也要收敛：后台子 agent（async）在 ai.done 时可能仍在跑，按
	// interrupted 终结（服务端 finalizeTurnActivity 同语义兜底）。
	finalizeRunSubagents(run, writeJSON)
	_ = writeJSON(done)
	return agentAIRunDone, agentAIEffortRejection{}
}

func isAgentAIResumeMissing(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(stderr, "No conversation found with session ID") {
		return true
	}
	mentionsSession := strings.Contains(lower, "session") || strings.Contains(lower, "thread") || strings.Contains(lower, "rollout")
	missing := strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "no rollout")
	return mentionsSession && missing
}

// isAgentAISessionIDInUse 识别 claude CLI 对 --session-id 的「身份占用」拒绝：
// 该 id 的 transcript jsonl 已存在（上一轮同 id run 已落盘）时，CLI 拒绝以该
// id 开新会话并 exit 1。与 isAgentAIResumeMissing 同族，是它的镜像问题
// （resume 打不开 vs fresh 身份被占）。

func isAgentAISessionIDInUse(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "session id") && strings.Contains(lower, "already in use")
}

// captureAgentAIStderr reads everything from reader, storing the first cap
// bytes into buf and discarding the rest. It never blocks the run on stderr
// volume and bounds memory usage.

func captureAgentAIStderr(reader io.Reader, buf *strings.Builder) {
	const cap = 8 * 1024
	b := make([]byte, 4096)
	for {
		n, err := reader.Read(b)
		if n > 0 && buf.Len() < cap {
			room := cap - buf.Len()
			if room > n {
				room = n
			}
			buf.Write(b[:room])
		}
		if err != nil {
			return
		}
	}
}

// agentAIOutputLimiter bounds AI streamed output with the shared outputMeter.
// Reserve keeps the original (n int) int signature so the streaming call sites
// are unchanged: it returns n when the window has not tripped (emit the full
// chunk) and 0 once a sustained burst exceeds the rate window or the lifetime
// cap, which the caller treats as "stop the run" (it then calls run.cancel()).

type agentAIOutputLimiter struct {
	mu       sync.Mutex
	meter    *outputMeter
	exceeded bool
}

func (l *agentAIOutputLimiter) Reserve(n int) int {
	if l == nil {
		return n
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exceeded {
		return 0
	}
	if l.meter == nil {
		return n
	}
	if l.meter.add(n, time.Now()) {
		l.exceeded = true
		return 0
	}
	return n
}

func (l *agentAIOutputLimiter) Exceeded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exceeded
}

func streamAgentAIStdout(reader io.Reader, format agentAIOutputFormat, run agentAIRun, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string), fileSink func(string), lastRetry *claudeRetryInfo, resumeSessionID *string, onSessionID ...func(string)) {
	switch format {
	case agentAIOutputCodexJSON, agentAIOutputClaudeStreamJSON, agentAIOutputOpenCodeJSON:
		streamStructuredAIDelta(reader, format, run, writeJSON, limiter, capture, fileSink, lastRetry, resumeSessionID, onSessionID...)
	default:
		streamAIDelta(reader, run, "assistant", writeJSON, limiter, capture)
	}
}
