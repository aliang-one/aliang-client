# SSH 入口设计(双形态:标准客户端裸流 + mesh 帧外壳)

日期:2026-11-08
分支:feat/ssh-ingress(基于 master a92eb58 之后 tip)
状态:设计已获用户批准,待实施计划
前置:出口侧 SSH 协议已合并(v1.1.100,spec 2026-11-07-ssh-proxy-design.md);本设计是其在服务端的镜像扩展

## 背景与目标

出口已支持 http/socks5/ssh 三种协议;入口仅有 `tun` 与 `http` 两种
(`processor/config/routing_schema.go` 的 ingress.mode 枚举),从未有过
socks5/ssh 形态的入口。

本设计为节点新增 **SSH 入口**:节点内置 sshd 服务端,接受外部 SSH 连入,

- **形态①(裸流)**:任意标准 ssh 客户端用 `ssh -D`(动态 SOCKS)/`-L`
  (本地转发)连入——服务端只看到 `direct-tcpip` channel——即可把节点
  当代理出口用。两设备直连场景的落地形态。
- **形态②(mesh 帧外壳)**:peer 节点的 mesh 出口把 mesh 帧协议
  (与 LAN 载体同款帧:首行 JSON 头 + 原始字节)跑在 SSH channel 里
  ——SSH 成为 LAN(明文)/piko(中继)之外的**加密载体**,mesh 元数据
  (来源/类别/防环 path vector/跳数)完整保留。

两种形态注入同一条入站管线(与 mesh B 侧语义 §3.3「与 56432 等本地
监听口同待遇」一致),继承 SNI 嗅探/规则/MITM/失败策略全链。

### 已拍板的关键决策

| 决策项 | 结论 | 理由 |
|---|---|---|
| 定位 | 独立第三入口(不占 mesh 载体拍板);mesh 场景由形态②覆盖 | ssh -D 服务端侧只看到 direct-tcpip,零自定义协议即成代理;mesh 帧外壳解决节点间加密通道 |
| 一期范围 | 两形态全量 | 用户拍板 |
| 服务端宿主 | 内置 x/crypto sshd(进程内) | 与出口客户端栈同族同源;不碰系统 sshd |
| 凭证体系 | 双密钥体系,禁密码 | mesh 互连公钥=形态②;人用授权公钥=形态①;凭证类型即形态区分器(同一监听口无嗅探) |
| UDP | 不做 | SSH direct-tcpip 无 UDP 语义(协议硬限制,与出口同源) |

## §1 双形态数据流

```
形态① 裸流(人用凭证):
  ssh -D/-L 客户端 ──direct-tcpip──▶ inbound/ssh ─▶ 构 Metadata{HostName,DstPort}
    ─▶ 注入入站管线(归「自定义」类,与代理端口入站同待遇)

形态② mesh 帧(节点凭证):
  peer 节点 mesh 出口 ──SSH channel 内跑 mesh 帧(首行 JSON 头+字节)──▶
  inbound/ssh ─▶ 解析帧头 ─▶ AppendHop 防环 ─▶ 门禁(允许被转发+转发方 ACL)
    ─▶ 构 Metadata{HostName/DstIP/DstPort} + IngressFrameMeta{Class,Via,Hops}
       context 携带(接缝见 §2)
    ─▶ 注入入站管线(元数据完整)
```

形态区分器=**连入凭证的公钥类型**;同一监听口,零嗅探。
流量类别语义:

- 形态①:归「自定义」类(与代理端口入站对称——mesh 再转发受
  「自定义仅 LAN」既有约束);
- 形态②:类别由帧头声明,继承 mesh 三类语义(自身/AI/自定义),
  中继载体准入约束在 A 侧拨出时已执行,B 侧不重复裁。

## §2 服务端组件(inbound/ssh 新包)

**`inbound/ssh/server.go` — 生命周期**

- `StartSSHIngress()/StopSSHIngress()/IsSSHIngressRunning()`(模式照
  `inbound/http/server.go` 的 Start/Stop/Is 三件套);
- 监听口独立可配(默认 `56441`,与启用段 56431-56433 避让;另避开
  mesh plan Task 10 提案的 beacon UDP 56440),总开关
  **默认关**;
