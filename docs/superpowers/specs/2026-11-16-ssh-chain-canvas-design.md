# SSH 出口链路 canvas 拓扑化设计(2026-11-16)

状态:已获用户逐节认可(方案 A + 五节设计拍板),待 spec 评审。

## 1. 背景与问题

出口网络设置页(RulesSettings)「测试连接」下方平铺显示「连接 1/连接 2/连接 3」状态灯,用户两次误读为配置了多条链路/数量错误。2026-11-15 已定案(提交 f1d3957,展示诚实化改词):数量来源是 `outbound/proxy/ssh/ssh.go` 的池默认值——`pool_size` 未配置时默认 3,池内 3 条是**同一路径的并行 SSH 连接**(负载分担+单链退避兜底,spec §4 拍板),不是多条链路。真机双侧证据:Mac SSH 入口台账记录 172.16.0.196 同时 3 条连接、同一秒集体断开。

平铺列表的残余问题:不表达拓扑(本机→跳板→出口的路径形状看不见)、跳板节点无地址、两处展示(RulesSettings 灯区 / LogsSettings 状态卡)各画各的、口径分裂。

## 2. 目标

- 链路拓扑可视化:完整画出「本机 → 跳板 1 → … → 跳板 N → 出口」节点链,每个节点显示基础信息。
- 两处展示统一:RulesSettings(测试连接下方灯区)与 LogsSettings(状态页 SSH 出口卡)替换为同一个共享拓扑组件。
- 节点信息用现有数据全量:角色 + host:port + 每条并行连接的存活/退避剩余秒/活跃通道数。

## 3. 非目标(YAGNI)

- **连接池默认值不动**:保持 `pool_size` 未设 → 3。想要单连接的用户在表单填 `pool_size: 1`(用户 2026-11-16 拍板)。
- 不新增延迟/流量探测(二期)。
- 不做节点拖拽(mesh 画布的拖拽属 peers 自由布局;链路拓扑形状固定,无需拖拽)。
- mesh 页不改动(其 SVG 拓扑画布是 peers 视图,与本设计无关,仅复用其范式)。

## 4. 方案决策记录

| 决策点 | 拍板 |
|---|---|
| 实现形态 | 方案 A:共享 Vue 组件 + 零依赖 SVG(对齐 `MeshSettings.vue` 拓扑画布先例),否决字面 `<canvas>` API(重绘循环/DPR/命中测试成本高、与先例割裂)与节点卡列表(无拓扑形态) |
| 替换范围 | 两处全换(RulesSettings 灯区 + LogsSettings 状态卡) |
| 节点信息 | 现有数据全量(跳板地址需后端补一个字段) |

## 5. 架构

### 5.1 后端(唯一 API 变更)

- `outbound/proxy/ssh/ssh.go`:
  - `SSH` 结构体新增 `jumpAddrs []string`(路径顺序,`hostPort()` 形态),`New()` 组装时从 `jumps []NodeConfig` 留存。
  - 新增方法 `JumpAddrs() []string`(返回副本或 nil,永不返回可变内部切片)。
- `outbound/registry.go` `SSHChainStatus`:
  - `type SSHChainStatus` 新增字段 `JumpAddrs []string \`json:"jump_addrs,omitempty"\``;
  - `Registry.SSHChainStatus()`(registry.go:320)透传 `ssh.JumpAddrs()`。
- wire 形态示例:`{ok, addr, jumps, jump_addrs, chains}`;`jump_addrs` 与 `jumps` 同为 omitempty,旧 payload 兼容。

### 5.2 前端组件

新建 `app/website/src/components/settings/SshChainCanvas.vue`:

- **props**:`ssh`(对象,形态=状态 summary 的 `data.ssh`:`{ok, addr, jumps, jump_addrs?, chains}`)。
- **布局**(viewBox 640×360 基准,泳道多时纵向延伸):
  - 上部:节点链横向排列,`本机 → 跳板 1 → … → 跳板 N → 出口`。节点=圆角矩形:角色标签(i18n)+ 地址行(跳板/出口显示 host:port;`jump_addrs` 缺失时降级只显「跳板 N」;本机节点只显角色)。节点框中性描边——状态语义全部归属连接泳道,节点不着状态色。
  - 下部:并行连接泳道,每池一条(数量=chains 长度,池上限 16):状态点(绿=存活/琥珀=退避+剩余秒/灰=空闲)+ 文案沿现有键 `status_sshChainAlive` / `status_sshChainBackoffIn` / `status_sshChainIdle` + `status_sshChainActive`(活跃通道数)。行标签沿用 `status_sshChainLabel`(连接 {index})。
  - **状态推导收敛**:泳道状态推导(存活/退避/空闲 → 语义色 + 文案键 + 活跃数文案)现重复于 `RulesSettings.vue` ~1404-1423 与 `LogsSettings.vue` ~638-668 两处,本批收敛为 `SshChainCanvas.vue` 内**单份实现**,两处页面级映射删除,不产生第三份。
  - 语义色取 mesh 画布 §9.2 同款常量(SVG 属性不吃 Tailwind `dark:` 变体)。
