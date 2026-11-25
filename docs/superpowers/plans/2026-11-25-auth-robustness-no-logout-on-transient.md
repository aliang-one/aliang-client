# 认证鲁棒性增强 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让服务端网络抖动/5xx/超时/传输错误不再触发本地登出；只有服务端明确的身份失效（明确拒绝短语的 401 或 refresh 端点 403）才落 HardInvalid/登出。

**Architecture:** 收严 `processor/auth` 两个 classifier 的兜底（不可解析/空/未知 JSON 401 返回 nil 进 SoftExpired 重试）；`RefreshSession` 在 refresh/profile 不确定 401 时返回非终态 `ErrSessionRecovering` 包装错误而非 wipe；前端 `isAuthenticationFailure` 不再把裸 401/403 当失效。SoftExpired 恢复链与 agent 401 恢复链保持不动。

**Tech Stack:** Go (processor/auth, app/http/services), Vue3 (app/website), vitest, go test。

**Spec:** `docs/superpowers/specs/2026-11-25-auth-robustness-no-logout-on-transient.md`

---

## File Structure

- Modify: `processor/auth/errors.go` — classifier 收严 + `refreshRejectionPhrases` 追加短语
- Test: `processor/auth/errors_test.go` — 更新/新增 classifier 用例
- Modify: `processor/auth/token_activate.go` — RefreshSession refresh/profile 不确定 401 返回非终态错误
- Test: `processor/auth/token_activate_test.go` — 新增 profile 不确定 401 不 wipe 用例
- Modify: `app/website/src/services/authFailure.js` — 裸 401/403 不再失效
- Test: `app/website/src/services/authFailure.test.js` — 更新用例

---

### Task 1: 后端 classifier 收严（processor/auth/errors.go）

**Files:**
- Modify: `processor/auth/errors.go`
- Test: `processor/auth/errors_test.go`

- [ ] **Step 1: 写失败测试（access classifier 未知/不可解析 401 期望 nil）**

在 `processor/auth/errors_test.go` 把以下三个用例的期望从 `ErrSessionExpired` 改为 `nil`：
- `TestClassifyAccessTokenFailure_UnknownJSON401`
- `TestClassifyAccessTokenFailure_InvalidJSON401`
- `TestClassifyAccessTokenFailure_EmptyBody401`

并新增 access 403 用例（`TestClassifyAccessTokenFailure_Non401` 中追加）：403 + TOKEN_REVOKED body → 期望 nil（access classifier 不处理 403）。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -run 'TestClassifyAccessTokenFailure' -v`
Expected: FAIL（未知/不可解析/空 401 仍返回 ErrSessionExpired）

- [ ] **Step 3: 实现 access classifier 收严**

在 `processor/auth/errors.go` 的 `classifyAccessTokenFailure`：
- 删除末尾兜底 `return ErrSessionExpired`（约 112 行）。
- 改为 `return nil`（未知 JSON/不可解析/空 body 的 401 视为瞬时）。
- 403 不处理（保持 `statusCode != http.StatusUnauthorized` 即 return nil）。

```go
	// 兜底 — 不明确的 401（未知 JSON/不可解析/空 body）视为瞬时，返回 nil 让上层
	// 进入 SoftExpired 重试而非清会话。只有上面明确拒绝语义才返回 ErrSessionExpired。
	return nil
```

- [ ] **Step 4: 运行 access classifier 测试确认通过**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -run 'TestClassifyAccessTokenFailure' -v`
Expected: PASS

- [ ] **Step 5: 写失败测试（refresh classifier 未知/不可解析 401 期望 nil + local session 短语仍 terminal）**

在 `processor/auth/errors_test.go` 的表驱动用例 `TestClassifyRefreshSessionFailure_KnownPatterns` 中，把子用例「unknown 401 is terminal」「invalid JSON 401 is terminal」的 `wantErr` 改为 `nil`；「error field: local session is no longer valid」保持 `ErrRefreshTokenInvalid`（短语加入后 terminal）。（这些是表内子用例，非独立函数。）

