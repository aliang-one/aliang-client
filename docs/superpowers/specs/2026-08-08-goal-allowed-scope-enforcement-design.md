# Goal allowed_scope 执行边界强制 — 设计

- **日期**: 2026-08-08
- **状态**: 设计待审 (Draft, pending review)
- **作者**: Claude (goal 复查会话)
- **影响代码**: `app/http/services/agent_ai.go`, `app/http/services/agent_goal.go`(goal_context 解析 helper), `app/http/services/agent_approval_policy.go`(复用 helper), `app/http/services/agent_scan_dirs.go`(复用)
- **行号约定**: 本文行号针对 `master@4029cea` + 本会话已落地的 Gap A/C 改动后的工作树；以 **symbol 名**为准（行号会漂）。
- **前置**: Gap A（envelope 渲染）、Gap C（假完成修复）已落地于 master（本批 UNCOMMITTED）

---

## 1. 背景

goal 复查发现执行端三个结构缺陷。Gap A（执行器看不到围栏/计划/约束）与 Gap C（零检查任务谎报完成）已修。本 spec 处理 **Gap B**：每个 goal task 的批准范围（`allowedRoots` + `allowedCommands`）目前只是**提示边界，不是执行边界**——AI 实际干活时工具完全敞开，越界无人拦。

### 1.1 现状（已第一手核验）

- 服务端在 `buildGoalContext`（`AliangPhoneServer/server/src/modules/goal/context.ts:163`）把 `task.allowedRoots` + `task.allowedCommands` 塞进 `goal_context` 信封，经 WebSocket 下发。**数据已到达设备 agent。**
- 设备 agent 侧，这两个字段**只在 `serializeGoalContext`（`agent_ai.go:6117/6126`）当文本渲染进提示词**——渲染完即丢，不作为"规则"留作校验。
- claude 执行 goal task 时，工具调用的授权走 `PreToolUse` hook（`handleClaudeApprovalHook`，`agent_ai.go:2618`；goal-run 分支 `if len(run.goalIdentity) > 0` 在 `:2677`）。该分支注释声称 *"plan was pre-approved by the user (with allowed_roots + allowed_commands)"*，但实现**只查 `decisionAutoDeny`（写死危险命令名单）**，对其它工具一律自动放行，**从不读 roots/commands**。注释与实现不符：强制根本不存在。
- 全 agent 代码中 `allowedRoots`/`allowedCommands` 的消费点仅 `serializeGoalContext` 两处文本渲染。

### 1.2 风险

一个 goal task 可能在执行中**悄悄**改了围栏外的文件、跑了未批准的命令，用户后知后觉。每个 task 自带批准范围的设计意图（containment）目前落空。

---

## 2. 目标 / 非目标

### 目标
- 把 allowed_scope 从"提示词里的一句话"变成**运行时硬校验**：goal task 执行中，越界的工具调用在 claude 子进程动手前被 agent 挡掉。
- claude 主路径全覆盖（Edit/Write/MultiEdit/NotebookEdit 文件路径校验 + Bash 命令校验）。
- 复用现有基建（`pathUnderAnyScanDir`、`evaluateApprovalDecision` 的 hook 拓扑、`claudeApprovalCommand`、Gap C 的 goal_context→run→emitter plumbing 模式）。

### 非目标（YAGNI，明确不做）
- **codex / opencode** 的 scope 强制——各自走 app-server approvalPolicy / `--pure`，机制不同，单独立项。本次只动 claude。
- **OS 内核级沙箱**（macOS seatbelt / Linux firejail）——超出范围，见 §6 限制。
- **违规计数 / 自动 block**——本次采用 deny + 模型自纠（用户已定），不引入计数器状态。
- **改服务端**——`allowed_scope` 已下发，纯 agent 侧消费。无 schema、无协议、无 DB 变更。
- **影响 vibecoding 会话**——只对 `goalIdentity` 非空的 goal run 生效。

---

## 3. 方案设计

### 3.1 强度定位

| 档位 | 拦截者 | 本次? |
|---|---|---|
| 软（提示词 only） | 无人 | 现状 |
| **agent 级（hook 校验）** | **agent 进程逐次工具调用拦截** | **本次** |
| OS 内核级 | seatbelt/firejail | 不做 |

本次是 **agent 级硬限制**：AI 真的执行不了越界动作。但非 OS 级刀枪不入——见 §6。

### 3.2 数据通路（无新存储）

照 Gap C 的 `goalRequiredCheckCount` 模式（已验证的单点 plumbing）：

```
① 真相在服务端：goal_tasks 表的 allowed_roots / allowed_commands（计划提交时写死）
② 下发：buildGoalContext 塞进 goal_context 信封 → WebSocket → 设备 agent（已发生）
③ 本次新增：agent 从 msg["goal_context"] 解析出 allowedRoots/allowedCommands
   → 存 agentAIRun（两个 []string 字段，run 级、内存、不落库）
   → newAgentAIRunEmitter 镜像到 emitter
   → approval hook 读取
```

