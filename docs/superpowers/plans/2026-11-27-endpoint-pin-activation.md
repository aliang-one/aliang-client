# 托盘 ENDPOINT 单选激活（钉选增量）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development。步骤用 `- [ ]` 跟踪。
> **仓库铁律**：提交须用户同意（本计划经用户「都要，先A后B」拍板授权执行，按任务 Commit）。

**Goal:** 托盘面板 ENDPOINT 区的 radio 从「钉选能力即将上线」toast 升级为**真实组内互斥激活**：点击某行 = 该行 enabled=1、同协议组其他行 enabled=0（builtin 兜底行不参与互斥），调度池随之收缩到该行——cc-switch 式钉选。

**Architecture:** 服务层薄组合方法 `ActivateInGroup(protocol, targetID)`（含 builtin 保留与非池行拒绝语义）+ `POST /api/endpoints/activate` handler + summary items 增 `id` 字段（前端定位行需 id）+ 前端 radio 接真激活（乐观勾移/失败回滚/summary 回读确认）。

**语义裁定（写入实现注释与本计划）：**
- 一期 `selectAuto` 池准入只看 `AutoGroup`——`enabled=1` 但 `auto_group=0` 的行**不进池**，激活对调度无效。因此 **radio 只对 rank≥1（池内）行提供激活；rank=0 行（builtin 等）点击 → toast「builtin 为兜底行/该行不在 auto 池，激活无效」**。
- 互斥 = 同 protocol 组内 enabled 互斥；**builtin 行不参与互斥**（保持现状——builtin 是回落兜底，被 radio 关掉会让用户配置的 endpoint 全部失效）。
- 一期 enabled 切换无「激活迁移清零」（二期 §5.6 落地后自动获得，托盘零改动）。
- 冷却/熔断行激活：允许（用户显式选择压过健康状态——激活本身就会清零健康吗？一期不会；**v1 明确**：对 cooldown 行激活 = 置 enabled 同时健康状态不变，行仍显示冷却徽标直到懒恢复；disabled（用户摘除）行激活 = 置 enabled=true 语义上自相矛盾（用户摘除=enabled=0 的用户动作），**拒绝**（400 语义在前端 toast 层体现：disabled 行点击 toast「已摘除的行请先在仪表盘恢复」）。cooldown 行允许激活并 toast「已激活（冷却中，恢复于 N 分钟后）」。

**Tech Stack:** Go（endpoint_service + handler + tray_summary）+ Vue（EndpointZone + api.js）。无新依赖。

**规格来源：** 托盘样式 spec §4.4（交互，经钉选递延拍板修订）；endpoint 二期设计 spec `docs/superpowers/specs/2026-11-27-endpoint-phase2-design.md`（§2 激活术语/§5.6 迁移清零/§7.1 行上开关——v1 A 用一期 PUT 语义，二期合入后自动升级）。

**工作分支：** `feat/endpoint-pin-activation`（worktree `.claude/worktrees/endpoint-pin-activation`，基点 master=880f619f）。

---

### Task 1: summary items 增 `id` 字段

**Files:**
- Modify: `app/http/services/tray_summary.go`（items 结构体 + 映射加 `ID`，json `id`；数据源 ListEndpoints 行自带 ID）
- Test: `tray_summary_test.go` 追加断言（items 带 id、形状恰等断言同步）

- [x] 失败测试：items 断言加 `id` 字段（从假数据源行 ID 透传）
- [x] 红 → 实现（结构体字段 + 映射一行）→ 绿
- [x] `go test ./app/http/services/ -run 'TestTray|TestServeTray' -count=1` + `go build ./...`
- [x] Commit（`新增:tray/summary items 带 id——托盘激活定位行`）

### Task 2: 服务层 ActivateInGroup + POST /api/endpoints/activate

**Files:**
- Modify: `app/http/services/endpoint_service.go`（新方法 ActivateInGroup(protocol, targetID string) error）
- Modify: `app/http/handlers/endpoint_handler.go`（HandleEndpointActivate，照 Task 5 修复后的 handler 惯例：405/来源闸照既有 /api 模式）
- Modify: `app/http/routes/routes.go`（POST /api/endpoints/activate）
- Test: `app/http/services/endpoint_service_test.go` 追加、`app/http/handlers/endpoint_handler_test.go` 追加

语义（实现注释写明）：
- 目标行不存在/protocol 不匹配 → 404 语义（ErrNotFound 或既有错误惯例）
- 目标 `health_status='disabled'`（用户摘除）→ 拒绝（「已摘除的行请先在仪表盘恢复」）
- 目标 cooldown 行 → 允许（激活不清零冷却——v1 明确；行内徽标持续到懒恢复）
- **builtin 行 / `auto_group=0` 行 → 拒绝**（「builtin 为兜底/该行不在 auto 调度池,激活无效」）
- 执行：同 protocol 组内所有 `auto_group=1 ∧ 非builtin` 行 enabled=0；目标行 enabled=1；builtin 行 enabled 保持不变
- 落库走既有 Update/持久化路径（一期语义，无激活清零——二期合入自动升级）

- [x] 失败测试：正常互斥（目标 on 其他 off、builtin 不动）＋四条拒绝分支＋同 protocol 过滤
- [x] 红 → 实现 → 绿 → `go test ./app/http/services/ ./app/http/handlers/ -count=1`
- [x] Commit（`新增:endpoint 组内互斥激活——ActivateInGroup+POST activate`）

### Task 3: 前端 radio 接真激活

