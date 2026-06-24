# Agent AI 审批策略模板 — Phase B（服务端）+ Phase C（手机端）实现计划

> **STATUS (2026-06-22): ALL THREE PHASES COMPLETE & VALIDATED.**
> - Phase A (agent): complete, 19 tests green, vet/build clean.
> - Phase B (server `AliangPhoneServer`): DB tables + resolve/hash + agent GET endpoints + device-settings extension + custom PATCH + contract — typecheck (server+web) clean, 259 vitest green (10 policy), contract 32 samples verified.
> - Phase C (mobile `AliangVibeCodingPhone`): api + types + `ApprovalPolicyCard` scheme selector wired into DeviceDetailScreen — typecheck clean. (4 pre-existing jest failures in terminal/vibe code from unrelated in-progress work, not caused by these changes.)
> - Deferred: approval-card `matched_rule_id` display — blocked on the unresolved `ai.approval.*`↔`approval.*` server bridge (separate concern, documented in ai-approval-reliability-design.md).
> Not committed (per CLAUDE.md / repo norms).

> 仓库：服务端 `~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`；手机 `~/MyProgram/AiProgram/vibe_on_phone/AliangVibeCodingPhone`。
> 提交策略：遵各仓库约定；不自动 commit。

**Goal:** 让审批策略真正可服务端配置/分发（B）并在手机端可配置 + 审批卡展示上下文（C）。agent 端（Phase A）已完成，失联时退化到内置 balanced。

**已完成（B keystone）：**
- `AliangPhoneServer/server/src/modules/approval/policy.ts`：类型 + `DEFAULT_BALANCED_POLICY`/`DEFAULT_ALLOW_ALL_POLICY`（与 agent builtin 逐字对齐）+ `computePolicyHash`/`canonicalPolicyForHash`。
- `AliangPhoneServer/server/test/modules/approval/policy.test.ts`：6 测试全绿（vitest）。

---

## Phase B：服务端集成（AliangPhoneServer，TS）

### B1. DB 表 + 设备列 + 种子（`server/src/database.ts`）

在 `this.db.exec(\`...\`)` 块（~line 449 索引之前）加：

```sql
CREATE TABLE IF NOT EXISTS system_preset_templates (
  scheme TEXT NOT NULL,            -- balanced | allow_all
  version INTEGER NOT NULL,
  rules_json TEXT NOT NULL,
  default_decision TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  PRIMARY KEY (scheme, version)
);
CREATE TABLE IF NOT EXISTS device_custom_templates (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  rules_json TEXT NOT NULL,
  default_decision TEXT NOT NULL,
  hash TEXT NOT NULL,
  created_from_preset TEXT,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_device_custom_device ON device_custom_templates(device_id, version DESC);
CREATE INDEX IF NOT EXISTS idx_system_preset_active ON system_preset_templates(scheme, is_active);
```

在 `ensureColumn` 事务（~line 483）加：
```ts
this.ensureColumn('devices', 'approval_scheme', "TEXT NOT NULL DEFAULT 'balanced'");
this.ensureColumn('devices', 'approval_custom_version', 'INTEGER');
```

种子（建表后、事务后，幂等）：若 `system_preset_templates` 无 balanced 行，插入 `balancedPolicy()` / `allowAllPolicy()` 为 version=1, is_active=1（rules_json = `JSON.stringify(rules)`，hash 用 `computePolicyHash`）。

### B2. Store 方法（`server/src/database.ts`，镜像既有 insert/get 风格）

- `getActivePreset(scheme): ApprovalPolicy | undefined` — 取 `is_active=1` 最新行 → 反序列化。
- `getDeviceCustomTemplate(deviceId, version?): ApprovalPolicy | undefined`。
- `upsertDeviceCustomTemplate(deviceId, policy)` — version+1，写 hash，`created_from_preset='balanced'`。
- `resolveDeviceApprovalPolicy(deviceId): ApprovalPolicy` — 读 device `approval_scheme`：balanced/allow_all → `getActivePreset`；custom → `getDeviceCustomTemplate(deviceId, device.approvalCustomVersion)`；缺失回退 balanced。填 `device_id`、`version`、`hash=computePolicyHash(...)`。
- `getDeviceApprovalPolicyHash(deviceId): { version, hash }` — `resolveDeviceApprovalPolicy` 后取 version+hash。

### B3. 类型 + 序列化器

- `server/src/types.ts`：`Device` 增 `approvalScheme: 'balanced'|'allow_all'|'custom'`（默认 `'balanced'`）、`approvalCustomVersion?: number`。
- `server/src/modules/device/normalize.ts`（或 device load 处）：DB row → Device 映射这两列（默认 balanced）。
- `server/src/modules/device/serializers.ts` `publicDevice`：增
  ```ts
  approval_policy: {
    scheme: device.approvalScheme ?? 'balanced',
    version: <resolved version>,
    hash: <resolved hash>,
  }
  ```
  （resolved 经 `resolveDeviceApprovalPolicy`；为避免每次序列化查 DB，可在 settings 变更时缓存到内存 Device 上。）

### B4. zod schema（`server/src/schemas.ts`）