唯一注入点：`messageRun := agentAIRun{...}`（`agent_ai.go:1122`），因为 `runStart` 的 `m.message(msg, writeJSON)`（`:1360`）最终复用 `message()`，故 goal_context 只在 `message():1107` 单点消费、emitter 只在 `:1131`（`m.runEmitter`）单点建——与 Gap C 完全同构。

**存储结论**：规则在服务端 DB（早就在）；运行时副本在 agent 的 run 内存（新增字段，不新建表、不落库，run 结束释放）。

### 3.3 校验逻辑

抽**纯函数**，便于 TDD。注意 hook 处 `toolInput` 是 `marshalAgentAIRaw(...)` 返回的 **`json.RawMessage`**（`agent_ai.go:2668`，与 `evaluateApprovalDecision` 同型），**不是 `map[string]interface{}`**——故纯函数收 hook 的顶层 `raw map[string]interface{}`，内部自行 `marshalAgentAIRaw`+`json.Unmarshal` 取 `file_path`，与现有代码同型：

```go
// goalScopeAllowsToolCall 判断一次工具调用是否落在 goal task 的 allowed_scope 内。
// raw = handleClaudeApprovalHook 的顶层 payload（含 tool_input / toolInput）。
// 返回 (allowed, reason)。allowedRoots/allowedCommands 均空 = 不强制（兼容零检查纯审计 task）。
func goalScopeAllowsToolCall(
    toolName string,
    raw map[string]interface{},
    projectPath string,            // claude cwd，用于解析相对路径
    allowedRoots []string,
    allowedCommands []string,
) (allowed bool, reason string)
```

**写路径类工具**（`Edit`/`Write`/`MultiEdit`/`NotebookEdit`——`tool_input` 顶层均有单数 `file_path`，MultiEdit 的 `edits[]` 不带各自路径，已核验 `agent_ai.go:5525`）：
- 从 `tool_input` 抽 `file_path`（`firstNonEmpty("file_path","path")`）。
- **路径规范化**（照搬 `agent_approval_policy.go:543/570/577` 的防御范式）：
  1. `filepath.Abs(file_path)`——相对路径按 `projectPath`（claude 的 `cmd.Dir`）解析成绝对（claude 通常发绝对路径但不保证）。
  2. `filepath.Clean`。
  3. `filepath.EvalSymlinks`——**`if err == nil` 才用其结果，err 时回退到 Clean 后的绝对路径**（新建文件 Write 时目标尚不存在，`EvalSymlinks` 必失败；回退是必须的，否则每次新建文件都被误挡）。
- `pathUnderAnyScanDir(resolved, allowedRoots)`（复用 `agent_scan_dirs.go:14`）；不在任一 root 下 → `false`，reason `"path outside allowed roots: <path>"`。

**Bash 工具**：
- `claudeApprovalCommand(raw)`（`agent_ai.go:2759`，现成，收顶层 `raw`）抽命令字符串。
- **复用 `splitShellCommandSegments`**（`agent_approval_policy.go:642`，带引号/转义意识的 `&&`/`||`/`;`/`|` 分段器——不要另写 splitter）。
- **每段** `shlex.Split` 取首 token，必须**前缀命中** `allowedCommands`（`git`/`npm`/`npx`/…，与服务端 `planService` 的 allowed_commands 表一致）。
- 任一段不中 → `false`，reason `"command not in allowed set: <段>"`。

**只读/其它工具**（`Read`/`Glob`/`Grep`/`LS`/…）：放行（scope 主要约束写 + 命令）。

**空 scope**（roots 与 commands 均空）：一律放行——兼容"零检查纯审计 task"无路径边界的合法情况；不强制不误伤。

### 3.4 hook 集成（`handleClaudeApprovalHook` `agent_ai.go:2618`，goal-run 分支 `:2677`）

现行 goal 分支：
```go
if len(run.goalIdentity) > 0 {
    if decisionAutoDeny { return deny }
    return autoApprove  // ← 围栏真空区
}
```

改为（伪码）：
```go
if len(run.goalIdentity) > 0 {
    // 新增：先过 allowed_scope 围栏
    if ok, reason := goalScopeAllowsToolCall(toolName, toolInputMap,
        run.goalAllowedRoots, run.goalAllowedCommands); !ok {
        return deny("blocked: " + reason)  // claude 收到拒绝、自纠
    }
    // 既有：危险命令名单仍叠加在 scope 之上
    if decisionAutoDeny { return deny }
    return autoApprove
}
```

deny 原因随 hook 回执回给 claude（PreToolUse 原生语义），模型据此改用围栏内方式重试。

### 3.5 命令匹配决策（用户已定）

**shell 拆分 + 逐段前缀校验**：允许 `npm test && npm run lint`，拒绝 `npm test; rm -rf .`。