**Files:**
- Modify: `app/website/src/panel/api.js`（activateEndpoint(protocol, id)：POST /api/endpoints/activate，10s 超时档，三层判失败对齐）
- Modify: `src/panel/components/EndpointZone.vue`（onItem：rank≥1 且 state 非 disabled → 乐观勾移 + activateEndpoint + summary 回读确认失败回滚；cooldown 行激活 → toast「已激活（冷却中，约 N 分钟后恢复）」+勾移；rank=0/builtin 行 → toast「builtin 为兜底行/该行不在 auto 调度池，激活无效」；维持零请求契约的行继续零请求——本任务后 ok 行点击**会**发请求，相关测试全部改写）
- Test: `EndpointZone.test.js` 改写（radio 真激活：请求体断言/成功勾移/失败回滚/cooldown 激活/rank=0 与 builtin toast；「无 activate」守卫测试**删除**——本任务后 api.js 有 activate，守卫使命结束）

- [x] 改写测试红 → 实现 → 绿 → panel 全量 + build + check-dist
- [x] Commit（`新增:面板 radio 接真激活——组内互斥钉选(乐观勾移/失败回滚)`）

### Task 4: 收口

- [x] 全量门禁：`go test ./app/http/services/ ./app/http/handlers/ ./app/tray/ ./internal/ipc/ -count=1` + panel vitest 全量 + build + check-dist + Linux 闭包门
- [x] 计划/spec 回写：Task 6 递延注记更新为「v1 组内互斥激活已落地；二期合入后自动获得激活清零」；托盘样式 spec §4.4 radio 语义同步
- [x] M1 手工走查清单增补：radio 真激活走查项
- [x] Commit（`文档:钉选增量收口——spec/计划回写`）

## 明确不做

- endpoint 二期本体（egress_kernel/黏性/401 锁存/agentcore_llm 退役）——独立批次
- 激活迁移清零（二期 §5.6）
- builtin 参与互斥
- 面板内 endpoint 增删改查

## 风险

- 两次 PUT 非原子 → 服务层组合方法一次落库已消除
- radio 激活改变 auto 池（N 候选→1）——这正是产品语义（cc-switch 式）；manager UI 可随时恢复多候选
- 互斥会把同组其余 enabled 行（含 cooldown/health=disabled）翻成 enabled=0，后者失去懒恢复自愈通道（等效手动停用）——钉选持久性的必要代价，已在 `ActivateInGroup` 注释与测试中钉死

## 执行记录（2026-11-27 收口）

| 任务 | 提交 | 内容 |
|---|---|---|
| Task 1 | `814bc55b` | summary items 带 `id`（json `id`，从源行透传）+ 契约测试同步 |
| Task 2 | `5fa6697d` | `ActivateInGroup`（哨兵拒绝闸/互斥写序/冷却放行不清零）+ `POST /api/endpoints/activate` + 路由 |
| 评审增益（后端） | `e01562fb` | mux 级可达性冒烟（变异红：删注册行 405≠401）+ 摘除行连带关停语义点破/测试 + 死代码清理 + 快照窗口注释 |
| Task 3 | `7c24c904` | 面板 radio 真激活（api.activateEndpoint/乐观勾移/回读确认/单飞/deriveOnKey）+ 测试改写 |
| 评审增益（前端） | `a9cfba82` | 已选行空操作早退（防互斥连带关停）+ 四条变异红护栏（乐观勾移/引用比对/errorBodyMsg 默认关/builtin rank0 fixture）+ 单飞提示 + toast 组名（spec §6-6）+ 注释精确化 |
| Task 4 | 本文档提交 | 文档回写（M1 计划 Task 6/10/Step 5 + 样式 spec §4.4/§6-6）+ 全量门禁 |

**终门禁（2026-11-27）**：`go test ./app/http/services/`（241.99s，复跑绿；首次失败为预存 TempDir 清理 flake `TestSteerReviveViaMessageRestoresSession`，测试本体 PASS、单跑 3 连绿）、`./app/http/routes/ ./app/http/handlers/ ./app/tray/ ./internal/ipc/` 全 ok、`go build ./...` OK、`go vet` 四包干净、`gofmt -l` 改动文件干净、panel vitest 10 文件/192 用例全绿、`npm run build` OK、`check-dist` 双入口通过、Linux 闭包 `GOOS=linux go list -deps` 0 GUI 依赖。

## 挂账（非阻塞，走查/后续批次处理）

- **rank 池 ≠ selectAuto 池缝隙（产品待对齐）**：`tray_summary.go:388` 只给 `auto_group ∧ priority>0` 授 rank，而 `selectAuto` 池只看 `enabled ∧ auto_group`。`priority=0 ∧ auto_group=1` 的行是真实调度候选但 rank=0，面板本地闸会拒绝并给「不在 auto 调度池」文案（后端本会接受）。与 M1 计划打磨清单的 priority=0 缝隙同源，需产品对齐后统一 rank 语义。
- **cooldown 行视觉与 disabled 行同为 `.off` 置灰**：可点/不可点无法从外观区分（spec §4.4 已回写「可激活」，视觉打磨留手工走查定）。
- **组头 g-current 无淡入过渡**：瞬切，样式打磨项（spec §6-6 已注记）。
- **回读确认的轮询第二写者窗口**：2s 轮询与 `refresh` 并发时，先发后到的旧快照可能被当作激活后的 `after`，低概率误报「激活结果未确认」并回滚，下一轮轮询自愈。
- **humanizeActionError 与 TransportRow 本地重复**：面板组件自含纪律下的两份实现，规则一致、文案前缀不同；如需统一可后续抽 `panel/errors.js`。
- **在途单飞跨组提示**：另一协议组在途点击会提示「激活进行中，请稍候」而非静默丢弃。

## 任务勾选

- [x] Task 1: summary items 增 `id` 字段
- [x] Task 2: 服务层 ActivateInGroup + POST /api/endpoints/activate
- [x] Task 3: 前端 radio 接真激活
- [x] Task 4: 收口（全量门禁 + 文档回写）
