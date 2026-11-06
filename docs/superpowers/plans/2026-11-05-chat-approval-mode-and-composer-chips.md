# 聊天审批模式+composer chips Implementation Plan

> **执行状态(2026-11-05):全部任务已执行完毕,改动留在 worktree `feature/chat-approval-mode` 未提交(等用户指示)。**
> 验证:Go config/services/handlers 三包绿(归一/带键标记/三内核覆盖矩阵/记忆跳过/journal 头行同步/bypass 快路径);vitest 270/270;build 绿;e2e 全量 61/61(chat-recall 并行负载下偶发 flake,单包/串行复跑均绿)。
> 执行要点:①approval_mode 带「键存在」标记(F2 纪律,老客户端不带键 preserve,防冲掉 bypass_all);②journal 头行 kind=session 挂键+显式改键 syncSessionApprovalMode;③回读腿=inventory 头行解析→AgentVibeSession.ApprovalMode→(前端回填待后续,芯片态由 conv.approvalMode 持久化保真);④三内核 bypass 快路径仅 svc 缺失时走(svc 在场 deny 照拒,由块内 override 处理);⑤spec reviewer 抓到 codex 第三决策点(:6372)与回读腿缺失,已折入。
> 偏差:e2e chat-settings selectOption→chip 点击流迁移;顶栏 pending 角标随迁工具行(单 testid 去重)。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 会话级 `approval_mode`(空/ask_all/bypass_all) 三内核统一生效;composer 底部三 chip(权限模式/模型/effort),顶栏旧模型/effort 选择器移除。

**Architecture:** 复用 effort/model 既有管道——WS create 载荷新键 `approval_mode` → session 字段 → agentAIRun 镜像 → agentCoreApprove 与审批 hook 两处短路径(共享小判定函数) → journal 回合头 → status 回显;前端 applySessionSettings/resendCreate 管道原样,chip 控件迁入 composer。

**Tech Stack:** Go(services/protocol)、Vue 3 Options API + composables、vitest、go test。

**Spec:** `docs/superpowers/specs/2026-11-05-chat-approval-mode-and-composer-chips-design.md`

**Worktree:** `.worktrees/chat-approval-mode`(branch `feature/chat-approval-mode`,基线 master@d4ed54a)。

**提交纪律:** 全程不自动 commit。

---

### Task 1: 后端——协议键/会话字段/run 镜像(TDD)

**Files:**
- Modify: `app/http/services/agent_ai.go`(四个锚点:~1845 create 解析、~2224 run 构造、~2492 session apply、~3475 status 回显)
- Modify: `app/http/models/agent_protocol.go`(~318 create optional keys 加 "approval_mode")
- Test: `app/http/services/agent_ai_test.go` 或就近文件

- [ ] **Step 1: 写失败测试**(归一函数 + 解析)