- **空态**:`ok=false` → 虚线框空态(样式对齐 mesh 画布空态);空态文案**归属组件**且不新增键——`ok=false` 复用现有键 `status_sshNotConfigured`,`ok=true` 而 `chains` 为空(首轮轮询前的瞬时窗)同样式复用 `rules_sshChainIdleHint`(两键均已过 parity 测试)。
- **错误处理**:轮询失败沿用静默保上一帧纪律(两处现有行为,不改);viewBox 缩放天然适配窄屏。

### 5.3 两处替换

- `RulesSettings.vue:390-407`(`data-ssh-chain-lights` 区块):整体替换为 `<SshChainCanvas :ssh="…">`;**测试连接按钮与其结果区不动**;5s 轮询逻辑(`loadSshChains`/`startSshChainPolling`)保留,数据源不变。
- `LogsSettings.vue:358` 起(`data-ssh-path` 路径行 + `sshChainCards` 连接卡):卡片体内路径行与连接行整体替换为 `<SshChainCanvas :ssh="sshStatus">`(出口地址、跳板级数、并行连接数的展示语义全部并入画布节点与泳道);卡片标题与外层结构不动。

## 6. 数据流与兼容

- 前端仍 5s 轮询 `/api/status/summary`,取 `data.ssh` 整体传组件,轮询与组件解耦。
- **存储形态变宽(防误读)**:RulesSettings 的 `sshChainsRaw` 现仅存 `data.ssh.chains` 数组,本批拓宽为存整个 `data.ssh` 对象再传组件;「数据源不变」指轮询端点与 5s 频率不变,组件入参形态是新的。
- `jump_addrs` 缺失(旧 agent 新前端)→ 画布降级:跳板节点只显「跳板 N」不带地址,布局不变;同包出包部署下两端同步,此分支仅为灰度窗口兜底。
- 池大小不再单独下发:泳道数 = `chains.length`(`pool.Status()` 恒返回全部链,含死链,长度即池大小)。

## 7. 测试

- **Go**:
  - `ssh.New` 后 `JumpAddrs()` 断言:有跳板(地址按路径序)、无跳板(nil/空)。
  - `Registry.SSHChainStatus` wire JSON 断言:`jump_addrs` 字段存在与形态(顺带补上 11-15 遗留账「Jumps wire 形态无 Go 侧断言」)。
- **vitest**:
  - `SshChainCanvas.test.js`:三态渲染(存活/退避/空闲)、节点信息(本机角色/跳板地址/出口地址)、`jump_addrs` 缺失降级、`ok=false` 空态。
  - `RulesSettings.test.js` / `LogsSettings.test.js`:锚点更新(`data-ssh-chain-lights`、`data-ssh-path` 等旧锚点迁移到画布组件锚点),现有文案断言按组件内键迁移。
- **i18n**:新增键(画布内节点角色、空态等)进 zh/en 双语 parity 测试,口径遵循既有约定(全角标点/「你」)。

## 8. 验收与风险

- 单测全绿 + 真机冒烟:0.196 当前链路已断(11-15 21:38 后 active=0),验收需先在 0.196 重新启用出口链路,真机确认画布节点链与泳道状态。
- 「canvas」一词歧义已随方案 A 拍板消解:实现是零依赖 SVG(与 mesh 画布同范式),不是 `<canvas>` API。
- 遗留账并入:11-15 的「Jumps wire 形态无 Go 断言」在本批测试节补上。
- EN 术语拍板(2026-11-16 Task 2 评审记录):画布节点出口用 `Exit`(拓扑惯语),与卡片标题 `SSH Egress Links` 并存可接受;本机用 `This device`(SSH 出口是设备域),与 mesh 画布 `This node`(抽象节点域)有意区分,后续画布统一 pass 不再翻案。