- host key:首启自动生成 ed25519,落
  `cache.GetCacheSubdir("ssh")/ingress_host_key`(与出口 known_hosts
  同目录族);**仪表盘展示指纹**供客户端侧核对(TOFU 的服务端镜像);
  文件损坏时重生成+指纹更新提示。

**`inbound/ssh/auth.go` — 双密钥准入**

- gossh.PublicKeyCallback:公钥在本机「人用授权列表」→ 形态①;
  在「mesh 互连列表」→ 形态②;都不在 → 拒绝;
- 密码认证**不注册**(PasswordCallback 为 nil);
- 认证失败限速:每来源 IP 失败计数,指数退避拒绝(防爆破),
  限速态拒绝文案与未授权文案可区分(沿用 mesh 门禁文案区分惯例);
- 凭证→形态的映射在认证层完成,后续 channel 处理按已定形态走;
  同一公钥**同时出现在两份列表**属配置歧义:Validate 拦截
  (保存时报「key is listed in both authorized_keys and
  mesh_authorized_keys」),运行期按 mesh 列表优先保守处理。

**`inbound/ssh/channel.go` — channel 处理与安全收敛**

- **硬禁清单**:shell/pty、`-R` 远程转发(forwarded-tcpip 接受侧)、
  agent 转发、X11、session 之外的一切 channel 类型;仅放行
  `direct-tcpip`;非白名单 NewChannel 一律 Reject;
- 全局请求(global request)除必要 keepalive 外一律拒绝/忽略;
- 形态①:direct-tcpip payload 解出目标 → 构 Metadata 注入;
- 形态②:**每 channel** 读首行帧头(JSON,与 LAN 载体同格式;A 侧可能在
  一条 SSH 连接上为多条转发连接复用多 channel,帧头按 channel 计)→
  AppendHop(本机 device_id)→ 防环(Via 含本机即拒,错误回写)→
  门禁(customer.mesh 的「允许被转发」EffectiveForwardEnabled +
  转发方 ACL:来源 device_id ∈ 允许列表)→ channel 内后续字节
  按「帧头目标+原始字节双向」语义注入;
- **`dns:` 目标(帧头合法形态)一期拒连**:错误行明示
  「dns target not supported on ssh carrier」——代解析语义归 mesh
  Task 8 领地,合并后集成批次统一启用,本分支不做半套。

**注入胶水**:`构 Metadata + 喂 processor/tcp 既有入口
(getTCPHandler().Handle 同款路径)`——胶水在 inbound/ssh 内部实现,
**不新增 processor/tcp 文件**(该目录是 mesh 分支的改动领地)。

**类别/Via/Hops 携带接缝(定死,本分支可独立实现)**:master 的
Metadata 与 ProxyRoute 枚举均无 Route/Via/Hops 字段(它们随 mesh
分支落地),本分支**不得自行给 Metadata 加字段或扩枚举**。携带
机制=inbound/ssh 导出自己的 **context 载体**:
`type IngressFrameMeta struct{ Class string; Via []string; Hops int }`,
注入时 `context.WithValue` 挂在传给管线的 context 上(键为包内
导出常量)。本分支只负责「挂载与透传」;消费侧(mesh Task 7 的
dialByRoute 续传)是**合并后集成契约**——mesh 分支合并后由集成
批次把 context 值接入 dialByRoute。§6 的续传测试断言的是「context
值挂载正确」,不是 Metadata 字段。

**集成期注记**:inbound/ssh 为喂管线会 import processor/tcp——合并后
dialByRoute 若要回读 context 键,Go 禁止反向 import(环)。集成批次
需把 `IngressFrameMeta` 与键常量迁至共享低层包(或 processor/tcp
内——那不是「给 Metadata 加字段/扩枚举」,不违反本分支禁令)。

## §3 准入与门禁