```go
// approval_mode 白名单:空/ask_all/bypass_all,非法回落空。
func TestNormalizeAgentAIApprovalMode(t *testing.T) {
	cases := map[string]string{
		"": "", "ask_all": "ask_all", "bypass_all": "bypass_all",
		"ASK_ALL": "", "yolo": "", "  ": "",
	}
	for in, want := range cases {
		if got := normalizeAgentAIApprovalMode(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: 跑失败** → **Step 3: 实现**

`normalizeAgentAIApprovalMode(s string) string`:trim + 只认 `ask_all`/`bypass_all`,其余返回 ""。

四处管道挂键(与 effort 完全同款):
- create 解析(~1845):`approvalMode := normalizeAgentAIApprovalMode(remoteString(msg, "approval_mode"))`
- run 构造(~2224):`approvalMode: normalizeAgentAIApprovalMode(remoteString(msg, "approval_mode"))`(字段名按该处实际 struct 为准)
- session apply(~2492):`session.approvalMode = normalizeAgentAIApprovalMode(remoteString(msg, "approval_mode"))`
- status 回显(~3475):`"approval_mode": session.approvalMode`

`agentAIRun` 与 session 结构体各加 `approvalMode string` 字段(注释:空=策略默认;ask_all=auto_approve 降级 require;bypass_all=require 降级放行,auto_deny 恒拒)。

protocol models(~318)create Optional 加 `"approval_mode"`。

- [ ] **Step 4: go test 相关包 PASS**

---

### Task 2: 后端——决策短路径(三内核,TDD)

**Files:**
- Modify: `app/http/services/agent_approval_policy.go`(共享判定函数)
- Modify: `app/http/services/agent_ai_agentcore.go`(~2119 agentCoreApprove)
- Modify: `app/http/services/agent_ai.go`(~4358 hook 路径)
- Test: 就近 _test.go

- [ ] **Step 1: 写失败测试**(矩阵)

```go
// ref=decisionRequireApproval(app), ap=decisionAutoApprove, ad=decisionAutoDeny
// mode=""      → app/ap/ad 原样
// mode=ask_all → app→app; ap→app(降级); ad→ad(护栏)
// mode=bypass_all → app→ap(跳手机); ap→ap; ad→ad(护栏)
func TestApprovalModeDecisionMatrix(t *testing.T) { ... }
```

- [ ] **Step 2: 实现**

`agent_approval_policy.go`:

```go
// applyApprovalModeOverride 会话级审批模式对策略决策的覆盖(三内核共用):
//   - bypass_all: require(含会话记忆/MCP 默认门前的 require)降级为本地放行
//     (跳手机云端往返);auto_deny 是安全护栏,恒不越过。
//   - ask_all: auto_approve 降级为 require(每次问);auto_deny 恒拒。
//   - 空:现状。
func applyApprovalModeOverride(mode string, decision policyDecision) policyDecision
```

接线:
- `agentCoreApprove`:策略评估后、`switch decision` 处对 decision 过 override(bypass_all 下 decisionRequireApproval→decisionAutoApprove,日志带 `mode=bypass_all`;ask_all 下 decisionAutoApprove→decisionRequireApproval)。会话记忆检查点:ask_all/bypass_all 时跳过 memory 命中分支(bypass 反正放行,ask_all 显式要求每次问)。注意 run 镜像字段 `run.approvalMode` 传入。
- hook 路径(~4358):同一 override 接在 evaluateApprovalDecisionForHook 的 switch 前;会话记忆检查点同款跳过。goal run 分支(~4340-4355)不接管。

- [ ] **Step 3: journal 回合头**

回合头行(journal kind=turn,写 effort/model 处)加 `"approval_mode": run.approvalMode`(与 effort 同族键)。

- [ ] **Step 4: go test PASS**

---

### Task 3: 前端——chatSettings 纯函数 + 状态管道(TDD)

**Files:**
- Modify: `app/website/src/utils/chatSettings.js`(常量+payload)
- Modify: `app/website/src/composables/useAliangChat.js`(applySessionSettings ~324 加 approval_mode 挂角标;~2000 回显;create 载荷带键;conversationForStorage 保留)
- Test: `app/website/src/utils/chatSettings.test.js`

- [ ] **Step 1: 失败测试**

```js
describe('approval mode', () => {
  it('词表封闭 + 归一', () => {
    expect(APPROVAL_MODES).toEqual([
      { value: '', labelKey: 'chat_approvalModeDefault' },
      { value: 'ask_all', labelKey: 'chat_approvalModeAskAll' },
      { value: 'bypass_all', labelKey: 'chat_approvalModeBypassAll' }
    ]);
    expect(normalizeApprovalMode('bypass_all')).toBe('bypass_all');
    expect(normalizeApprovalMode('yolo')).toBe('');
  });
  it('create 载荷带 approval_mode(与会话值同源)', () => {
    const p = buildCreatePayload({ provider: 'agentcore', approvalMode: 'ask_all' });
    expect(p.approval_mode).toBe('ask_all');
  });
});
```

(常量/函数名按现有文件风格对齐;buildCreatePayload 以实际签名为准。)

- [ ] **Step 2: 实现**

- `chatSettings.js`:`APPROVAL_MODES` 词表 + `normalizeApprovalMode`(trim,只认两值) ;`buildCreatePayload` 加 `approval_mode: normalizeApprovalMode(conv.approvalMode)`。
- `useAliangChat.js`:applySessionSettings 挂角标条件加 `'approval_mode' in patch`;conv 字段 `approvalMode`(conversationForStorage 保留);create 回显(~2000)`if (typeof e.approval_mode === 'string') conv.approvalMode = e.approval_mode`。

- [ ] **Step 3: vitest 该文件 PASS**

---

### Task 4: 前端——composer 三 chip + 顶栏瘦身

**Files:**
- Modify: `app/website/src/components/chat/ChatComposer.vue`(底部 chip 行)
- Modify: `app/website/src/components/chat/ChatSettingsBar.vue`(移除模型/effort 两控件及其 computed 桥)
- Modify: `app/website/src/components/ChatPage.vue`(事件接线:新 chip 的 apply-settings 路由到现有 applySessionSettings)
- Modify: `app/website/src/i18n/zh.js`、`en.js`(chat_approvalMode* / 确认文案)

- [ ] **Step 1: composer 底部行**

textarea 容器底部加一行(发送键左):三个控件。实现载体:本行直接放 ChatComposer 内(事件 emit 到 ChatPage)或由 ChatPage 把控件作为 slot 传入——**以现 ChatComposer 的 props/emits 结构最小侵入为准**(倾向:ChatComposer 新增受控 props:`approvalMode/modelOptions/model/effort/activeRunning` + emits `apply-settings`;与 ChatSettingsBar 的 props 桥同构)。

- 权限模式 chip:`🛡️ {label} ▾`,点击开 absolute 下拉(挂外层容器防裁剪,同目录面板手法);三选;**切 bypass_all 先 confirm**(window.confirm 或仓内现有确认弹层——按现有模式,文案 `chat_approvalModeBypassConfirm`);bypass 挂起/生效时 chip 红色警示类。
- 模型 chip:复用 ChatSettingsBar 的 modelOptions/自定义输入逻辑(modelCustomActive/modelCustomDraft/commitCustomModel 随迁),点击弹下拉面板含自定义输入。
- effort chip:下拉 EFFORT_OPTIONS。
- 运行中禁用(activeRunning);「下轮生效」角标移到 composer 行(或保留顶栏——**按 pendingConfig 角标现挂点最小改动**,角标可留顶栏)。

- [ ] **Step 2: ChatSettingsBar 瘦身**

移除模型 select+自定义输入+effort select 及其 computed 桥(activeModel/activeEffort/modelOptions/EFFORT_OPTIONS 引用);目录/更多/预算面板/错误行不动。

- [ ] **Step 3: ChatPage 接线**

ChatSettingsBar 减掉的 props/事件从 ChatPage 移给 ChatComposer;apply-settings 事件两处都路由到现有 applySessionSettings。

- [ ] **Step 4: i18n + vitest + build**

```js
// zh
chat_approvalMode: '权限模式',
chat_approvalModeDefault: '默认推荐',
chat_approvalModeAskAll: '每次询问',
chat_approvalModeBypassAll: '放过所有',
chat_approvalModeBypassConfirm: '「放过所有」将跳过手机审批卡,设备拒绝规则仍然生效。确认切换?',
chat_approvalModePending: '审批模式下轮生效',
// en 对应
```

Run: `npx vitest run && npx vite build`
Expected: 全绿

---

### Task 5: e2e 与全量收尾

- [ ] **Step 1: e2e 断言迁移**

`tests/e2e/chat-settings.spec.js`:模型/effort 选择器断言从 `[data-testid="chat-settings-model"]`(顶栏)改为 composer chip 的 testid(如 `chat-composer-model`);新增权限模式 chip 断言(三选 + bypass 确认弹层)。
Run: `npx playwright test tests/e2e/chat-settings.spec.js tests/e2e/mobile-responsive.spec.js`

- [ ] **Step 2: go 相关包全量**

`go build ./... && go test ./app/http/services/ -run 'Approval|PeerMCP'`
Expected: PASS(journal truncate 预存失败按基线认知)

- [ ] **Step 3: 冒烟清单**(报告用)

1. composer 三 chip 渲染;顶栏旧模型/effort 消失,目录/更多正常
2. 切 bypass_all→确认弹窗→chip 红+「下轮生效」→下回合跑工具手机不弹卡(设备 deny 规则照拒)
3. 切 ask_all→读文件也要确认;会话记忆不再放行
4. 默认档:行为与现状一致
5. 手机端照常弹卡(默认态)
6. 刷新页面模式保留;运行中 chip 禁用

- [ ] **Step 4: 报告**(不 commit)
