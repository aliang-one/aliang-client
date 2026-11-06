# AI 回合停滞三修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 根治「手机对话老是停下来且毫无反馈」：① 边界守卫升级的审批卡不再被服务端 allow_all 自动批吞掉（用户能看到并裁决）；② 工具挂死兜底从 1 小时缩到 20 分钟；③ 回合失败（如上游 502）在聊天气泡可见。

**Architecture:** 三修复分属两仓。修复一仅改 PhoneServer intake 自动批豁免（agent 侧载荷已带 `matched_rule_id`，仅核验）；修复二改 agent watchdog 常量默认值；修复三改 agent inventory（journal `end` 错误行合成 system 转录行）+ 验证服务端/App 的 system 行透传与渲染。全部走既有契约面，无新协议层。

**Tech Stack:** Go（alianggate agent）、TypeScript/zod（AliangPhoneServer）、React Native（AliangVibeCodingPhone，仅验证+可能的小改）。

**证据基线（2026-11-01 会话诊断，journal+core log 实锤）:**
- 会话 `agentcore-1793348975299332000-2`（agent 侧 `ai_1sll913fE82l`，mode=voice）
- 10-30 16:46 上游 502 重试 5 次后 `ai.run.terminal type=ai.error`，手机端**零可见反馈**（journal end 行只进 Diagnostics 不进转录）
- 11-01 09:15 工具挂死 1h 后被 watchdog `tool_wait_timeout` 强杀（`awaiting=false pendingToolUses=2`）
- 「看不到审批申请」实锤：PhoneServer `intake.ts` 步骤5 对 allow_all 会话的权限类审批**自动批+不推手机**——把 agent 边界守卫刻意升级的卡也吞了，与 agent 侧「越界必须用户确认」设计相抵触
- PhoneServer intake.ts 注释即自证：「权限类审批在 allow_all 会话里对用户不可见」

**工作流约束（记忆规则）:** 两仓均在 git worktree 分支开发，不动主检出；提交标题全中文「修复：/新增：」；提交尾部 `Co-Authored-By: Claude Code <noreply@anthropic.com>`；跨平台测试名避开 GOOS 尾段。

**执行顺序:** 按用户指定 修复一 → 修复二 → 修复三。

---

## 修复一：边界升级审批卡可见（agent + PhoneServer）

**背景:** agent 侧 `agentCoreApprove`（agent_ai_agentcore.go:2052 起）在策略命中边界守卫（`outside-project-path` / `outside-project-bash`）时强制 `require_approval`，卡片 `Reason` 已带人话（`boundaryApprovalReason`，:2040），且 `agentAIApprovalRequestPayload` **已下发 `matched_rule_id`**（agent_ai.go:9973-9975，CLI 两路径 :4395/:6419 均已填充）；PhoneServer `intake.ts` 步骤5 只看 `kind`（tool/command 均视为权限类）+ allow_all/MCP 前缀就自动批，**完全没消费 `matched_rule_id`**。结果：安全升级卡被服务端静默中和。

### Task 1.1: agent 载荷核验（只读，零改动）

**Files:**
- Verify only: `app/http/services/agent_ai.go:9973-9975`（`matched_rule_id` 已在 payload）、`:4395`/`:6419`（agentCore/CLI 路径已填充 `MatchedRuleID`）

- [ ] **Step 1: 核验** — 确认上述三处存在（评审已证）；边界升级路径（`agentCoreApprove` 的 `decisionRequireApproval` 分支设 `run.matchedRuleID = matchedID`）最终流入 payload。**agent 仓不产生提交**；核验结论写进 Task 1.2 的 PhoneServer 提交说明里，**agent 侧零改动**

### Task 1.2: PhoneServer intake 豁免边界升级卡

**Files:**
- Modify: `server/src/modules/agent/handlers/approval.ts`（`handleAiApprovalRequest` 与 `ai.approval.sync` 恢复路径两处的 draft 构造，把 `matched_rule_id` 透传进 draft；先 grep `tool_name:` 定位 draft 字段装配点）
- Modify: `server/src/modules/approval/intake.ts`（步骤5 前加边界豁免闸）
- Modify: `server/src/modules/approval/intake.ts`（新增导出）

```typescript
// 边界守卫升级规则 id（与 agent 侧 boundaryApprovalReason 一一对应）：
// 这类卡是 agent 刻意凌驾本地策略要求用户裁决的安全升级，allow_all/MCP
// 前缀自动批必须豁免，否则升级被服务端静默中和（2026-11-01 ai_1sll913fE82l 事故）。
export const BOUNDARY_ESCALATION_RULE_IDS = new Set([
  'outside-project-path',
  'outside-project-bash',
]);
export const isBoundaryEscalation = (ruleId: string | null | undefined): boolean =>
  !!ruleId && BOUNDARY_ESCALATION_RULE_IDS.has(ruleId);
```

intake 步骤5 守卫改法（最小 diff）：自动批条件加边界豁免前置：

