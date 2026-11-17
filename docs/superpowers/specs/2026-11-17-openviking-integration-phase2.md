# OpenViking 集成 Phase 2:平台托管接入设计(2026-11-17)

状态:调研与部署冒烟已完成(Phase 1,全链实测证据见 §14),本 spec 为产品化接入设计,待评审拍板(§13 未决问题)。

## 1. 背景与现状

Phase 1(已完成,运行中):OpenViking v0.5.5 自托管于 k8s nursor(私仓镜像 `register.liang.home/library/openviking:v0.5.5-embed3`,含 llama-cpp-python 0.3.37 + libgomp1),公网 `https://openviking.aliang.xin/mcp`(MCP Streamable HTTP,`Authorization: Bearer` 鉴权)。全链实测:27 工具、remember→VLM 自动抽取→结构化记忆落库、find 语义检索 83% 命中、per-user key 身份隔离实证。当前仅本机手工注册(`~/.claude.json` mcpServers.openviking,claude-code 专用 user key),无平台化能力。

产品缺口:<100 用户增长期,需要「安装即集成、per-用户隔离、可卸载、可共存」的平台托管接入。已定案走 Route A(云端铸 key lazy-mint),否决 trusted 网关与 OAuth(OVER-KILL,见 §4)。

## 2. 目标

- 用户装机后零手工获得 OpenViking 工具面(三内核:claude/agentcore/codex;codex 依赖 §7.6 解除断点)。
- 一个平台账号 ↔ 一个 OpenViking user:per-用户记忆隔离(key 内嵌身份,服务端双层隔离,已实证),平台持 ROOT key 铸 key,永不落设备。
- 会话 capture 生命周期:成功回合自动沉淀到服务端(宿主层实现,与官方 Claude Code 插件 9-hook 生命周期一一对应)。
- 干净卸载:一键关停=条目自动移除+流量归零;数据保留策略独立控制。
- BYO 共存:用户已有 OpenViking 配置/官方插件时不覆盖不劫持,提供托管/自带双模式。

## 3. 非目标(YAGNI)

- **回合前自动 recall 注入**:Phase 2+ 。先验证工具自助模式价值,避免与现有 `<memory>` 注入双份烧 token(验收线 §11.1 未过之前不做)。
- **trusted 网关 / OAuth issuer**:1000+ 用户或企业客户时的优化,现为负资产。
- **手机端(PhoneServer RN)配置卡**:Phase 2 仅仪表盘;手机通道白名单另批次。
- **离线 capture 队列**:下线期间会话静默缺失,接受(诚实降级语义已定案)。
- **usage 配额/计费**:服务端 usage audit 有现成数据,接驼时再做。

## 4. 方案决策记录

| 决策点 | 拍板 | 依据 |
|---|---|---|
| 接入路线 | A:云端铸 key lazy-mint(PhoneServer 薄端点) | 复用既有配置下发+认证管线;official-website/中间层/sub2api 零改动;~2-3 人日;规模无上限(key 无状态 O(1)/请求) |
| 铸 key 时机 | 设备首次连云取配置时(非注册时) | 解耦注册流程;OpenViking 为可选增强,注册流程不需要知道它存在 |
| 身份映射 | 一个平台账号 ↔ 一个 OpenViking user(同 account 下),peer 维度预留 | 共享 resources 知识库 + 私有 user 记忆,正是产品形态 |
| 用户已有配置 | 不覆盖不劫持;BYO 通道 + 一键迁移 | §7.3 |
| capture 触发 | 仅成功回合(detached goroutine,失败即弃) | 与现有记忆层同纪律;垃圾不沉淀;有意比官方插件(除 kill -9 全提交)收紧 |
| 净收益前提 | dogfood 四条验收线全过才启动 Phase 2 实施(§11.1) | 正反对抗论证共同判据;Plan B=journal+pgvector 自建(同价,切换不伤筋) |

## 5. 架构总览

```
设备 agent 启动 boot 自愈
  → customer.openviking.enabled?
  → GET <api_server>/api/openviking/credential(既有平台认证)
      PhoneServer:auth 中间件认出用户 → 查 openviking_credentials 表
        首次:持 ROOT key 调 OpenViking Admin API 铸 user_id=<平台用户id> 的 key,入库
        之后:返回存量 key(铸一次存一次,不轮换)
  → 写统一 MCP 注册表 ~/.aliang/mcp.json + ~/.claude.json(双写,aliang-peer 纪律)
  → BYO 检测:已有同名条目则跳过+提示(§7.3)
  → 三内核下个 run 生效(claude sanitized 档经 claudeTierMCPArgs 合并/full 档原生发现;
    agentcore 每 run 采集;codex 经托管块——需 §7.6)
会话结束(成功回合)→ capture 钩子 → journal transcript → OpenViking session commit(§7.4)
```

