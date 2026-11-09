# 节点转发网(mesh)前端实施计划(阶段 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 仪表盘组网视图——状态条、SVG 拓扑画布(节点/节点对连线/规则徽标)、连线详情面板、离线兜底常驻卡、RulesSettings 增 mesh 目标选项,消费已上线的 `/api/mesh/*` 与 `customer.mesh` 契约。

**Architecture:** 单卡片组件 `MeshSettings.vue` 挂 SettingsPage ①代理设置区(SshIngress 先例「代理族相邻挂载」);SVG 原生画布(零新依赖,仓内已有 SVG 先例);规则编辑单编辑器原则留在 RulesSettings.vue(legacyActionTail 红线),组网页只读展示;配置写经既有 `/api/config/customer` patch 链。

**Tech Stack:** Vue 3 + Tailwind(CDN 内联配置为准)+ vitest(jsdom,createApp+h() 挂载模式,无 @vue/test-utils)+ 既有 fetch stub 模式;**零新依赖**。

**规范:** `docs/mesh-forwarding-network-design.md` §9(已获用户逐项确认);后端契约=`/api/mesh/status`、`/api/mesh/lan_peers`、`customer.mesh`(types.go:736-755)。

**工作区:** 从当前 master HEAD 新开 worktree `.worktrees/mesh-frontend` 分支 `feat/mesh-frontend`——**不用 `.worktrees/mesh-forwarding`**(其前端范式落后主树一个 Task 12 合并)。

**范围裁定补充(计划评审定案):**
4. **环路红牌降级为保存期错误呈现**:画布无拖拽连线操作(连线由规则派生),拖拽时红牌不适配;环路防护=后端 ValidateMesh 保存期拒绝(卡片 error 框呈现)+ 可选客户端解析 `mesh:` 尾段的环提示横幅。设计 §9.2 拖拽红牌为本适配的牺牲项。
5. **节点点击详情面板**:与连线点击同级支持(节点卡点击 → 右栏显示节点信息+关联规则),Task 3 一并实现。
6. **状态条「离线」数据源(定死)**:`/api/mesh/status` 无平台可达性字段——离线判定用 `/api/agent/status` 的设备运行状态(AgentSettings.vue 既有 runtimeOnline 模式);status 拉取失败 → 视作降级态(琥珀+提示,区别于确认离线)。

---

## 关键契约与模式(实施者必读)