```typescript
    if (!isBoundaryEscalation(draft.matched_rule_id) &&
        (matchedPrefix || resolvedPolicy?.scheme === 'allow_all')) {
```

（若 `draft` 无 `matched_rule_id` 字段：在 draft 类型/构造处同步加，zod/类型如无此键则补 optional 字段；handler 侧从 `message.matched_rule_id` 读取——评审已核 draft 装配点在 handlers/approval.ts:174 与 :305 两处，两处都要透传。）

- [ ] **Step 1: 写失败测试** — 在 intake 现有测试目录（`grep -rl "intakeApproval" server/test`）加用例：allow_all 会话 + `matched_rule_id: 'outside-project-path'` 的 command 审批 → 断言 outcome 非 autoResolved、approval.status==='pending'、（若可断言）走了升级面；对照组：无 matched_rule_id 同条件 → autoResolved=true
- [ ] **Step 2: 跑测确认失败** — `cd /Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer && npx vitest run <testfile>`（以仓内既有 runner 为准，先看 package.json scripts）
- [ ] **Step 3: 实现**（draft 透传 + 豁免闸，代码如上）
- [ ] **Step 4: 跑测确认通过 + 全量 `npm test`（或仓内等价命令）绿**
- [ ] **Step 5: 提交** — `git commit -m "修复:边界守卫升级审批卡不再被 allow_all 自动批,恢复推送手机"`（Co-Authored-By 尾行）

---

## 修复二：工具挂死兜底 1h → 20m（agent）

**背景:** `agentAIToolWaitCeiling` 默认 1h（agent_execution_guard.go:156）。11-01 事故用户对着假「running」干等 1 小时。agentcore 内核单工具上下文已有 10min 超时（app/agentcore/loop.go:1327），claude CLI Bash 单调用上限 10min——健康工具必然 ≤10min 返回，20m 默认留 2× 余量。长任务正路是子代理（独立 6h 上限 + task_progress 重置工具等待钟，均不受影响）。

### Task 2.1: 调默认值 + 存量断言同步 + 文档同步

**Files:**
- Modify: `app/http/services/agent_execution_guard.go:148-156`（默认值与 Env 注释）
- Modify: `app/http/services/agent_execution_guard_test.go:58-59`（**存量断言要同步改**：现断言 `agentAIToolWaitCeiling != time.Hour → want 1h`，自带 env-skip 守卫 :46-48）
- Modify: 全仓 grep `Default 1h` 相关注释同步

- [ ] **Step 1: 改失败测试** — 把 `agent_execution_guard_test.go:58-59` 断言改为 20m：

```go
	if agentAIToolWaitCeiling != 20*time.Minute {
		t.Fatalf("default agentAIToolWaitCeiling = %s, want 20m", agentAIToolWaitCeiling)
	}
```

- [ ] **Step 2: 跑测确认失败** — `go test ./app/http/services/ -run TestAgentAIExecutionGuardDefaults -count=1`（以该测试实际函数名为准，`grep -n "func Test" agent_execution_guard_test.go | head`），FAIL（默认仍为 1h）
- [ ] **Step 3: 实现** — `agent_execution_guard.go:156` 改 `resolveEnvDurationAllowZero("ALIANG_AI_TOOL_WAIT_CEILING", 20*time.Minute)`；同处注释「Default 1h」改「Default 20m」并补一句：健康单工具 ≤10min（内核 toolCtx 10min/CC Bash 10min），20m=2× 余量；长任务走子代理（6h 上限+progress 重置）
- [ ] **Step 4: 全仓 grep `Default 1h` 相关注释同步**（`grep -rn "Default 1h" app/http/services/ app/`），跑改后测试 + `go test ./app/http/services/ -run TestAgentAIWatchdog -count=1` 确认 watchdog 族绿
- [ ] **Step 5: 提交** — `git commit -m "修复:工具等待兜底默认 1h 收紧为 20m,挂死回合 20 分钟内可见终态"`（Co-Authored-By 尾行）

---

## 修复三：失败终态上聊天气泡（agent inventory + 服务端/App 验证）

**背景:** journal `end` 行（agent_ai_agentcore_journal.go:327 写入，含 error+diagnostics）在 inventory（agent_inventory_agentcore.go case "end"）只折叠进 `session.Diagnostics`（诊断面板），转录（Transcript）不含它——手机聊天页对失败**零可见**。转录契约 `role` 枚举服务端已支持 `'system'`（schemas.ts:501），App `agentTranscript.ts` 已消费 system 行（:387-392），**无需新契约**。

### Task 3.1: inventory 把 end 错误行合成 system 转录行

**Files:**
- Modify: `app/http/services/agent_inventory_agentcore.go`（case "end"，现 :224-232）
- Test: `app/http/services/agent_inventory_agentcore_test.go`（追加用例）

- [ ] **Step 1: 写失败测试** — 构造含 `{"kind":"end","status":"ai.error","error":"agentcore: 上游返回 HTTP 502 …","ts":"…"}` 的 journal fixture，断言 `session.Transcript` 末尾出现 `Role=="system"`、Content 含「本轮执行失败」与 502 字样的行；且 `session.MessageCount` 不变（同 tool 行不计）；无 error 的干净 end 行不产生转录行