## 6. 配置面设计

- `processor/config/types.go` CustomerConfig 尾部追加(照 peer_mcp:591 家族惯例):

```go
OpenViking *OpenVikingConfig `json:"openviking,omitempty"`
type OpenVikingConfig struct {
    Enabled   *bool  `json:"enabled,omitempty"`    // nil=默认关(新增强化层默认不开,显式开启)
    Endpoint  string `json:"endpoint,omitempty"`   // 默认 https://openviking.aliang.xin/mcp;BYO 可覆盖
    Mode      string `json:"mode,omitempty"`       // "managed"(默认)|"byo"
    APIKey    string `json:"api_key,omitempty"`    // 仅 byo 模式:用户自带 key;managed 模式从云端取
}
```

- 惯例铁律:nil=整层关;保存走 deepMerge 缺键 preserve;**patch 数组字段必须发全集**(status_ingress_handler.go:180 实证);前端必须走 SettingsPage 父层 mergeCustomerConfig+buildCustomerSavePatch,不可绕父层裸 POST(SettingsPage.vue:604-609 实证,否则踩「缺键置零段」潜伏缺陷)。
- 运行时读点 `GetGlobalConfig()` per-run,热生效;托管注册类开关切换不即时 apply——ensure 自愈在 boot+显式 apply 端点双通道(照 agent_peer_mcp.go:245)。
- 未知键:config UnmarshalJSON 记录 customerUnknownFields,新增字段必须先落 Go struct,否则用户手写 `customer.openviking` 进未知键通道。

## 7. 详细设计

### 7.1 凭据服务端点(PhoneServer 仓,~100-150 行)

- `POST /api/openviking/credential`(设备以既有平台认证调用;无新认证机制):
  - auth 中间件认出用户 → `SELECT * FROM openviking_credentials WHERE platform_user_id=?`:
    - 有 → 直接返回 `{endpoint, api_key, user_id}`(不轮换)
    - 无 → 服务端持 ROOT key 调 `POST /api/v1/admin/accounts/{account}/users`(`{user_id, role:"user"}`)→ 返回 user_key → 入库 → 返回
  - ROOT key 存放:PhoneServer env/secret(secretKeyRef),**永不下发设备**;同 ns 内网直连 `openviking.nursor.svc` 更佳(Admin API 不走公网 ingress,可选加固:ingress 屏蔽 `/api/v1/admin/*` 外网路径)。
  - 表:`openviking_credentials(platform_user_id PK, ov_user_id, api_key, account_id, created_at)`。
  - 撤销/轮换:管理动作调 `POST /accounts/{account}/users/{user}/key` regenerate+更新表;设备下次 ensure 自愈取新 key。
- 失败语义:OpenViking 不可达→503+明确错误码,设备 ensure 静默跳过本 boot(Warn),下 boot 重试——增强层失败模式。

### 7.2 客户端 ensure 自注册(alianggate,~100 行)

- 照 `agent_peer_mcp.go` 全量往返纪律(原子写、UseNumber):
  - boot 自愈挂点:`app/agentruntime/server.go:93`(:100 之后)新增 `EnsureOpenVikingMatchesConfig`。
  - managed 模式:调云端 credential 端点 → 写注册表条目 `{type:"http", url, headers:{Authorization:"Bearer <key>"}, enabled:true, exclude_tools:[管理面清单 §7.5]}` → 同步 `~/.claude.json` mcpServers.openviking(照 agent_peer_mcp.go applyPeerMCPClaudeRegistration 范式)。
  - disabled:移除两处条目(干净卸载语义)。
- 注册表条目名 `openviking`(`^[a-zA-Z0-9_-]{1,64}$` 合法,非 aliang- 前缀;上限 24 条内)。
- **BYO 检测先行**:写之前查两处目标——已有同名条目且非本平台托管写入 → 跳过+置 `integrations` 报告状态「检测到用户自有 OpenViking 配置,未接管」(仪表盘 AgentIntegrationsSettings 只读报告已有此通道),UI 提供手动「切换到托管」。官方 Claude Code 插件共存检测:插件存在时 UI 提示二选一(双注入烧 token 风险)。
- claude 档位边界(如实写进预期):sanitized 档经注册表合并注入;full 档原生 user 域发现;**isolated 档永不注入**(policy_notice effective 字段可观测)。

