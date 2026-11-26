# LLM Endpoint 管理平台 升级与验收

- 日期：2026-11-26
- 对应设计：`docs/superpowers/specs/2026-11-25-llm-endpoint-manager-design.md`（§13 迁移与兼容、§17 不变量）
- 适用版本：引入 `llm_endpoints` / `endpoint_group_policy` 两表 + 56432 AI path 路由层的第一个发布版

## 1. 升级要点：旧 `customer.agentcore_llm` 不自动迁移

升级后旧字段**原样保留**（deep-merge 不删键），读优先级也保留——`EffectiveAgentCoreLLM` 显式配置仍最高优先（spec §17 不变量 9）。含义：

- 旧字段命中时，出网地址/凭据仍走旧链路，registry 里配了也不生效；
- registry 只在旧字段未配置时驱动出网；
- 内置 aliang 行首次启动自动 seed，开箱即用（等价升级前行为，无感知）。

## 2. 手动重建步骤（切到 registry 管理）

1. 仪表盘 → 设置 → 「LLM 端点管理」子页（`EndpointManagerSettings`）。
2. 对照旧配置（设置页 agentcore 面板里的 `customer.agentcore_llm`）逐条**新增**：
   - 填 name、base_url、api_key；勾协议 toggle（Anthropic = `/v1/messages`，OpenAI = `/v1/chat/completions`）；auto 分组开启；priority 越小越优先。
   - **先保存端点**（保存后行上才有 id），再在列表行上点「测试连接」/「拉取模型」——拉取模型是行级按钮，依赖已保存的行（id）；拉到的模型列表确认回填编辑表单后，再点保存落库。顺序：保存端点 → 行上「拉取模型」→ 确认回填 → 再保存。
3. 想让 registry 完全接管（旧字段彻底不参与），需在 agentcore 设置面板**清空旧字段**：把 `base_url` / `api_key` / `model` 等输入框清空后保存（`applyAgentCoreLLMClearing` 会把空串写入 patch.agentcore_llm，后端 deep-merge 后该字段不再命中）。**只清空想让 registry 接管的那些字段即可**（如只想让 registry 管 api_key，就只清 api_key）。
4. 清空后无需重启：内核三解析点（`agentCoreBaseURL` / `agentCoreAuthToken` / `agentCoreEnsureUpstream`）每次出网实时读 registry，56432 数据面同样实时读。

## 3. 已装机器兼容性

- 旧客户端配置文件里的真实 key **不动**：56432 路由层对客户端 Authorization 不透明接受（哨兵、旧 key、任意非空串都放行），无需重跑 quick-setup（spec §9/§17 不变量 10）。
- 新装机才写入哨兵 key `sk-aliang-local`。
- CONNECT 隧道与非 AI path 的明文代理行为零变化（不变量 1/2）。

## 4. 装机冒烟清单（人工步骤）

自动化等价验证见 go 单测（`endpoint_router_test.go` / `endpoint_service_test.go` / `endpoint_handler_test.go` / `agent_ai_agentcore_endpoint_test.go`）；装机后按顺序过一遍真实链路：

1. **内置链路不变**：不配任何 BYO endpoint，跑一条 agentcore 对话——应走 aliang 隧道成功（与升级前一致）。
2. **BYO 接入**：设置 → LLM 端点管理 → 新增（真实上游 base_url + key）→ 「测试连接」应 ok 且报模型数 → 「拉取模型」返回列表 → 确认保存。
3. **选中转发**：BYO priority 设小（优先于内置）→ 发一条对话 → 上游侧（或对话内容）确认走的是 BYO；列表页该行并发用量 inflight 出现过非 0。
4. **失败摘除**：把 BYO 的 base_url 改成不可达地址（或上游停掉）→ 组策略保持默认（失败摘除开，阈值 3）→ 连发 3 条对话 → 第 3 条后该行健康徽标变 disabled（红）+ 显示最近错误 → **第 3 条失败后**，后续对话自动回落内置链路（前 3 条本身会把上游错误回给客户端——摘除发生在第 3 次失败落库之后，摘除前的请求原样透传）。另外单次失败（未达阈值）触发的一次重试会切到下一候选 endpoint，不是同 endpoint 重打。
5. **冷却懒恢复**：等冷却到期（默认 300s）→ 再发一条对话 → 恢复走 BYO，徽标回 ok；或直接点「重置健康」立即恢复。
6. **池模式**：组策略关「失败摘除」→ BYO 保持不可达再发对话 → 徽标恒 ok、不摘除（对话透传上游错误）；开回「失败摘除」恢复 §4.4 行为。
7. **/v1/models 聚合**：`curl http://127.0.0.1:56432/v1/models` → 返回各 BYO models 并集（models=[] 的行与内置行不贡献）。
8. **掩码**：列表页 api_key 列只见掩码（前4***后4），编辑弹窗留空保存不丢 key。