| 项 | 锚点 | 要点 |
|---|---|---|
| 卡片 props/emits | `app/website/src/components/settings/SshIngressSettings.vue:244-251` | config/loading/saving/error/successMessage 五 props + save emit;卡片不自取配置 |
| 归一化具名导出 | 同上 :194-214 `normalizeSSHIngressConfig` | `normalizeMeshConfig(payload)` 输出 snake_case 线格式;**三态布尔语义:enable nil=true、forward_enable nil=false、relay nil=true、fallback.enable nil=false**(镜像 normalizePeerMCPConfig 注释:「漏挂=保存 200 但永不落盘」) |
| patch 链 | `SettingsPage.vue`(主树:497-504 `mergeCustomerSavePatch`/`buildCustomerSavePatch` 具名导出)+ `createDefaultCustomerConfig`/`normalizeCustomerConfig` 链 | 三处都要挂 `mesh: normalizeMeshConfig(...)` |
| API 服务 | `services/agentApi.js:127-136` 函数形态 | 新增 `getMeshStatus()`/`getMeshLanPeers()`(request + 信封拆包,调用方处理错误) |
| 挂载测试模式 | `SshIngressSettings.test.js:121-134` | createApp+h() 手动挂载;i18n vi.mock 返回键名;fetch vi.stubGlobal;`data-*` 选择器(勿用 #id);flush(rounds) |
| i18n | `zh.js`/`en.js` 扁平单键对象,`sshIngress_*` 家族先例(26 键) | 新家族 `mesh_*`;zh/en 键集必须相等(i18n.test.js 守卫) |
| 卡片样式 token | 卡片容器/错误框/成功框/主按钮类清单见探索报告 | 全用既有 token,不发明新类 |
| legacyActionTail 红线 | design §13.1 | **注意:后端 legacyActionTail={proxy,direct,reject,no-resolve} 不含 mesh**——mesh 尾在后端 ClassifyEntry **之前**由 splitMeshTail 剥除。前端对应改法:**禁止**把 mesh 加进 RulesSettings 的 ACTION_TAIL_WORDS(会造成集合不对称);正确改法=逗号条目校验/提取路径加 mesh 尾感知分支(geoip: 分支先例)。**今日已存在的前端缺陷**:裸逗号形态 `x.com,mesh:dev-b` 被 validateRuleEntry 拒绝而后端接受——本计划修复它 |

**范围裁定(实施期适配,已确认):**
1. 组网落 ①代理设置区卡片(非独立页面/非侧栏——前端无侧栏,横向 tab 模型);卡片内承载状态条+画布+详情+兜底卡
2. 规则**编辑**单编辑器原则:RulesSettings.vue 加 mesh 目标选项;组网页连线点击 → 只读规则 chips(过滤该节点对的条目)+「到规则设置编辑」指引
3. 画布节点位置:自动布局(本机居中+peers 环绕),**拖拽位置仅会话内视觉调整,localStorage 按 device_id 持久**(customer.mesh 无位置字段,后端不改)
4. 不做:UDP/实时拓扑探测可视化、多 peer 兜底、结构化规则编辑器(条件/策略/优先级表单)——规则以条目文本 chips 呈现

---

### Task 1: API 服务 + normalizeMeshConfig + patch 链接线

**Files:**
- Modify: `app/website/src/services/agentApi.js`(新增 `getMeshStatus`/`getMeshLanPeers`)
- Create: `app/website/src/components/settings/MeshSettings.js`(纯逻辑模块:normalizeMeshConfig/fallbackGate 类别常量——**.js 非组件**,便于纯测试;若仓惯例倾向放 .vue 具名导出,可改挂 MeshSettings.vue 具名导出,报告说明)
- Modify: `SettingsPage.vue`(createDefaultCustomerConfig/normalizeCustomerConfig 挂 mesh 键;模板挂 MeshSettings 卡片占位——本任务先挂空壳卡)

- [x] Step 1: 失败测试(normalize 三态语义矩阵/默认值/坏输入;api 函数信封拆包)
- [x] Step 2-4: TDD 绿
- [x] Step 5: 提交「新增:mesh 前端数据层——API 服务+normalizeMeshConfig+patch 链接线」

### Task 2: MeshSettings 卡片骨架——状态条 + 离线兜底常驻卡

**Files:**
- Create: `app/website/src/components/settings/MeshSettings.vue`、`MeshSettings.test.js`
- Modify: `SettingsPage.vue`(①代理设置区左列 SshIngress 旁挂载;import normalize 具名导出)

- [x] Step 1: 失败测试:状态条三态(**数据源已定死**:离线判定=/api/agent/status 设备运行状态,AgentSettings runtimeOnline 既有模式;/api/mesh/status 拉取失败=降级态琥珀+提示;均正常=绿色在线);兜底卡编辑(Enable 开关/Addr 手输+lan_peers 候选点选下拉/Classes 三勾选 AI/自身/自定义);save emit payload 含 `mesh.fallback`+`mesh.enable`;兜底 Enable=true 而 Addr 空 → validationError 禁保存;normalize 产出显式布尔(nil→显式 true/false,镜像 ssh_ingress「恒产出显式」先例并注释)
- [x] Step 2-4: TDD 绿(样式全用既有 token;错误/成功框 role=alert/status)
- [x] Step 5: 提交「新增:组网卡骨架——状态条+离线兜底常驻卡」

### Task 3: SVG 拓扑画布 + 节点对连线详情

**Files:**
- Modify: `MeshSettings.vue`(或拆 `MeshCanvas.vue` 子组件——若单文件超 500 行拆分,报告说明)
- Modify: `MeshSettings.test.js`

- [x] Step 1: 失败测试:本机节点居中+peers 自动布局(SVG 节点组渲染/device_id 标签);节点对连线+规则数徽标(从 customer.proxy_rules 解析 `mesh:<id>` 尾段计数,**客户端解析与后端 splitMeshTail 语义对齐:尾段 mesh: 前缀、可选策略段**);点击节点对 → 详情面板列出该对只读规则 chips + 编辑指引;**点击节点卡 → 右栏节点详情面板**(评审回填);保存期环路错误经卡片 error 框呈现(范围裁定 4:拖拽红牌不适配,后端 ValidateMesh 为防护网);localStorage 位置持久(拖拽后刷新保持)
- [x] Step 2-4: TDD 绿(jsdom 下 SVG 断言用 querySelector;拖拽用 pointer 事件合成或直接调位置更新函数——取稳)
- [x] Step 5: 提交「新增:组网拓扑画布——节点/节点对徽标/详情面板+位置持久」

### Task 4: RulesSettings mesh 目标支持(红线正确同步+既有缺陷修复)

**Files:**
- Modify: `app/website/src/components/settings/RulesSettings.vue`(**禁止**改 ACTION_TAIL_WORDS 集合;逗号条目校验/提取路径加 mesh 尾感知分支——geoip: 分支先例:特定前缀尾部在通用尾部剥离前先行识别)
- Modify: `RulesSettings` 测试

**后端真实语义(以此为准,勿按旧文本):** `legacyActionTail={proxy,direct,reject,no-resolve}` 不含 mesh;mesh 尾由后端 splitMeshTail 在 ClassifyEntry 之前剥离,形态=`mesh:<id>` 或 `mesh:<id>,<policy>`(policy ∈ direct|deny|fallback:<id>)。前端今日缺陷:裸逗号形态 `x.com,mesh:dev-b` 被 validateRuleEntry 拒绝(冒号撞 ENTRY_CHARS_RE)而后端接受。

- [x] Step 1: 失败测试:①裸逗号形态 `x.com,mesh:dev-b` 校验通过且生成正确条目(修复今日缺陷);②`x.com,mesh:dev-b,fallback:dev-c` 策略段形态;③`x.com,mesh:` 空 id 拒绝;④keyword 形态回归不破;⑤非 mesh 尾既有行为零变化
- [x] Step 2-4: TDD 绿。**实施说明(与计划的偏差)**:`RulesSettings` 的 proxy_rules 输入面是**纯文本域**(无目标下拉/无 action 选项控件——唯一下拉是 `proxy.type` 的 socks5/http/ssh),因此「加 mesh 选项」无落点;实际做的是让**校验/提取/塌缩三条纯函数路径**都能正确吃下 mesh 尾段(这才是本期真需求,也是裸逗号缺陷所在)。语法可发现性改由 `rules_proxyRulesDescExt` 文案 + placeholder 示例 + `rules_meshInvalid` 本地报错承担。红线核对:`ACTION_TAIL_WORDS` 未动。
- [x] Step 5: 提交「修复:RulesSettings mesh 尾支持——裸逗号形态拒绝缺陷+策略段+目标选项(红线正确同步)」

### Task 5: i18n + 全量回归

**Files:**
- Modify: `app/website/src/i18n/zh.js`、`en.js`(mesh_* 家族 ~30 键:title/description/statusOnline/statusOfflineFallback/canvasHint/fallbackTitle/fallbackAddr/fallbackAddrPlaceholder/fallbackClasses/fallbackClassAI/fallbackClassSelf/fallbackClassCustom/lanPeersTitle/lanPeersEmpty/ruleCountBadge/ruleEditHint/save/saving/validationFallbackAddr/nodeSelf/nodeOffline …)
- Modify: `i18n.test.js` 若扩展死键前缀过滤(mesh_ 纳入)

- [x] Step 1: 键集相等守卫测试(i18n.test.js 既有断言自动覆盖)
- [x] Step 2-4: 实现键 + `npm test` 全量绿 + `npm run build` 绿
- [x] Step 5: 提交「新增:组网 i18n——mesh_* 家族 zh/en」

### Task 6: e2e 冒烟 + 文档

**Files:**
- Create: `app/website/tests/e2e/mesh-settings.spec.js`(可选:mock /api/mesh/* 后断言卡片渲染——playwright route 拦截)
- Modify: `docs/mesh-forwarding-network-design.md` §9 标注实施适配(卡片挂载/规则编辑单编辑器/位置 localStorage 三点)

- [x] Step 1-3: e2e **已真实落地**(`tests/e2e/mesh-settings.spec.js`,3 例,全后端 route mock,未降级):画布渲染+徽标+两路详情面板、兜底卡保存的差异 patch、规则编辑器 mesh 尾段接受/报错。过程中暴露并修掉一个真实可用性缺陷:轴向连线包围盒退化为 0 导致点击不可达 → 命中区与可见线分离(见设计 §9.6 第 6 条)。
- [x] Step 4: 提交「新增:组网 e2e 冒烟+设计文档实施适配记录」

---

## 执行顺序与依赖

Task 1 → 2 → 3 → 4 → 5 → 6。2/3 同组件强耦合串行;4 独立可并行;5 依赖 1-3 的键清单;6 收尾。

## 已知边界

- 设计 §9.3 的「结构化规则编辑器(条件/动作/策略/优先级表单)」本期不做——规则编辑留在 RulesSettings 既有范式(条目文本+目标选项),组网页只读展示;结构化编辑器视使用反馈立项
- 节点「公网出口能力」「子网标签」字段后端 status 未下发(仅 device_id/enable/forward_enable/lan_listen_port/peers/lan_peers)——节点卡一期显示:状态灯+device_id+lan_listen_port;其余字段待后端补上报后跟进
- 「被依赖计数」可从解析后的规则客户端派生——节点详情面板实现时一并计算
- normalize 契约注记:mesh 保存把 nil 默认转显式布尔(enable nil→显式 true),镜像 ssh_ingress「恒产出显式」先例(SettingsPage.vue:313 注释)——无害但需注释防后人「修复」
- 自动选路/UDP/限流(§9.5)同后端二期