- [ ] **Step 6: 运行测试确认失败**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -run 'TestClassifyRefreshSessionFailure' -v`
Expected: FAIL（未知/不可解析 401 仍 terminal；local session 短语仅靠 fallback 命中）

- [ ] **Step 7: 实现 refresh classifier 收严 + 短语补全**

在 `processor/auth/errors.go`：
- `refreshRejectionPhrases` 追加 `"local session is no longer valid"`。
- `classifyRefreshSessionFailure` 删除末尾兜底 `return ErrRefreshTokenInvalid`（约 82 行），改为 `return nil`；403 显式返回 `ErrRefreshTokenInvalid`（**注意**：refresh 端点 403 = 明确拒绝 terminal，access 端点 403 不处理返回 nil——两者刻意不同，勿「对齐」）。**同时删除/改写 75-81 行旧注释块**（「A refresh endpoint has already authenticated... Its 401 is therefore a definitive rejection, including unknown JSON...」），避免与新 nil 兜底行为矛盾。

```go
func classifyRefreshSessionFailure(statusCode int, body []byte) error {
	if statusCode == http.StatusForbidden {
		return ErrRefreshTokenInvalid
	}
	if statusCode != http.StatusUnauthorized {
		return nil
	}
	// ... 解析 envelope，reason/code==REFRESH_TOKEN_INVALID 或 message/error 命中短语 → ErrRefreshTokenInvalid
	// 兜底 — 不明确的 401（未知 JSON/不可解析/空 body）视为瞬时，返回 nil 进入 SoftExpired 重试。
	return nil
}
```

- [ ] **Step 8: 运行 refresh classifier 测试确认通过**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -run 'TestClassifyRefreshSessionFailure' -v`
Expected: PASS

- [ ] **Step 9: 全量 auth 包测试**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -count=1`
Expected: PASS（若有其它用例因兜底行为变化失败，按 spec 更新期望）

- [ ] **Step 10: Commit**

```bash
git add processor/auth/errors.go processor/auth/errors_test.go
git commit -m "修复:auth classifier 收严——未知/不可解析 401 不再 HardInvalid"
```

---

### Task 2: RefreshSession 不确定 401 返回非终态错误（processor/auth/token_activate.go）

**Files:**
- Modify: `processor/auth/token_activate.go`
- Test: `processor/auth/token_activate_test.go`

- [ ] **Step 1: 写失败测试（profile 401 不确定不 wipe）**

在 `processor/auth/token_activate_test.go` 新增 `TestRefreshSession_ProfileUnclear401DoesNotWipe`：refresh 端点返回成功新 token，profile 端点返回 401 + 未知 JSON（`{"code":"SOME_UNKNOWN","message":"weird"}`）。期望 `RefreshSession` 返回非 nil 错误但 `errors.Is(err, ErrSessionExpired)`/`ErrRefreshTokenInvalid` 均为 false，且 `HasPersistedUserInfo()` 仍为 true（会话未清）。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -run 'TestRefreshSession_ProfileUnclear401DoesNotWipe' -v`
Expected: FAIL（当前 profile 401 无条件 `clearLocalSessionAfterExpiration` 清会话）

- [ ] **Step 3: 实现 profile 不确定 401 非终态**

在 `processor/auth/token_activate.go` 的 `RefreshSession` profile 401 分支（约 410-414 行）：
- 仅当 `errors.Is(classifyAccessTokenFailure(...), ErrSessionExpired)` 时 `clearLocalSessionAfterExpiration(...)` + 返回 `ErrSessionExpired` 包装。
- 否则返回 `fmt.Errorf("profile sync after refresh inconclusive: %w", ErrSessionRecovering)`（非终态），不清会话。

```go
	if profileErr != nil {
		if !authority.OperationCurrent(operation) {
			return nil, ErrStaleSessionOperation
		}
		var profileAPIError *authenticatedAPIError
		if errors.As(profileErr, &profileAPIError) && profileAPIError.StatusCode == http.StatusUnauthorized {
			if errors.Is(classifyAccessTokenFailure(profileAPIError.StatusCode, profileAPIError.Body), ErrSessionExpired) {
				clearLocalSessionAfterExpiration("refreshed access token rejected by profile endpoint")
				return nil, fmt.Errorf("refreshed session rejected: %w", ErrSessionExpired)
			}
			logger.Warn(fmt.Sprintf("Profile sync after refresh inconclusive (transient/unclear 401), keeping session for retry: %v", profileErr))
			return nil, fmt.Errorf("profile sync after refresh inconclusive: %w", ErrSessionRecovering)
		}
		logger.Warn(fmt.Sprintf("Profile sync after refresh failed, keeping last known user profile: %v", profileErr))
	}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -run 'TestRefreshSession_ProfileUnclear401DoesNotWipe' -v`
Expected: PASS

- [ ] **Step 5: refresh endpoint 不确定 401 非终态（核对现有行为）**

确认 `RefreshSession` refresh 端点 401 分支（约 368-379 行）：`classifyRefreshSessionFailure` 返回 nil 时已走 `return nil, fmt.Errorf("api returned status %d: %s", ...)`（非终态），无需改动。若返回非 nil 才 wipe（已如此）。仅加注释说明非终态语义。