### 3.6 违规处理（用户已定）

**deny + 模型自纠**。无计数器、无状态。dangerous-command（`rm -rf` 等）仍走 `decisionAutoDeny`，叠加在 scope 之上（双重保险）。

---

## 4. 测试策略（TDD）

### 4.1 纯函数 `goalScopeAllowsToolCall`（单测，覆盖矩阵）
- 写路径：file_path 在 root 内/外、`../` 逃逸、symlink 指向 root 外、symlink 在 root 内、**相对路径**（按 projectPath 解析后比对绝对 root）、**新建文件 Write 目标尚不存在**（`EvalSymlinks` 失败须回退到 Clean-abs，不得误挡——这是最常见的 Write 场景）。
- Bash：单命令命中/不中、复合全合法（`npm test && npm run lint`）、复合含越界段（`npm test; rm -rf x`）、管道（`npm test | tee log`——两侧皆合法命令则放行）、空命令、shlex 不可解析。
- 空 scope：roots/commands 均空 → 一律放行（不强制）。
- 只读工具：Read/Grep 放行。

### 4.2 hook 集成测
- goal run + 越界写路径 → hook 返回 deny + reason。
- goal run + 越界命令 → deny。
- goal run + 围栏内调用 → 放行（不误伤）。
- **vibecoding（`goalIdentity` 空）→ 完全不触发围栏**（回归保护：不影响现有 vibe 会话）。
- 围栏 deny 与 `decisionAutoDeny` 叠加：危险命令即便在 allowedCommands 表内仍被危险名单挡（或文档标注优先级）。

### 4.3 既有回归
- 全 `Goal` 测试套件 + `approval policy` 相关测试零回归。
- `agent_approval_policy_test.go` 现有断言不变。

---

## 5. 数据存储说明（明确）

| 数据 | 位置 | 变更 |
|---|---|---|
| allowed_roots / allowed_commands 真相 | 服务端 `goal_tasks` 表 | **无变更**（早就存在） |
| 下发通道 | `goal_context` 信封（context.ts） | **无变更**（早就下发） |
| 运行时副本 | `agentAIRun.goalAllowedRoots/Commands`（Go 内存） | **新增字段**，run 级、不落库 |

不新建表、不改 schema、不改协议、不改服务端。

---

## 6. 已知限制（写进文档 + 代码注释）

本方案是 **agent 级**强制，**非 OS 内核级**。以下绕过面**不防**，需在代码注释与 admin 文档明示：

1. **配置注入**：`git -c alias.x='rm -rf' x`、`npm config set ...` 改配置后间接执行越界。
2. **环境变量注入**：`FOO=$(rm -rf x) npm test` 类。
3. **解释器逃逸**：`python -c "import os; os.system('...')"`、`node -e "..."`——`python`/`node` 在 allowedCommands 表内即放行，解释器体内任意代码不校验。
4. **TOCTOU**：路径校验后、写入前，符号链接换指向——不防（可接受）。
5. **shell 解析局限**：shlex 对极端引号/转义可能不完整。

防住以上需 OS 级沙箱（seatbelt/firejail/seccomp），非本 spec 范围。**本 spec 目标：阻止模型在 goal task 里漂移出已批准命令集/路径集**（正常 drift 场景），这是对"零强制"的结构性改善；不承诺防对抗性越狱。

---

## 7. 验收标准

- [ ] `goalScopeAllowsToolCall` 纯函数单测全绿（§4.1 矩阵）。
- [ ] hook 集成测：越界 deny、围栏内放行、vibecoding 不触发（§4.2）。
- [ ] 全 Goal + approval 测试套件零回归。
- [ ] `go vet` / `go build ./...` 干净。
- [ ] 代码注释明示 §6 限制（避免下一位维护者误以为 OS 级安全）。
- [ ] 真 goal task（claude 路径）smoke：模型越界改文件/跑命令被挡、围栏内正常完成。（部署后验）

---

## 8. 风险 / 回滚

- **风险**：围栏误伤合法调用（如合法管道被拆错判）。缓解：§4.1 矩阵覆盖复合/管道；空 scope 一律放行兜底。
- **风险**：模型被频繁 deny 后烧预算空转。缓解：用户已选纯 deny（信模型自纠）；若实测空转严重，后续可加 §3.6 的"N 次后 block"作为独立增强。
- **回滚**：纯 agent 改动，revert 单 commit 即恢复"零强制"现状；无数据迁移、无协议影响。
- **部署**：重编 Go agent 二进制 + 重启；服务端、phone 零改。

---

## 9. 后续（独立 spec，不在本批）

- codex / opencode 的 allowed_scope 强制。
- OS 级沙箱（若 drift 防线不够、需防对抗性越狱）。
- `goal_context_too_large` 无降级、`native` driver 死分支、model/effort 创建继承——goal 复查中的其它中优先项。