| 凭证 | 管理方式 | 形态 | mesh 语义 |
|---|---|---|---|
| mesh 互连公钥 | 组网配 peers 时生成密钥对:私钥进对端出口配置,公钥进本列表(与 mesh 分支的集成缝:本分支只定义字段与准入逻辑,生成/分发 UI 归组网页) | 形态② | 完整元数据;受「允许被转发」门禁+ACL |
| 人用授权公钥 | 仪表盘粘贴公钥增删 | 形态① | 归自定义;mesh 再转发受「自定义仅 LAN」约束(与代理端口入站对称) |

门禁联动:形态②受 customer.mesh 的 `EffectiveForwardEnabled`
(「允许被其他节点转发流量」,默认关)门控——与 mesh Task 6 同款;
形态①只受 ssh_ingress 自身总开关门控(标准 ssh 客户端无法伪造
mesh 帧语义,归类已保守)。

**SSH 载体的类别准入裁定(接口契约,定死)**:SSH 载体按 **LAN 同款**
语义——全类别(自身/AI/自定义)可走,准入由 **A 侧拨出时按类别选择
载体**执行(SSH 无平台带宽关切,不同于 piko 中继);**B 侧不重复
裁类别**(与 mesh Task 6 对中继载体的 `RejectsCustomOnRelayCarrier`
不同)。A 侧拨出归 mesh 分支落地,此裁定即两分支的接口契约。

## §4 配置层

- 新增 `customer.ssh_ingress`:
  `{enable(默认 false), port(默认 56441), authorized_keys[](人用
  公钥), mesh_authorized_keys[](mesh 互连公钥)}`;
- `CustomerConfig` 加字段**追加在结构尾部**;可编辑面白名单
  (`validateCustomerEditableSurface` + customer_config_service 的
  editable 列表)加 `"ssh_ingress"` **追加在列表尾部**——与 mesh 分支
  的 MeshConfig 合并冲突收缩到相邻行;
- **独立于 ingress.mode**(不与 tun/http 互斥,附加服务面,不进
  CanonicalRoutingSchema 的 ingress 枚举);
- Validate:port 1..65535;enable 时至少声明一种凭证才可保存
  (开了门没人进=配置误导,拦截);authorized_keys 与
  mesh_authorized_keys 逐条过 `gossh.ParseAuthorizedKey` 校验
  (保存时拦,拒绝裸串入库);
- 门禁开关复用 PeerMCP 三态指针模式(`*bool`,nil 视为 false——
  服务面默认关,升级无行为回退诉求);
- **可编辑面同步清单(仓库铁律「三处 allowlist 同步」)**:除
  `validateCustomerEditableSurface` 白名单与 customer_config_service
  editable 列表外,还有 unknown-key switch(`extractCustomerUnknownFields`)
  与两处错误文案字段表(types.go 的 customerUnknownKeyErrors、
  customer_config_service.go 的 forbidden 文案)——四处全加
  `ssh_ingress`,漏文案处会把拒绝提示变成误导。

## §5 错误处理

| 场景 | 行为 |
|---|---|
| 总开关关 | 连接即拒;文案区分「ingress disabled」/「key not authorized」/「rate limited」 |
| 未知公钥 | 拒连+计入限速 |
| 形态② 防环命中(Via 含本机) | 拒绝+错误回写(与 mesh_inbound 同语义) |
| 形态② 门禁关/来源不在 ACL | 拒绝,文案区分 forward-disabled / not-allowed |
| 帧头解析失败(形态② 凭证) | 断开不兜底(节点凭证发不出合法帧=对端实现错误) |
| `dns:` 帧目标(形态②) | 拒连,错误行「dns target not supported on ssh carrier」(代解析归 mesh Task 8,合并后集成启用) |
| direct-tcpip 之外的 channel | Reject(安全收敛,恒定) |
| host key 文件损坏 | 重生成,仪表盘指纹更新提示 |
| 裸流注入后规则失败 | 走 B 既有失败策略(本地入站同路径,零新设计) |

## §6 测试

**镜像测试策略**:出口侧交付的 `sshtest` 内存 sshd 装置即服务端装置
先例;本功能服务端=内置 sshd,测试用**真 x/crypto 客户端**连它。

