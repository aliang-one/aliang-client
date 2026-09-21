# Agent 认证链自愈与计时锚定 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 拔除"客户端计时器与服务端凭据真实过期时间脱节 + agent→owner 凭据拒绝通知断链"两个根因，使 agent 掉线可自愈、诊断不再被误导。

**Architecture:** 服务端（账号服务 arbiter）在 refresh 响应中暴露上游 sub2api access JWT 的真实剩余寿命；客户端优先采用该锚点驱动刷新计时；agent 进程被 PhoneServer 拒绝时经 loopback HTTP 通知 owner 走既有 SoftExpired 恢复链。规格见 `docs/superpowers/specs/2026-09-21-agent-auth-recovery-design.md`。

**Tech Stack:** Go（两仓均 go test）；服务端 `internal/httpapi`（net/http + SQLite/Postgres）；客户端 `processor/auth` + `app/http`。

**偏差台账（相对规格，实施前确认的事实，均已向用户说明）：**
1. 规格 §4.3"注入即刷新"缩减为一条回归测试（Task B1 Step 5），无独立代码任务——`RestoreSession` 本身就先刷新（`token_activate.go:207`）且成功路径自动获得锚点。
2. 规格 §4.1 的字段名调整为 `upstream_expires_in` / `upstream_expires_at`——规格字面上的 `expires_in` 与既有字段冲突（该字段已有"本地 st_ 会话滚动 TTL"语义，web 前端在消费），改名才符合规格自己的兼容矩阵（§6）。规格新增字段的意图（客户端锚定上游真实 exp）不变。
3. 规格 §4.1 所指"login/register 重写路径"具体为 `injectLocalSessionIntoAuthResponse`（`internal/httpapi/routes.go:3226`，经 `captureSub2APITokens` 调用），Task A1 一并覆盖。

**提交纪律：** 全部提交只在 feature 分支上做，中文标题（`新增：/修复：`），**一律不推送**、不合 master——由用户审阅后决定。

---

## Task 0: 双仓 worktree 准备

**Files:**
- Create: `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/fix-agent-auth-recovery/`（worktree）
- Create: `/Users/mac/MyProgram/AiProgram/aliang-official-website/backend` 同仓根 `.claude/worktrees/fix-auth-upstream-anchor/`（worktree）