- [ ] **Step 6: 全量 auth 包测试**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ -count=1`
Expected: PASS（`TestRestoreSession_ProfileRejectsRefreshedTokenDoesNotResurrectSession` 仍绿：该用例 profile 返回明确 TOKEN_EXPIRED，仍 HardInvalid）

- [ ] **Step 7: Commit**

```bash
git add processor/auth/token_activate.go processor/auth/token_activate_test.go
git commit -m "修复:refresh profile 不确定 401 返回非终态错误而非清会话"
```

---

### Task 3: 前端 authFailure 裸 401/403 不再失效

**Files:**
- Modify: `app/website/src/services/authFailure.js`
- Test: `app/website/src/services/authFailure.test.js`

- [ ] **Step 1: 写失败测试**

在 `app/website/src/services/authFailure.test.js`：现有首个用例 envelope `{ code: 1001, msg: 'Token has expired' }` 命中 `'token expired'` 短语，保持 true 不变。新增两个用例：裸 401（`isAuthenticationFailure(401, {})`）→ false；403（`isAuthenticationFailure(403, { msg: 'forbidden' })`）→ false。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website && npx vitest run src/services/authFailure.test.js`
Expected: FAIL（裸 401 仍 true）

- [ ] **Step 3: 实现前端收严**

在 `app/website/src/services/authFailure.js` 的 `isAuthenticationFailure`：
- 移除 `if (Number(responseStatus) === 401) return true;`。
- 改为：仅当 `responseStatus === 401` 且 `envelopeLayers` 任一层命中明确拒绝语义（status unauthenticated / error session_expired|token_expired / code token_expired / message 含 local session is no longer valid / invalid authentication / token has been revoked / invalid access token / token expired / token revoked）→ true。
- 403 不触发（移除任何 403→true 逻辑；当前无，保持）。
- 其它 status → false。
- **实现偏差（评审确认）**：实际实现不设 `responseStatus !== 401 → false` 的门——否则 nested terminal envelope（200 + `data.status=unauthenticated`，既有测试钉死）会误杀。401 与其它 status 一律要求 envelope 层命中明确拒绝语义；403 无拒绝证据自然 false。
- `ACCESS_REJECTION_PHRASES` 须包含两条 Go 中间件真实文案（互不为子串，缺一不可）：`authenticated dashboard session is required`（dashboard_session.go:108 / remote_access.go:53）与 `authenticated session is required`（quick_setup_service.go ErrQuickSetupUnauthenticated）——移除裸 401 短路后这是真·会话过期触发登出的唯一信号路径，并有测试用例钉死该跨语言契约。

```js
const ACCESS_REJECTION_PHRASES = [
  'local session is no longer valid',
  'invalid authentication',
  'token has been revoked',
  'invalid access token',
  'token expired',
  'expired token',
  'token revoked',
  'session_expired',
  'token_expired',
];

export function isAuthenticationFailure(responseStatus, envelope) {
  if (Number(responseStatus) !== 401) {
    return false;
  }
  return envelopeLayers(envelope).some((layer) => {
    const status = normalized(layer.status);
    const error = normalized(layer.error);
    const code = normalized(layer.code);
    const message = normalized(layer.message || layer.msg);
    if (status === 'unauthenticated') return true;
    if (error === 'session_expired' || error === 'token_expired') return true;
    if (code === 'token_expired' || code === 'token_revoked') return true;
    return ACCESS_REJECTION_PHRASES.some((p) => message.includes(p));
  });
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website && npx vitest run src/services/authFailure.test.js`
Expected: PASS

- [ ] **Step 5: 相关前端测试**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website && npx vitest run src/services/quickSetupApi.test.js src/stores/auth.test.js`
Expected: PASS（quickSetupApi 的 dashboard-session 401 仍走 markDashboardSessionRequired 分支，不受影响）

- [ ] **Step 6: Commit**

```bash
git add app/website/src/services/authFailure.js app/website/src/services/authFailure.test.js
git commit -m "修复:前端裸 401/403 不再触发登出 reconcile"
```

---

### Task 4: 全量回归 + 验收

> agent 侧 `recoverOrExpireAfterRegisterAuthRejection` / `handleHandshakeUnauthorized` 已只对 typed 401 触发 `RecoverOrExpireLocalSession`，transport/5xx 走 `classifyRegisterFailure` 保留注册态（spec §3.6「保持」）。本计划不改 agent 侧代码，仅在验收时确认其行为不变。

- [ ] **Step 1: Go 全量 auth 相关测试**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./processor/auth/ ./app/http/services/ ./app/http/handlers/ -count=1`
Expected: PASS

- [ ] **Step 2: 前端全量测试**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website && npx vitest run src/services/authFailure.test.js src/stores/auth.test.js src/services/authApi.test.js src/services/quickSetupApi.test.js src/services/agentApi.upload.test.js`
Expected: PASS

- [ ] **Step 3: 手动验收清单（记录，不阻塞）**

- 断网/5xx：本地会话保持，日志见 SoftExpired 重试，无 sqlite 清空。
- 明确 401（invalid refresh token / token revoked）：HardInvalid + 清会话 + 登出。
- 前端 feature API 5xx/超时：UI 不翻未登录。

- [ ] **Step 4: 最终 commit（如有遗漏）**

```bash
git add -A
git commit -m "修复:认证鲁棒性增强——服务端异常不触发登出(全量回归)"
```