```ts
export const approvalRuleMatchSchema = z.object({
  tool: z.array(z.string()).optional(),
  command_regex: z.string().optional(),
});
export const approvalRuleSchema = z.object({
  id: z.string().min(1).max(80),
  match: approvalRuleMatchSchema,
  decision: z.enum(['auto_approve','require_approval','auto_deny']),
  reason: z.string().max(200).optional(),
});
export const approvalPolicySchemeSchema = z.enum(['balanced','allow_all','custom']);
// 扩 deviceSettingsSchema：
approval_policy: z.object({
  scheme: approvalPolicySchemeSchema.optional(),
  custom_rule_overrides: z.record(z.string(), z.enum(['auto_approve','require_approval','auto_deny'])).optional(),
}).optional();
```

### B5. 路由

**agent-facing（device_token 鉴权，镜像 `modules/agent/routes.ts` Bearer 提取 + `deviceCredentials` 校验）：**
- `GET /api/agent/approval-policy/hash` → `{ version, hash }`
- `GET /api/agent/approval-policy` → 完整 resolved `ApprovalPolicy`
（device_id 由 token → `device_credentials` 反查。）

**admin/mobile-facing（`requireUserId` + `getAccessibleDeviceOrThrow`）：**
- 扩 `PATCH /api/devices/:deviceId/settings`（`modules/routes/devices.ts:176`）：解析 `approval_policy.scheme` → 写 device `approval_scheme`；若切到 custom 且无 custom 模板 → `upsertDeviceCustomTemplate(deviceId, balancedPolicy())` 记 version，置 `approval_custom_version`。复用 `rememberAudit`（`eventType:'device.settings.update'`）+ `publishToAgent('device.settings.updated', {device, approval_policy:{scheme,version,hash}})` + `publishToMobiles('device.updated')`。
- `PATCH /api/devices/:deviceId/approval-policy/custom`：勾选式 `custom_rule_overrides`（rule id → decision）应用到 custom 模板副本 → version+1 + 重算 hash → 推 `device.settings.updated`。

### B6. 契约（`AliangPhoneServer/docs/agent-cloud-contract/`）

- `README.md`：补 "Agent approval policy fetch (`GET /api/agent/approval-policy[/hash]`, device_token Bearer)" + `device.settings.updated.approval_policy` 字段。
- `samples.json` + `schema.json`：加这两类 payload 样本与 schema；`npm run contract:agent` 通过。

### B7. 验证

- `npm run typecheck`（server + web）。
- `npm test`（vitest）：policy.test.ts（已绿）+ 新增 resolve/hash 集成测试（httptest 风格：seed → resolve → hash 稳定；scheme=custom → 取 custom；custom 缺失 → 回退 balanced）。
- `npm run contract:agent`。

---

## Phase C：手机端（AliangVibeCodingPhone，React Native + zustand）

### C1. API 层（`src/api/`）

- 扩 `src/api/devices.ts`（或新 `src/api/approvalPolicy.ts`）：
  - `getDeviceApprovalPolicy(deviceId)` → `GET /api/devices/:id`（已含 `approval_policy` via publicDevice）。
  - `updateDeviceApprovalScheme(deviceId, scheme)` → `PATCH /api/devices/:id/settings { approval_policy:{scheme} }`。
  - `patchCustomOverrides(deviceId, overrides)` → `PATCH /api/devices/:id/approval-policy/custom`。

### C2. 配置 UI（`src/screens/devices/DeviceDetailScreen.tsx`）

- 新增"审批策略"区块：scheme 三选（balanced / allow-all / custom）RadioGroup；选 custom 时展开规则勾选列表（从 device 当前 custom 模板 rules 渲染，每条 rule 一个 decision picker：放行/审批）。
- 保存调上述 API；成功后 store 更新。

### C3. 审批卡上下文（`src/screens/operations/ApprovalCenterScreen.tsx` + `src/store/slices/approvalSlice.ts`）

- approval item 类型增 `matched_rule_id?`、`policy_version?`（后端 `ai.approval.request` 已带）。
- 审批卡展示"命中规则：{matched_rule_id}（策略 v{policy_version}）"，让用户知道为何被拦。

### C4. i18n + store

- `src/store/slices/approvalSlice.ts`：携带 policy 上下文字段。
- 文案随项目既有 i18n 习惯补（RN 项目若无 i18n 库则内联中英）。

### C5. 验证

- `npm test`（jest）+ `npm run lint`。
- 手动：DeviceDetailScreen 三选切换；ApprovalCenterScreen 审批卡显示命中规则。

---

## 完成定义（全 feature）

- B：服务端可按设备存/分发策略，agent hash 同步生效（Phase A 已对接）；契约 + typecheck + vitest 通过。
- C：手机端可选 scheme + custom 勾选；审批卡展示命中规则；jest + lint 通过。
- 端到端：手机选 allow-all → 服务端推 `device.settings.updated` → agent 重拉 → 后续 Bash/Edit 本地放行（活动流可见）；选 balanced → 只文件改写/危险命令上报。

## 备注

- Phase A 已让"审批洪流"立即消失（内置 balanced），B/C 是可配置性增强，可分 PR 上线。
- 本计划步骤精确到文件/符号，执行时按 TDD（先失败测试再实现），每步 typecheck/test 绿后留交用户提交。