- 生命周期:Start/Stop/IsRunning;端口配置;开关关拒连(文案区分);
- host key:首启生成/重启稳定(指纹不变)/损坏重生成;
- 准入:人用公钥→形态①;mesh 公钥→形态②;未知公钥拒+限速
  (指数退避);密码不注册;
- 形态①:direct-tcpip→注入管线断言(参照 mesh Task 6 的
  `TestMeshInboundInjectsTLSConnIntoPipeline` 风格:发 TLS ClientHello,
  断言路由层收到 HostName);非 direct-tcpip channel 全拒;
- 形态②:帧头解析/防环(Via 含本机拒)/门禁关拒/ACL 拒/类别与
  IngressFrameMeta context 挂载断言(见 §2 接缝:断言 context 值
  挂载正确,非 Metadata 字段);帧头坏行断开;
- 安全收敛:shell/pty/-R/agent/X11 请求全拒(逐类型断言);
- 配置层:Validate(port/凭证至少一种)/可编辑面放行/尾部追加不扰
  既有字段;
- 与 mesh 分支并行策略:帧格式常量与解析**在本分支独立实现**
  (注释互指 mesh 分支同源定义),合并后统一为集成任务;集成测试
  (真双节点 SSH 载体链)归合并后的集成批次。

## §7 前端与 host key API

SSH 入口卡片(代理族下):

- 总开关+端口输入;
- host key 指纹展示+复制按钮(供客户端侧 known_hosts 核对);
- 人用授权公钥列表:粘贴添加/删除;
- mesh 互连公钥列表:只读展示+说明文案(生成动作归组网页,与
  mesh 分支的前端集成缝);
- i18n zh/en 按既有口径(「」、你、全角标点、上游代理)。

**host key API(双注册纪律)**:

- `GET /api/proxy/ssh-ingress/hostkey` →
  `{"fingerprint","algorithm","generated_at"}`(读
  ingress_host_key,不存在时触发生成并返回新指纹);
- 鉴权沿用代理配置端点同档(core 默认中间件栈;agent 侧
  loopbackOnly);**routes.go + registerAgentRoutes 双注册**
  (本仓反复踩的 404 坑);parity 正则已由出口任务扩围覆盖
  `/api/proxy`,新端点自动受守护;
- handler 落 `app/http/handlers/ssh_ingress_handler.go`(或并入
  ssh_known_hosts_handler.go 同族文件),host key 路径解析与
  inbound/ssh 同源(`cache.GetCacheSubdir("ssh")` 族)。

**端口默认定 `56441`(TCP)**:mesh plan Task 10 提案 beacon 用
UDP 56440,TCP/UDP 虽不硬撞,避开可省运维混淆;常量+可配。

## §8 冲突规避(worktree 策略,与组网分支并行)

- 本分支 `feat/ssh-ingress` 基于 master 当前 tip,**不动 mesh 分支
  领地**:`processor/tcp/mesh_inbound.go`、`app/mesh/`、
  `outbound/proxy/mesh/`、`app/tunnel/manager.go`;
- config 冲突面收缩:CustomerConfig 尾部加字段+白名单尾部加
  `ssh_ingress`——mesh 合并时为相邻行冲突,手并秒级;
- 帧格式/解析各自实现、注释互指;A 侧 mesh 出口的 SSH 载体拨出
  归 mesh 分支落地(本分支交付服务端+凭证字段=集成缝,设计文档
  即接口契约);
- 集成测试(真双节点 SSH 载体链)归合并后的集成批次,不在本分支。

## 明确不做(YAGNI)

- UDP/`-R` 远程转发/agent 转发/X11/shell(安全收敛+协议限制);
- 密码认证;PerLimit 细粒度限流(限速防爆破已含);
- ssh_config 级 cipher/kex 定制(x/crypto 默认);
- 与系统 sshd 的任何集成;
- 手机端界面。

## 影响面与兼容

- 纯增量:总开关默认关,存量用户零感知;不进 ingress.mode 枚举;
- 与出口 SSH 共享 `cache/ssh` 目录族(新增 ingress_host_key 文件),
  known_hosts(出口客户端用)互不干扰;
- 凭证字段为 optional;mesh 分支合并时 config 相邻行冲突手工并。