### 7.3 capture 生命周期(宿主层,~80 行)

- 挂点:`agent_ai_agentcore.go:1642` `agentCoreMemoryExtractAsync` 调用旁并列一个 `openvikingCaptureAsync`(同款 detached+60s 超时+任何失败只 Warn 纪律)。
- 触发门:仅 run 成功收尾(与现有记忆层同门:中断/stopped/失败/僵尸不捕获);`customer.openviking.enabled` 且 MCP 建连健康。
- 数据源:`~/.aliang/ai-sessions/<sessionID>.jsonl` 的 msg/turn 行(现成 transcript,appendAssistantTurn:655)。
- 上传:OpenViking session commit 接口(服务端 Compress→Archive→Memory Extraction 全后台);**脱敏纪律**:入队前剥离 headers/env 值红线同 agent_ai_agentcore_mcp.go:6-7(「env/headers 值永不进日志」家族);密钥类内容依赖服务端 privacy 机制不强求。
- 注入标记防回吃:capture 内容若曾含 recall 注入块,剥 `<openviking-context>` 标记块(官方教训:capture 会剥标记块防自吞)。

### 7.4 工具面治理(excludeTools)

注册表条目配 exclude_tools 裁管理面误触面:`list_users,list_groups,get_acl,set_acl,privacy_tree,privacy_upsert`(USER 角色 key 本就调不动 manage 类,双保险)。保留 21 个数据面工具,占 agentcore 128 工具预算 ~16%。

### 7.5 观测(防「静默降级=监控盲区」——对抗论证反方胜点,必做)

- 心跳拨测:ensure 建连成功后每 N 分钟(默认 10m)对 `/health` 轻探;失败连续 3 次→ integrations 报告置「OpenViking 不可达」+ 仪表盘卡状态灯变红。不做自动关停(静默降级是特性,但要可见)。

### 7.6 codex 内核 headers 断点解除(前置拍板项)

现状:注册表→codex 托管块同步刻意丢 headers(`agent_mcp_registry.go:381-407`,spec §6.4),OpenViking 走托管块必 401。本机 codex config.toml 已有 `http_headers` 子表先例(context7/glm-camp)。**改动=`aliangMCPCodexBlockLines` 输出 headers 到 `http_headers` 子表+自愈自检对齐**;属跨内核契约变更,发布说明必写。此项独立可先行,是 §11.1 验收线第 4 条的前置。

### 7.7 UI 卡落点

- `AgentPage.vue` aliangcode tab(或 user-agent tab):「OpenViking 跨设备记忆」卡——三态开关(托管/BYO/关)+状态灯(拨测)+数据删除按钮;范式照 PeerMCPSettings 两段式保存+AgentMCPGlobalSettings 三态 dirty。
- 文案 zh.js/en.js 双改,照 `agent_integrations_*` 口径(全角标点/「」/「你」/token 小写)。

## 8. 数据模型

PhoneServer postgres 新表(唯一新增存储):

```sql
CREATE TABLE openviking_credentials (
  platform_user_id VARCHAR PRIMARY KEY,
  ov_account_id    VARCHAR NOT NULL DEFAULT 'aliang',
  ov_user_id       VARCHAR NOT NULL,      -- 即平台用户 id 的 slug
  api_key          TEXT NOT NULL,          -- 三段式 key,铸一次存一次
  created_at       TIMESTAMPTZ DEFAULT now()
);
```

## 9. 安全考量

- ROOT key 仅存 PhoneServer secret;设备侧 key 为 USER 角色三段式(破坏面被服务端钳死:forget/set_acl 需 manage,USER 调不动)。
- key 落盘 0600;设备上报清单已对 authorization 头定向打码(agent_mcp_inventory.go mcpAuthHeaderNames);「headers 值永不进日志」红线沿既有纪律。
- AGPL:未修改 server 自托管+专有客户端 MCP 调用,§13 不触发(许可文本推论,非法律意见);**派生镜像(加 llama-cpp-python)是否算「修改」需留意**——镜像内 pip 安装依赖不改动 server 源码,倾向不触发,正式商用前过一次许可证审查。
- 镜像私有仓 register.liang.home,免鉴权内网开放(现状),建议后续加 basic auth(独立小活,不阻塞)。

## 10. 生效链