- [ ] **Step 1: 客户端 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .claude/worktrees/fix-agent-auth-recovery -b fix/agent-auth-recovery-chain
mv docs/superpowers/specs/2026-09-21-agent-auth-recovery-design.md .claude/worktrees/fix-agent-auth-recovery/docs/superpowers/specs/
mv docs/superpowers/plans/2026-09-21-agent-auth-recovery.md .claude/worktrees/fix-agent-auth-recovery/docs/superpowers/plans/
```

注意：master 工作区里 9/9 遗留的两个未跟踪文档（traffic-mirror-batching）**不要动**。若 `docs/superpowers/{specs,plans}/` 目录在 worktree 中不存在则先 mkdir -p。

- [ ] **Step 2: 服务端 worktree**

```bash
cd /Users/mac/MyProgram/AiProgram/aliang-official-website
git worktree add .claude/worktrees/fix-auth-upstream-anchor -b fix-auth-upstream-anchor
```

（服务端仓分支命名沿用该仓现有风格；若其 master 有未提交改动导致失败，停下来问用户，不要 stash 别人的工作区。）

- [ ] **Step 3: 验证**

```bash
git -C /Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/fix-agent-auth-recovery status --short
git -C /Users/mac/MyProgram/AiProgram/aliang-official-website/.claude/worktrees/fix-auth-upstream-anchor status --short
```

Expected: 两边输出为空（干净工作区，specs/plans 已在客户端 worktree 内待提交）。

---

## Task A1（服务端）: arbiter refresh 响应暴露上游真实过期锚点

**Files:**
- Modify: `internal/httpapi/routes.go:1405-1432`（`writeLocalSessionRefresh`）与 `internal/httpapi/routes.go:3226`（`injectLocalSessionIntoAuthResponse`，及其 `:2525` 调用点）
- Test: `internal/httpapi/refresh_arbiter_test.go`、`internal/httpapi/auth_passthrough_test.go`

工作目录：`/Users/mac/MyProgram/AiProgram/aliang-official-website/.claude/worktrees/fix-auth-upstream-anchor/backend`（下称 `$SRV`）

- [ ] **Step 1: 读现有测试定样板**

读 `$SRV/internal/httpapi/refresh_arbiter_test.go` 的 `TestRefreshArbiterServesCachedFreshToken`（:237 起）与 `TestRefreshArbiterDedupesAndRotates`（:127 起），复用 `setupTestDB(t)` 与它们构造"已登录用户 + vault"的方式。

- [ ] **Step 2: 写失败测试（两条路径都要）**

在 `refresh_arbiter_test.go` 末尾新增（helper 名以 Step 1 实读为准）：

```go
func TestRefreshArbiterResponseCarriesUpstreamAnchor(t *testing.T) {
	// 沿用 TestRefreshArbiterServesCachedFreshToken 的搭建方式：
	// 1) setupTestDB(t) 2) 建用户 + st_ 本地会话 + 写入 als_sub2api_auth_tokens
	//    （access_token 用真实 JWT 形状、access_expires_at = now+7h30m，HasAccessExpires 路径）
	// 3) POST /api/v1/auth/refresh {refresh_token: st_...} 命中缓存路径
	// 4) 解析响应 JSON 断言：
	//    data.upstream_expires_in ≈ 7h30m（±60s）
	//    data.upstream_expires_at ≈ now+7h30m（±60s）
	//    data.expires_in 语义不变（本地会话滚动值，>0 即可，不锁数值）
	// 再造一条 access_expires_at 为 NULL 的 vault（HasAccessExpires=false），
	// access_token 用无法解码的串 → 断言 upstream_expires_in ≈ 50min±60s（defaultAccessTTL 回退）。
}
```

再在 `auth_passthrough_test.go` 新增登录路径测试（沿用该文件现有搭建方式）：

```go
func TestLoginResponseCarriesUpstreamAnchor(t *testing.T) {
	// 走真实 login 流（密码或既有测试用的捕获路径），vault 在 captureSub2APITokens
	// 中刚写入（access_expires_at 已知值）→ 断言 login 响应 JSON 的
	// data.upstream_expires_in/at 与刚写入的 vault AccessExpiresAt 一致（±60s），
	// 且 expires_in 保持既有本地会话 24h 语义不变。
}
```

- [ ] **Step 3: 跑测试确认失败**

```bash
cd "$SRV" && go test ./internal/httpapi/ -run 'TestRefreshArbiterResponseCarriesUpstreamAnchor|TestLoginResponseCarriesUpstreamAnchor' -v
```

Expected: FAIL（字段不存在）。

- [ ] **Step 4: 实现（两处）**

**4a. `writeLocalSessionRefresh`（routes.go:1405）**：在 `writeJSON` 前加载 vault 并计算锚点，`data` map 增加两个键（**不动现有四个键**）：

```go
	upstreamExpiresIn := int(defaultAccessTTL.Seconds())
	if vault, vErr := r.sub2api.LoadVault(ctx, userID); vErr == nil && vault != nil {
		if vault.HasAccessExpires {
			upstreamExpiresIn = int(time.Until(vault.AccessExpiresAt).Seconds())
		} else if vault.AccessToken != "" {
			if exp := accessExpiryOrDefault(vault.AccessToken); exp != nil {
				upstreamExpiresIn = int(time.Until(*exp).Seconds())
			}
		}
	}
	if upstreamExpiresIn < 1 {
		upstreamExpiresIn = 1
	}
