# 仪表盘子 agent 面板 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 网页仪表盘 aliangcode 对话页新增子 agent 状态面板——常驻入口条 → 面板列表 → 单个子 agent 详情(实时状态/工具时间线/完整报告),实时+历史回看。

**Architecture:** 数据面=journal 新增 `kind=subagent` 行(start/finish)+tool 行 `subagent_task_id` 关联字段(mirror 落行,tracked gate);详情 API 读侧组装 `subagents[]`(列表/同步路径剥离);前端=WS `ai.subagent` 消费 reducer + 入口条/面板/详情三组件 + 详情 API 历史装配。

**Tech Stack:** Go(agent journal/读侧/API)、Vue3(app/website,组合式 useAliangChat)、vitest、Playwright e2e。

**Spec:** `docs/superpowers/specs/2026-11-07-subagent-panel-design.md`(九条评审发现已落实)。

**基线事实(实现者必读):**
- journal 写侧 `app/http/services/agent_ai_agentcore_journal.go`(appendTool :558 风格:j.mu 串行+marshal+写行;单行上限 16MiB)
- mirror:`app/http/services/agent_ai_agentcore_subagent.go`(start :262/progress :284/finish :299;toolHook 组合 :486-497;**tracked=false 时 start 返回 ok=false**)
- 读侧:`app/http/services/agent_inventory_agentcore.go`(:91-334,switch 无 default=未知 kind 跳过;tool 行映射 :244-267)
- 详情链:`agent_detail.go:240-334`;列表剥离点:`agent_inventory.go:354-362`(summarizeAgentVibeSessions)
- wire:`app/http/models/agent.go`(AgentVibeSession :153/AgentVibeMessage :245)
- 前端:`app/website/src/composables/useAliangChat.js`(:37-41 重连归零集合/:2141 fallback/:2162 switch/:2404 default/:866,:916 tool 行丢弃)、`useDeviceSessions.js`
- **运行命令**:后端 `go test -count=1 ./app/http/services/ -run '...'`;前端 `cd app/website && npx vitest run <file>`;e2e `npx playwright test <file> --project=chromium`(locale zh-CN)
- **纪律**:TDD 先红后绿;worktree 开发;全中文提交;不自动 push;gopls 对 worktree 误报一律无视以 go build/test 为准

---

## 段一:后端数据面

### Task 1: journal 写侧三方法

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_journal.go`(appendTool :558 附近追加)
- Test: `app/http/services/agent_ai_agentcore_journal_test.go`(追加)

- [ ] **Step 1: 写失败测试**(照 appendTool 既有测试风格;newAgentCoreJournal(t.TempDir()) 构造):

```go
func TestAppendSubagentLines(t *testing.T) {
    j := newAgentCoreJournal(t.TempDir())
    sid := "sess-sub"
    taskID := "agentcore-msg7-researcher-1" // 含回合 messageID 消歧(spec H1)
    j.appendSubagentStart(sid, taskID, "researcher", "加密调研")
    j.appendSubagentTool(sid, taskID, "Grep", "grep 摘要", false)
    j.appendSubagentFinish(sid, taskID, "completed", "结论摘要",
        "完整报告全文", 8123, 45230)

    lines := readJournalLines(t, j.dir, sid) // 既有助手(agent_ai_agentcore_journal_test.go:18)
    var start, tool, finish map[string]interface{}
    for _, l := range lines {
        var m map[string]interface{}
        if json.Unmarshal([]byte(l), &m) != nil { continue }
        switch m["kind"] {
        case "subagent":
            if m["event"] == "start" { start = m } else { finish = m }
        case "tool":
            if m["subagent_task_id"] == taskID { tool = m }
        }
    }
    if start == nil || start["name"] != "researcher" || start["description"] != "加密调研" {
        t.Fatalf("start 行缺失/字段错: %v", start)
    }
    if tool == nil || tool["tool"] != "Grep" || tool["is_error"] != false {
        t.Fatalf("tool 关联行缺失/字段错: %v", tool)
    }
    if finish == nil || finish["status"] != "completed" || finish["result"] != "完整报告全文" ||
        finish["total_tokens"] != float64(8123) || finish["duration_ms"] != float64(45230) {
        t.Fatalf("finish 行缺失/字段错: %v", finish)
    }
}
```

- [ ] **Step 2: 跑测试确认红**:`go test -count=1 ./app/http/services/ -run TestAppendSubagentLines` → FAIL(undefined: appendSubagentStart)
- [ ] **Step 3: 最小实现**(appendTool 同款纪律:j.mu 锁内、json.Marshal、写行失败 Warn 吞):

```go
func (j *agentCoreJournal) appendSubagentStart(sessionID, taskID, name, description string) {
    j.appendSubagentLine(sessionID, map[string]interface{}{
        "kind": "subagent", "event": "start", "task_id": taskID,
        "name": name, "description": description,
    })
}