- **配置/注册表改动**:56431 仪表盘保存即热生效(注册表三内核 per-run)。
- **代码/UI 改动**:website build→go build→tag→pkg/MSI/deb→装机;master 现有未推批次与脏工作树,动 types.go/zh.js/en.js 前 git status 对齐,按文件归属切分提交,勿 add -A。
- PhoneServer 端点:标准部署管线(k8s),同 ns 直连。

## 11. 验收标准

### 11.1 dogfood 验收线(Phase 2 实施的闸门,当前进行中)

1. 真实问题检索命中 ≥7/10(10 条自选工作问题盲测)
2. 净 token 账为正(抽取 VLM 成本 < recall/检索节省的 prompt 成本)
3. 两周无 P0 级上游事故(breaking change 导致数据迁移/服务不可用)
4. codex headers 解除后三内核回归绿

### 11.2 实施验收清单

- [ ] 新装设备:开开关→boot 后三内核 tools/list 可见 21 工具,零手工
- [ ] 两设备同账号:一端 remember,另一端 find 命中(跨设备核心卖点)
- [ ] BYO:预置同名条目→ensure 跳过+仪表盘提示;一键迁移可用
- [ ] 卸载:关开关→两处条目消失→工具消失→流量归零;数据保留;删除按钮真正按用户域抹除
- [ ] capture:成功会话后服务端 sessions 出现记录;失败/中断会话无记录
- [ ] 拨测:停 openviking deployment→3 周期内仪表盘变红;恢复→变绿
- [ ] codex:托管块带 http_headers,tools/list 200
- [ ] 深合并:patch 只带 enabled 键不丢 endpoint 现值(缺键 preserve 回归)

## 12. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 上游 Alpha+breaking(真发版税) | 锁 v0.5.x;验收线 3 兜底;Plan B=journal+pgvector 自建(集成面收敛在注册表层,切换不伤筋) |
| 双注入(官方插件+我们) | §7.2 共存检测提示二选一 |
| 与现有 `<memory>` 层重复沉淀 | 共存期 prompt 标注来源优先级;验收线 2 过后拍板切换(`memory_enabled=false` 即完成替换且可回滚) |
| VLM 成本随用户×会话线性涨 | flash 档模型;usage audit 监控;配额后置 |
| 单实例天花板 | 升级路径 pgvector 多副本(v0.5.5 刚出);当前 <100 用户无近忧 |
| 部署锁死锁 | **deployment strategy 必须改 Recreate**(kuboard 手动,SSA 改不动旧 manager;每次镜像更新前核对) |

## 13. 未决问题(需拍板)

1. Phase 2 实施闸门:是否等 §11.1 四条全过后才动工?(建议:是;codex headers 解除可提前独立做)
2. 卸载数据默认语义:保留(建议)还是删除?
3. BYO 迁移「一键切换」是否 Phase 2 必做?(建议:提示可后,迁移按钮后置)
4. derive 镜像的 AGPL 「修改」认定:商用前专项审查(挂 TODOS)。
5. 手机端配置卡批次时点。

## 14. 附录:Phase 1 实测记录与坑清单(实施者必读)

- 全链证据:MCP 27 工具/write-read-tree/remember→抽取→find 83% 命中/per-user 隔离实证。
- 三大坑:①PVC `/app/.openviking/ov.conf` 整体优先于 env secret(secret 里 `gateway.root_api_key` 为无效键位死配置)——**改配置改文件+重启 pod**;②vlm 模型名含 `glm` 触发关键词探测直连智浦,**修=写 `openai/glm-5.3-flash`**;③内嵌存储数据目录锁 × RollingUpdate(maxUnavailable 25%→0)=死锁,清场=删旧 RS+删新 pod,**strategy 改 Recreate 根治**。
- `/ready` 报 embedding ok 是假绿(不验 llama-cpp-python 可导入);坏 embedding 时期写入内容无向量,重新 remember 触发重索引救活。
- 镜像构建配方:集群内 buildah 特权 pod(vfs 驱动必须,fuse-overlayfs COMMIT 后挂死),ConfigMap 带 Dockerfile,多阶段 python:3.13-slim 编译轮子,推 `registry.my-assistent.svc.cluster.local:5000`;**Mac docker push 大层会死锁,勿走**。
- 运维账:litellm `deepseek-v4.1-flash` 通道订阅失效 403;litellm 一卡死副本(预存);openviking.aliang.xin 证书为多域 SAN(瞬态错配一次已自愈)。