```

`data` 增加：`"upstream_expires_in": upstreamExpiresIn, "upstream_expires_at": time.Now().UTC().Add(time.Duration(upstreamExpiresIn) * time.Second).Unix()`。

**4b. `injectLocalSessionIntoAuthResponse`（routes.go:3226）**：锚点在 **`:2525` 调用点**计算（那里有 `routes` 接收器与 `localUserID`，vault 已由 `captureSub2APITokens` 写入，读到的是刚写入的准确值）——把 4a 的锚点计算抽成 `upstreamAnchorSeconds(ctx, userID) int`（两处复用），调用点算好后把两个键值并入注入函数生成的响应 JSON（给注入函数加参数，或在调用点合并 JSON map，取其一）。

- [ ] **Step 5: 跑测试确认通过 + 全包回归**

```bash
cd "$SRV" && go test ./internal/httpapi/ -run 'TestRefreshArbiterResponseCarriesUpstreamAnchor|TestLoginResponseCarriesUpstreamAnchor' -v && go test ./internal/httpapi/
```

Expected: 新测试 PASS，包内全部 PASS。

- [ ] **Step 6: 提交**

```bash
cd "$SRV" && git add internal/httpapi/routes.go internal/httpapi/refresh_arbiter_test.go internal/httpapi/auth_passthrough_test.go
git commit -m "新增：refresh/login 响应暴露上游 access 真实过期锚点（upstream_expires_in/at）"
```

（提交前 `git log --oneline -3` 看该仓风格。）

---

## Task B1（客户端）: 计时锚定采用服务端真值

**Files:**
- Modify: `processor/auth/token_activate.go:496-503`（`authTokenEnvelope`）与 `:387`（`mergeRefreshedSessionWithCurrentUser` 调用点）
- Test: 新建 `processor/auth/token_activate_anchor_test.go`

工作目录：`/Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/fix-agent-auth-recovery`（下称 `$CLI`）

- [ ] **Step 1: 写失败测试（纯函数，不碰 HTTP/config）**

```go
package auth

import "testing"