func (j *agentCoreJournal) appendSubagentTool(sessionID, taskID, tool, summary string, isError bool) {
    j.appendSubagentLine(sessionID, map[string]interface{}{
        "kind": "tool", "subagent_task_id": taskID, "tool": tool,
        "summary": summary, "is_error": isError, "ts": time.Now().UTC().Format(time.RFC3339),
    })
}

func (j *agentCoreJournal) appendSubagentFinish(sessionID, taskID, status, summary, result string, totalTokens, durationMs int64) {
    j.appendSubagentLine(sessionID, map[string]interface{}{
        "kind": "subagent", "event": "finish", "task_id": taskID, "status": status,
        "summary": summary, "result": result,
        "total_tokens": totalTokens, "duration_ms": durationMs,
        "ts": time.Now().UTC().Format(time.RFC3339),
    })
}

// appendSubagentLine: 加锁 + 补 ts(start 行) + marshal + 写行失败 Warn。
// 若 appendTool 内部已有私有 writeLine 助手则复用;ts 字段与既有行同款 RFC3339。
```

- [ ] **Step 4: 跑测试绿**;再跑全包 `-run 'TestAgentCoreJournal'` 无回归
- [ ] **Step 5: 提交** `新增:journal 子agent行写侧——start/finish/tool 关联行`

### Task 2: mirror 落行接线(tracked gate)

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_subagent.go`(mirror 结构/start/progress/finish/Execute hook)
- Modify: `app/http/services/agent_ai_agentcore.go`(runAgentCore mirror 构造处注 `journal: m.coreJournal`)
- Test: `app/http/services/agent_ai_agentcore_subagent_visibility_test.go`(追加)

- [ ] **Step 1: 失败测试**——
  a) happy path:mirror 构造带 `journal: newAgentCoreJournal(t.TempDir())`,Execute 后断言 journal 文件含 start/finish 行与带 subagent_task_id 的 tool 行;
  b) **taskID 消歧(spec H1)**:start 实现改为 `taskID := fmt.Sprintf("agentcore-%s-%s-%d", m.messageID, bridgeName, m.seq.Add(1))`;两个 mirror 实例(不同 messageID)各 start 同名桥,断言 taskID 不同;
  c) **cap 满 gating(spec L1)**:两条可行路径任选——主案 `m.registry = nil`(start 的 nil 接收者守卫直接返 false,单测一行);或 `newAgentAISubagentRegistry()` 循环灌 64 条 distinct toolUseID 的 active 记录顶满 cap(第 65 次 start 返 false)。断言 journal **无** start/finish/工具行(三类行同 gate,评审 F2)。**勿用「包装 stub」**——registry 字段是具体类型非接口,stub 赋不进。
  注:finish 落行必须收在 `registry.finish` 返回 ok 之内,且 Execute 的 defer finish 仅在 tracked 时调(拒收场景连 finish 都不调,孤儿行无从产生)。
- [ ] **Step 2: 红** → **Step 3: 实现**:

```go
// mirror 结构加字段
journal *agentCoreJournal
// start: taskID 生成含回合 messageID(spec H1 消歧,跨回合不重号);
// registry ok 后才落 start 行(tracked gate,spec L1)
taskID := fmt.Sprintf("agentcore-%s-%s-%d", m.messageID, bridgeName, m.seq.Add(1))
if ok {
    if m.journal != nil {
        m.journal.appendSubagentStart(m.sessionID, taskID, bridgeName, description)
    }
    m.activity.beginSubagent()
    m.emitRecord(record)
}
// progress: 不落 journal(时间线=finish 前的 tool 行已逐条落)
// finish: 仅 tracked(registry ok)时落;F2——拒收场景 start 未落,finish 也不落
if tracked && m.journal != nil {
    sum := truncateAgentText(summary, 200)
    res := truncateAgentCoreMCPOutputRuneSafe(result, 64*1024) // 新助手:rune 安全 64KB(见 Task 2b)
    m.journal.appendSubagentFinish(m.sessionID, taskID, phase, sum, res, totalTokens, duration.Milliseconds())
}
```

**Task 2b(前置小助手)**:`truncateAgentCoreMCPOutputRuneSafe(s string, maxBytes int) string`——字节封顶+回退 rune 边界(复用 truncateAgentCoreSubagentTask 的回退循环,去标记版);result 传参前调用。toolHook 组合里 journal 分支:

```go
// tracked 时 journal 走 appendSubagentTool(带关联);否则 b.onToolCall 内的 appendTool 原样
```

注意:现有 `onToolCall`(journal appendTool 闭包)在 tracked 分支里**不再调用**(子 run 工具行改走 appendSubagentTool,避免双行),未 tracked 时保持原样。

- [ ] **Step 4: 绿 + `TestAgentCoreSubagentMirror*` 全绿 + `-race`**(⚠️既有断言同步:visibility_test.go 的 taskID 前缀断言改为含 messageID 形态;finish 测试调用点随七参签名更新)
- [ ] **Step 5: 提交** `新增:mirror 落 journal 子agent行——start/finish/工具关联行(tracked gate)`

### Task 3: 读侧解析+wire+组装

**Files:**
- Modify: `app/http/models/agent.go`(AgentVibeSession.Subagents + AgentVibeSubagent/AgentVibeSubagentTool 类型)
- Modify: `app/http/services/agent_inventory_agentcore.go`(:152 switch 加 case "subagent";tool 行分支读 subagent_task_id 归组;返回结构携带)
- Test: `app/http/services/agent_inventory_agentcore_test.go`(追加)
- (detail 链 agent_detail.go:241→:321 直接复用读侧,reader 内填 Subagents 即自动覆盖,无需改)

- [ ] **Step 1: 失败测试**——构造临时 journal 文件(session 头+msg+subagent start/finish+带 subagent_task_id 的 tool 行),调 `readAgentCoreSessionMetaWithOptions`(或 detail 链入口),断言返回会话的 `Subagents` 长度/字段/Tools 归组排序;再断言:无 subagent 行的旧文件 → `len(Subagents)==0`;同名跨回合不合并:**两个 start 的 messageID 不同 → task_id 各含各自 messageID(必不同)**,断言 `len(Subagents)==2`;同 task_id 的行视为同一实例(读侧按 task_id 键合并是有意语义)。
- [ ] **Step 2: 红** → **Step 3: 实现**:读侧维护 `sub Entries map[taskID]*AgentVibeSubagent`+`order []string`;`case "subagent"` 按 event 分支;tool 行有 subagent_task_id → 归组(**不**转 AgentVibeMessage 进 transcript);detail 出口填 `session.Subagents`(列表路径照常填充,由 Task 4 剥离)。注意解析缓存按 (size,mtime)——Subagents 进缓存对象随行。
- [ ] **Step 4: 绿 + 全包 `-run 'TestAgentInventory|TestAgentCore'` 无回归**
- [ ] **Step 5: 提交** `新增:journal 子agent行读侧——subagents[] 组装与 tool 关联归组`

### Task 4: 列表剥离+契约测试

**Files:**
- Modify: `app/http/services/agent_inventory.go`(summarizeAgentVibeSessions :354-362 加 `session.Subagents = nil`)
- Test: `app/http/services/agent_service_test.go`(或 inventory 测试,追加)

- [ ] **Step 1: 失败测试**——detail 响应含 `subagents`;列表响应(`VibeSessions()`/summarize 后)**不含**该键(json marshal 断言无 `"subagents"`)
- [ ] **Step 2: 红** → **Step 3: 一行实现** → **Step 4: 绿**
- [ ] **Step 5: 提交** `修复:清单/同步路径剥离 subagents 载荷——详情契约≠清单契约`

## 段二:前端

### Task 5: reducer 纯函数+单测

**Files:**
- Create: `app/website/src/utils/subagentPanel.js`
- Test: `app/website/src/utils/__tests__/subagentPanel.test.js`

- [ ] **Step 1: 失败测试**(vitest):`applySubagentEvent(list, msg)`——started 新增/progress 更新(current_step/last_tool/tool_uses,**不建时间线**)/completed+failed 定稿(summary/时长);乱序(先 progress 后 started)容错;多 task 互不串线;会话归属由调用方保证(函数不管)。
- [ ] **Step 2: 红**(`cd app/website && npx vitest run src/utils/__tests__/subagentPanel.test.js`)→ **Step 3: 实现**(ESM 导出,不可变更新或浅拷贝)→ **Step 4: 绿**
- [ ] **Step 5: 提交** `新增:子agent面板状态 reducer——ai.subagent 事件纯函数聚合`

### Task 6: WS 接线+归属规则

**Files:**
- Modify: `app/website/src/composables/useAliangChat.js`(:37-41 集合加 'ai.subagent';handleAIEvent switch 加 case :2162 区;调用 reducer 更新会话维度状态;completed 时若面板打开→按 M1 只拉 subagents[])
- Test: `app/website/src/composables/useAliangChat.subagent.test.js`(同目录共置约定,vitest glob src/**/*.test.js)