```go
// 断言骨架
var found bool
for _, m := range session.Transcript {
    if m.Role == "system" && strings.Contains(m.Content, "本轮执行失败") {
        found = true
    }
}
if !found { t.Fatal("end error row should synthesize a system transcript message") }
```

- [ ] **Step 2: 跑测确认失败** — `go test ./app/http/services/ -run AgentInventoryAgentcore -count=1`（按新用例名收窄），FAIL
- [ ] **Step 3: 实现** — case "end" 内 `row.Error != ""` 时追加（沿用 tool 行模式：稳定 ID、计 transcriptRow、不计 MessageCount/displayCount、不置 UpdatedAt）：

```go
	if row.Error != "" {
		transcriptRow++
		window.add(models.AgentVibeMessage{
			ID:        fmt.Sprintf("end-err-%06d", transcriptRow), // 窗口不去重,计数式 ID 避免多 end 行撞号（同 tool-%06d 模式）
			Role:      "system",
			Content:   "本轮执行失败: " + truncateAgentText(strings.TrimSpace(row.Error), agentVibeTranscriptMaxContentRunes),
			Timestamp: normalizeAgentTime(row.Ts),
			Index:     transcriptRow - 1,
		})
	}
```

- [ ] **Step 4: 跑测通过 + `go build ./...` + services 包全量 `go test ./app/http/services/ -count=1`（对照基线 flake 清单，子进程超时类偶败单包复跑）**
- [ ] **Step 5: 提交** — `git commit -m "新增:journal end 错误行合成 system 转录行,回合失败原因在聊天页可见"`（Co-Authored-By 尾行）

### Task 3.2: 服务端透传验证（PhoneServer，可能需小改）

- [ ] **Step 1: grep 验证** `role: z.enum(['user','assistant','system','tool'])`（schemas.ts:497-501）已确认 role 枚举含 system；**关键疑点是 ingest 边界**：schemas 注释自述 role:'tool' 行过 zod 后在 `buildReportedAiSession` 边界被过滤「不放给手机端」——沿该边界核查 role:'system' 行是否同样被滤（`grep -rn "buildReportedAiSession" server/src` + 读过滤分支）
- [ ] **Step 2: 若 system 行被边界过滤**：最小修正（如按 content 前缀「本轮执行失败」或新增 role 白名单放行）+ 补服务端测试；若天然放行则记录结论
- [ ] **Step 3: 有改动则提交**（`git commit -m "修复:回合失败 system 行透传手机端,不被会话详情边界过滤"`，Co-Authored-By 尾行）

### Task 3.3: App 渲染验证（AliangVibeCodingPhone，小改+单测）

**Files:**
- Verify/Modify: `src/utils/agentTranscript.ts`（:83-92 的 system 噪音过滤不得吞「本轮执行失败」行；:387-392 system 行构建）
- Verify: `src/screens/vibecoding/`（system 行的气泡渲染组件；确认视觉上是一行式状态条而非折叠隐藏）
- Test: `src/utils/__tests__/` 或 `__tests__/`（沿既有 agentTranscript 测试文件）

- [ ] **Step 1: 写单测** — system 行 content=`本轮执行失败: agentcore: 上游返回 HTTP 502 …` 经 `buildDisplayTranscript` 后保留为 `role:'system'` 条目、不被噪音过滤剔除；chat 渲染层若有按 role 的快照/分支测试，补一条 system 失败行用例
- [ ] **Step 2: 跑测确认**（若现有过滤已放行则测试直接绿——仍是有效回归锚；若被吞则修过滤条件后转绿）
- [ ] **Step 3: 真机视觉核对留给部署后冒烟**（本计划不含）
- [ ] **Step 4: 提交** — `git commit -m "新增:回合失败 system 行经转录过滤保留,失败原因聊天气泡可见"`（Co-Authored-By 尾行）

---

## 验收（全部合入后）

- [ ] agent 侧：`go build ./... && go test ./app/http/services/ -count=1` 全绿（对照预存 flake 基线）
- [ ] PhoneServer：仓内全量测试绿
- [ ] App：vitest 全绿（对照既有 3 红基线为并行线预存，勿当回归）
- [ ] 手工联调（部署后）：①allow_all 项目里让 agent 执行项目目录外命令 → 手机弹边界卡；②构造挂死 → 20 分钟内出 stalled 终态；③断上游 → 聊天页出现「本轮执行失败: …」气泡

## 明确不做（本计划外）

- ai.run.progress 心跳增加等待态字段 + 手机「等待中」状态条（20 分钟兜底+失败气泡已覆盖主要体感，留 polish backlog）
- 服务端 auto-batch 的「已自动放行」事件可视化为会话事件流
- agentcore 工具执行挂死（pendingToolUses 泄漏）的内核级根因定位（需独立复现，兜底已止血）