func TestEffectiveRefreshExpiresInPrefersUpstreamAnchor(t *testing.T) {
	if got := effectiveRefreshExpiresIn(86400, 27000); got != 27000 {
		t.Fatalf("upstream anchor should win: got %d", got)
	}
	if got := effectiveRefreshExpiresIn(86400, 0); got != 86400 {
		t.Fatalf("missing anchor must fall back to server expires_in: got %d", got)
	}
	if got := effectiveRefreshExpiresIn(86400, -5); got != 86400 {
		t.Fatalf("invalid anchor must fall back: got %d", got)
	}
	if got := effectiveRefreshExpiresIn(0, 0); got != 0 {
		t.Fatalf("both absent stays zero (caller falls back to constant): got %d", got)
	}
}
```

> 注意消费点覆盖：`authTokenEnvelope` 在客户端有三个消费场景——密码登录（`LoginWithPassword` → `finalizeAuthenticatedSessionWithOperation(..., response.Data.ExpiresIn)`，token_activate.go:135 附近）、刷新（:387）、扫码激活（`ActivateWithTokens` 收常量 `scanAccessTokenTTLSeconds`，其上游响应若也走 envelope 解析需同样接线）。Step 3 完成后 `grep -n 'authTokenEnvelope\|response.Data.ExpiresIn' processor/auth/*.go` 逐一核对消费点全部改走 `effectiveRefreshExpiresIn`。

- [ ] **Step 2: 跑测试确认失败**

```bash
cd "$CLI" && go test ./processor/auth/ -run TestEffectiveRefreshExpiresInPrefersUpstreamAnchor -v
```

Expected: FAIL（`effectiveRefreshExpiresIn` undefined）。

- [ ] **Step 3: 实现**

1. `authTokenEnvelope.Data`（:496）增加字段 `UpstreamExpiresIn int \`json:"upstream_expires_in"\``（`upstream_expires_at` 不参与判定，不加，保持单一真相源）。
2. `RefreshSession` 内 `:387` 的调用改为：

```go
	userInfo := mergeRefreshedSessionWithCurrentUser(current, response.Data.AccessToken, nextRefreshToken, response.Data.TokenType,
		effectiveRefreshExpiresIn(response.Data.ExpiresIn, response.Data.UpstreamExpiresIn))
```

3. 密码登录收尾（`LoginWithPassword` 调 `finalizeAuthenticatedSessionWithOperation(..., response.Data.ExpiresIn)` 处，:135 附近）同样改为 `effectiveRefreshExpiresIn(response.Data.ExpiresIn, response.Data.UpstreamExpiresIn)`；扫码路径按消费点核对结论处理（若其响应经 envelope 解析则同改；`ActivateWithTokens` 的常量参数保留，因其本就无响应可解析）。
4. 新增纯函数（放 envelope 定义旁）：

3. 新增纯函数（放 envelope 定义旁）：

```go
// effectiveRefreshExpiresIn 返回刷新计时应采用的剩余秒数。账号服务在 data 里
// 额外给出 upstream_expires_in（上游 sub2api access JWT 的真实剩余寿命）时优先采用——
// expires_in 只是本地 st_ 会话的滚动 TTL，与上游凭据真实过期时间脱节（2026-09-20 事故根因）。
func effectiveRefreshExpiresIn(expiresIn, upstreamExpiresIn int) int {
	if upstreamExpiresIn > 0 {
		return upstreamExpiresIn
	}
	return expiresIn
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd "$CLI" && go test ./processor/auth/ -run TestEffectiveRefreshExpiresIn -v && go test ./processor/auth/
```

Expected: PASS，包内全绿。

- [ ] **Step 5: 回归断言（RestoreSession 锚点贯通）**

确认 `RestoreSession` 成功路径（`token_activate.go:218-220` 返回 `RefreshSession` 结果）无需改动即继承锚点——在测试文件加一条注释性断言即可，不新增代码：

```go
// RestoreSession 成功路径直接返回 RefreshSession 的结果（token_activate.go:218-220），
// 故恢复/注入会话自动获得上游锚点；这是对 2026-09-20 事故（13:41 恢复后被错误计时）的回归防线。
```

- [ ] **Step 6: 提交**

```bash
cd "$CLI" && git add processor/auth/token_activate.go processor/auth/token_activate_anchor_test.go docs/superpowers/specs docs/superpowers/plans
git commit -m "新增：刷新计时锚定上游真实过期时间——修复旧凭据恢复后静默过期（2026-09-20 事故根因）"
```

---

## Task B2（客户端 owner 端）: 凭据拒绝通知端点

**Files:**
- Modify: `app/http/handlers/auth_handler.go`（新增 handler 方法）
- Modify: `app/http/routes/routes.go:138` 附近（注册路由）
- Modify: `app/http/middleware/startup_status.go:50-57`（启动期放行名单）
- Test: `app/http/handlers/auth_handler_test.go` 追加

- [ ] **Step 1: 写失败测试**

在 `auth_handler_test.go` 追加（构造方式沿用该文件现有测试）：

```go
func TestHandleAgentAuthRejectedGenerationRules(t *testing.T) {
	// 用例矩阵（直接构造 handler 或经 httptest 走路由，沿用现有测试基建）：
	// 1) body generation>0 且已失效（构造 generation 变更后的快照）→ 200 {"applied":false,"ignored":"stale_generation"}，
	//    且不调用 RecoverOrExpireLocalSession（用包内已有的可替换变量/或提为函数变量以便断言）
	// 2) body generation==0、observed_at=now、本地快照 Active → applied:true，Recover 被调用
	// 3) body generation==0、observed_at=6 分钟前 → applied:false, ignored:"stale_notification"
	// 4) 非法 body → 400
}
```

注：若 `RecoverOrExpireLocalSession`（`processor/auth/errors.go:139`）无法在测试中替换，先把它提为包级函数变量 `recoverOrExpireLocalSession = auth.RecoverOrExpireLocalSession`（生产代码引用处同步），这属于既有测试基建惯例的延伸。

- [ ] **Step 2: 跑测试确认失败**

```bash
cd "$CLI" && go test ./app/http/handlers/ -run TestHandleAgentAuthRejected -v
```

Expected: FAIL（方法不存在）。

- [ ] **Step 3: 实现 handler**

`auth_handler.go` 新增（规则与规格 §4.4 一致）：

```go
// HandleAgentAuthRejected 接收 agent 进程上报的"凭据被远端拒绝"通知，
// 触发 owner 侧 SoftExpired 恢复链（刷新成功→既有 handleAuthRefreshed 自动转发新会话；
// 刷新 401 才落 refresh_invalid 真终态）。幂等/防回退规则见设计文档 §4.4。
func (h *AuthHandler) HandleAgentAuthRejected(w http.ResponseWriter, r *http.Request) { ... }
```

请求体 `{"reason":"...","device_id":"...","observed_at":<unix秒>,"generation":<int64>}`；判定顺序：非法 body→400；generation>0 且 `!auth.GetSessionAuthority().GenerationActive(generation)`→忽略(stale_generation)；generation==0 时要求快照 Active 且 `now-observed_at<=300s` 否则忽略(stale_notification)；通过→`recoverOrExpireLocalSession(reason)`。响应统一 `{"applied":bool,"ignored":"..."}`。

- [ ] **Step 4: 注册路由 + 启动放行**

`routes.go:138` 旁：`register("/api/auth/agent-auth-rejected", h.Auth.HandleAgentAuthRejected, http.MethodPost)`；`startup_status.go:50-57` 名单追加该路径。

- [ ] **Step 5: 跑测试确认通过**

```bash
cd "$CLI" && go test ./app/http/handlers/ -run TestHandleAgentAuthRejected -v && go test ./app/http/...
```

- [ ] **Step 6: 提交**

```bash
cd "$CLI" && git add app/http/handlers/auth_handler.go app/http/handlers/auth_handler_test.go app/http/routes/routes.go app/http/middleware/startup_status.go
git commit -m "新增：owner 端 agent-auth-rejected 通知端点——agent 凭据被远端拒绝可触发会话恢复链"
```

---

## Task B3（客户端 agent 端）: 拒绝沿通知 owner

**Files:**
- Modify: `app/http/services/agent_service.go:47` 旁（env 常量）、`:1610-1626`（register 401 分支）、`:1480` 附近成功路径（去重复位）
- Modify: `app/http/services/agent_remote_ws.go:121-130`（WS 握手 401 路径）
- Modify: `app/agentruntime/manager.go:352-364`（`userAgentEnv` 注入 owner 地址）
- Test: 新建 `app/http/services/agent_auth_reject_notify_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestNotifyOwnerAuthRejectedTransitionEdge(t *testing.T) {
	// httptest 起假 owner：断言 1) 首次调用发出 POST，body 含 reason/generation/observed_at；
	// 2) 连续第二次（无成功复位）不再发送；3) 模拟注册成功复位后再发能发出；
	// 4) owner 地址为空 → 不 panic、不发送（降级为现状）。
}
```

- [ ] **Step 2: 跑测试确认失败**（`go test ./app/http/services/ -run TestNotifyOwnerAuthRejected -v`，Expected: FAIL）

- [ ] **Step 3: 实现**

1. `agent_service.go:47` 旁加 `SessionOwnerAddrEnv = "ALIANG_SESSION_OWNER_ADDR"`。
2. 通知器（放 agent_service.go，状态用包内现有 mutex 惯例）：

```go
var (
	ownerNotifyMu       sync.Mutex
	ownerNotifyInFlight bool // 转换沿：ok→rejected 只发一次，注册成功后复位
)

// NotifyOwnerAuthRejected 把"凭据被远端拒绝"沿转换沿通知 session owner，
// owner 走 SoftExpired 恢复链；通道不可用时静默降级为现状（agent 自禁）。
func NotifyOwnerAuthRejected(reason string) { ... }
```

   body 带 `generation`（`auth.GetSessionAuthority().Snapshot().Generation`，只读不改状态）。
3. 接线两处：`recoverOrExpireAfterRegisterAuthRejection` 非 owner 分支（:1621-1623，在日志后调用）；`agent_remote_ws.go` 握手 401 终态禁用路径（:121-130）。注册成功路径（`registerAndSyncLockedWithUserContext` :1480 成功返回前）复位 `ownerNotifyInFlight=false`。
4. `manager.go` 的 `userAgentEnv`：`next = append(next, services.SessionOwnerAddrEnv+"="+ownerBaseURL())`；`ownerBaseURL()` 返回 `"http://" + config.DefaultManagementAddr`（`defaults.go:25`，127.0.0.1:56431）。部署级覆盖用新 env `ALIANG_MANAGEMENT_ADDR`（本计划新引入，仅部署方显式设置时生效，代码内不读其它来源）——注意这是新约定，注释里写明。

- [ ] **Step 4: 跑测试确认通过 + 包回归**

```bash
cd "$CLI" && go test ./app/http/services/ -run TestNotifyOwnerAuthRejected -v && go test ./app/...
```

- [ ] **Step 5: 提交**

```bash
cd "$CLI" && git add app/http/services/agent_service.go app/http/services/agent_remote_ws.go app/http/services/agent_auth_reject_notify_test.go app/agentruntime/manager.go
git commit -m "新增：agent 凭据被远端拒绝沿通知 session owner——打通掉线自愈恢复链"
```

---

## Task B4（卫生）: 测试日志隔离 + 注释漂移修正

**Files:**
- Modify: `app/http/services/testmain_test.go`、`app/http/routes/testmain_test.go`（存在则改，不存在则在该包新建）
- Modify: `app/http/handlers/agent_handler.go:148-152`（注释）

- [ ] **Step 1: TestMain 重定向**

各测试包 TestMain 在 `m.Run()` 前 `os.Setenv("ALIANG_LOG_DIR", t 临时目录)`（用 `os.MkdirTemp`，`defer os.RemoveAll`），并视需要同时隔离 `ALIANG_DATA_DIR`；以现有 `testmain_test.go:22` 的写法为准。

- [ ] **Step 2: 注释修正**

`agent_handler.go:148-152` 改为描述 B3 后的真实行为："agent 被远端拒绝后沿转换沿通知 owner 走恢复链；恢复失败（刷新 401）才落 refresh_invalid 终态"。

- [ ] **Step 3: 验证隔离生效**

```bash
stat -f '%m %N' ~/.aliang/logs/aliang_core.log   # 记录 mtime
cd "$CLI" && go test ./... 2>&1 | tail -5
stat -f '%m %N' ~/.aliang/logs/aliang_core.log   # mtime 不应变化
```

- [ ] **Step 4: 提交**

```bash
cd "$CLI" && git add -A app/http app/agentruntime && git commit -m "修复：go test 不再写生产日志/状态目录；修正会话过期注销行为的漂移注释"
```

---

## Task B5: 全量验证

- [ ] **Step 1:** `cd "$CLI" && go test ./...` 全绿；`go vet ./...` 无新告警
- [ ] **Step 2:** `cd "$SRV" && go test ./...` 全绿
- [ ] **Step 3:** `git -C "$CLI" log --oneline master..HEAD` 与服务端同查——确认提交序列干净、全中文、无越界文件
- [ ] **Step 4:** 向用户汇报分支与验证结果，**等待用户决定合并/推送**（master 受保护，禁止强推）

## 上线后人工验证（非本计划任务，备忘）

1. Mac 托盘重登 → 触发 refresh → 抓 refresh 响应确认 `upstream_expires_in` 与 PhoneServer 侧 exp 一致；
2. 观察一个完整"恢复→锚定→到期前 10min 刷新"周期（可临时把 TokenRefresher 日志级别调 debug）；
3. 服务端与客户端独立发版顺序均可（兼容矩阵见规格 §6）。