- [ ] **Step 1: 失败测试**(模拟 handleAIEvent 派发 ai.subagent 帧→断言状态变化+他 session 帧被丢弃)
- [ ] **Step 2: 红** → **Step 3: 实现**(归属规则:帧 session_id ≠ 当前会话 id 直接 return;completed 静默重拉仅限用户正在看该子 agent 详情时触发,只消费 subagents[] 转录不落壳;不在看则不拉——由 Task 8 的「打开面板时拉」兜底,spec M1 口径)
- [ ] **Step 4: 绿 + 既有 useAliangChat 测试无回归**
- [ ] **Step 5: 提交** `新增:仪表盘 WS 消费 ai.subagent——会话归属守卫+状态聚合`

### Task 7: 入口条组件

**Files:**
- Create: `app/website/src/components/chat/SubagentStatusBar.vue`
- Modify: `ChatPage.vue`(挂载;仅 agentcore 会话且 subagents 非空)
- Modify: `app/website/src/i18n/zh.js`+`en.js`(键:chat.subagentBar 等)
- Test: vitest 组件测试(挂载/文案/点击 emit)

- [ ] **Step 1: 失败组件测试** → **Step 2: 红** → **Step 3: 实现**(运行中 spinner+计数,点击 emit('open'))→ **Step 4: 绿** → **Step 5: 提交** `新增:子agent状态入口条`

### Task 8: 面板+详情组件

**Files:**
- Create: `app/website/src/components/chat/SubagentPanel.vue`(drawer:桌面右侧滑出/移动全屏;列表)
- Create: `app/website/src/components/chat/SubagentDetail.vue`(状态头/时间线/报告 markdown+复制)
- Modify: `ChatPage.vue`/(状态接线)
- Modify: i18n 两文件
- Test: vitest 组件测试(列表渲染/切换/失败标红/报告渲染/运行中占位)

- [ ] **Step 1: 失败测试** → **Step 2: 红** → **Step 3: 实现**(响应式:桌面 drawer、<768px 全屏;触控≥40px;报告 markdown 用既有渲染器)→ **Step 4: 绿** → **Step 5: 提交** `新增:子agent面板与详情视图`

### Task 9: 历史装配+断线刷新

**Files:**
- Modify: `useAliangChat.js`(openDeviceSession/详情拉取后:subagents[] 入面板状态;断线重连成功→仅消费 subagents[] 的刷新,转录不落壳)
- Test: reducer/装配单测追加(详情响应→面板状态)

- [ ] **Step 1: 失败测试** → **Step 2: 红** → **Step 3: 实现** → **Step 4: 绿** → **Step 5: 提交** `新增:面板历史装配与断线自愈刷新`

## 段三:e2e

### Task 10: e2e

**Files:**
- Create: `app/website/tests/e2e/subagent-panel.spec.js`(locale zh-CN 坑:断言中文须设 locale)

- [ ] **Step 1: 空跑一次确认红**(测试尚无实现路径,预期失败)
- [ ] **Step 2: 实现**(**禁写真实 agent home**):详情 API route fixture 预置 subagents[] + 既有 `window.__aiFrames` 帧注入 harness 注入 ai.subagent 事件(同 chat-device-sessions.spec.js 的 openChat route 表模式)→ 打开会话→入口条出现→点击→面板→切换→详情字段
- [ ] **Step 3: 跑绿**(`cd app/website && npx playwright test tests/e2e/subagent-panel.spec.js --project=chromium`)
- [ ] **Step 4: 提交** `新增:子agent面板 e2e`

### Task 11: 收尾验证

- [ ] `go build ./...` + 全仓 vet + 三包 `-count=1` + `-race`
- [ ] `cd app/website && npx vitest run`(全量)+ e2e 全量
- [ ] 汇报(不自动 push)

---

## 既有陷阱速查(实现者必读)

- `newAgentAIManager()` 构造 manager(nil map panic 勿用 `&agentAIManager{}`)
- 测试回调 goroutine 禁 t.Fatal
- `_windows_test.go` 在 darwin 静默跳过:新测试勿用 GOOS 尾段命名
- 前端 e2e 断言中文须 locale zh-CN;双实例 testid 须 scope
- services 全包有预存 flake(subprocess 2s/5s 高并行偶败):单包复跑即绿勿当回归
- 新 worktree 首次 `go build` 需从主仓 `cp -R app/website/dist <worktree>/app/website/`(embed 占位)
