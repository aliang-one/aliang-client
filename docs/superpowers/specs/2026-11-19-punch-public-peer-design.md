# punch 公网对端直连优化(feat/punch-public-peer)设计决策记录

## 0. 一句话

打洞链路识别「对端无 NAT(公网直连可达)」:B 侧自声明 + A 侧跳过自身 STUN 探测直拨——公网服务器对端从「白付全套打洞流程(信令+双侧 STUN,数秒)」变为「只付一次信令往返」,并把云 EIP 服务器与「STUN 全挂」两个场景一并救活。

## 1. 拍板决策(2026-11-19 用户确认)

1. **方案 = B 声明 + A 跳过自身 STUN**(机制安全依据:QUIC 客户端-服务端握手同五元组回包,A 发 Initial 即开自身 NAT 映射与过滤,B 应答必达——跳过 A 侧判型不影响拨通)。
2. **帧面新键**:`session.result` 增 Optional `public`(bool)——B 声明「本机候选可直拨」;契约同步=**三件套**(agent 契约表+契约测试 want;PhoneServer 中继 zod 白名单,见 2.4——漏 zod=键被静默剥除,评审坐实)。
3. **不做**:PhoneServer 候选打标(信令本身省不掉,收益重复);lan_addrs 公网手填(既有能力,文档提示即可)。

## 2. 关键设计裁决

### 2.1 B 侧:声明判据 = 自动检测 ∨ 显式配置

- **自动检测**:STUN 映射 IP ∈ 本机非回环 IPv4 接口集(复用 `collectAgentNetSnapshot`,2s TTL 缓存)→ 本机公网直挂。保守正确:**云 EIP 1:1 NAT(接口只见私网 IP)与家宽 DNAT(映射 IP=路由器公网)均不误判**——前者靠裁决 2 的显式配置覆盖,后者本就需要打洞。
- **显式配置**:`customer.mesh.public_listen`(bool,**默认关**,让渡型三态——声明可被公网直连=对公网暴露监听口,与 punch_enable 同族)。覆盖场景:**云 EIP 1:1 NAT(vm220/vm459 均云 VM,自动判据天然漏判)**;配置声明后**候选 STUN-独立**(classify 失败回落「首个非回环 IPv4 : 监听口」,接口快照)——顺带给「国内 STUN 单点→打洞全瘫」开了逃生门。
- 依赖门:`public_listen` 生效前提与 punch 同闸合取(ssh_ingress+mesh+punch;消费处与 `EffectivePunchEnabled` 合取,不单飞)。
- 声明出口:`session.result` 的 `public=true` + 候选照既有 `peer.punch.candidates` 帧发送(零新候选契约)。

### 2.2 A 侧:收到 `public=true` 跳过 classify+自候选发送(socket bind 保留)

- 依据:公网对端入站直连无需 A 侧开洞;A 自身公网映射仅服务于「B 探测辅助」(B 无 NAT 不需要)与「对称记账」(公网 B 无对称性概念)——跳过零损失。**socket bind 保留**:`punch.DialPeer` 必须持有 conn(dialer.go:103 fail-loud),且 socket.go 绑定铁律(TUN-on 5-tuple 正确性)不可绕;bind 的成本是接口解析(毫秒级),本优化省的是秒级 STUN 探测——跳过清单=**classify + 自候选发送**。
- 行为:dialMiss 在 result 解析到 `public=true` → 跳过「classify+自候选发送」(bind 照旧),直接进 candidates 等待 → `DialPeer`(预算不缩,成功近似必达,预算仅上限语义)。
- skipMemo:**不写**(classify 跳过无 Record);**预读照旧**(ShouldSkip 先于 session 帧发送,早于 public 可知——残留读取影响有界:陈旧 memo ≤5min TTL,退化落中继,评审裁定可接受)。
- 帧面:自候选发送跳过 → B 侧探测轮空转(日志一行,无害)。
- 兼容:老 B 无 `public` 键 → false → 照旧;新 A 全量行为分支有测试钉。

### 2.4 PhoneServer 中继 zod 白名单补登(评审重磅:含上批遗留缺陷修复)

中继 `server/src/modules/peer/punch.ts` 是 **zod 白名单剥除模式**(parse 后转发 `parsed.data`,未注册键静默剥除,~:61-68/:229):

- **上批遗留缺陷(本批一并修)**:`peerPunchSessionFrameSchema`(A→B session 帧)缺 `purpose`/`transfer_id`——生产中继会把传输腿会话剥成无 purpose 帧 → B 侧 purposeOrMesh 路由到 **mesh**(喂 sshin)→ 传输 hello 失配 → punch 腿静默退 COS。**agent 侧集成测试全绿是假象**(frame-sink 缝绕过了中继)——此缺陷由本批 spec 评审跨仓核查坐实。
- 本批新增:`peerPunchSessionResultFrameSchema`(B→A result 帧)补 `public` Optional。
- PhoneServer 测试:punch.ts 中继帧 schema 用例补三键(server/test/ 平铺新增,沿 peerLan.test.ts 形态);部署随批次出包(PhoneServer 未同步=public 静默失效+传输腿持续退 COS——**部署顺序硬依赖**,发布说明前置标注)。

### 2.5 UI 与配置链

- `MeshSettings` 卡加 `public_listen` 开关(形态沿 Task 4 已立的 punch 开关先例);`normalizeMeshConfig` 归一(三态 bool)+文件头注释;契约表 session.result 补 `public` Optional(契约表+want 两侧面,zod 见 2.4);status 回读不新增(public_listen 是配置项,走既有 config 面无 status 面)。
- `ValidateMeshDependency` 不动:`public_listen` 单独存在无意义(消费处与 punch 闸合取),保存期不硬拒(YAGNI;手改配置绕过由运行期合取兜住)。

## 3. 边界走查(定稿依据,压缩版)

多公网接口✅默认路由映射∈接口集 / 云 EIP 1:1 NAT ⚠️→显式配置覆盖(vm220 主受益场景)/ 家宽 DNAT ✅不误判 / B 侧 STUN 全挂 ⚠️→public_listen 回落接口快照救活(**精度注记**:该救活仅限接口真挂公网 IP 的机器;云 EIP 机接口快照是私网 IP,回落候选不可拨→退化落中继,安全降级勿过度承诺) / A 侧 NAT 四型(EIM/EDF/对称/无 NAT)✅同五元组握手全通 / A 无候选→B 探测空转 ✅无害 / 伪 public 声明 ✅降级安全(打不通落中继,B 为同账号设备) / skipMemo ✅不写,预读照旧(评审裁定) / 老 A↔新 B、新 A↔老 B ✅双向兼容 / 预算 ✅不缩(上限语义) / 双端握手窗 ✅不变(评审强制项沿袭) / IPv6 ✅不涉(udp4) / 集成测试拓扑 ⚠️loopback 映射=回环被接口集排除→**B 判据抽为纯函数 `isPublicMapping(mapping, snapshot)` 单测不依赖机器**,集成走 public_listen 配置。

## 3.1 术语口径(评审消歧)

「双侧/两侧补登」一律指:**agent 契约表(agent_protocol.go)+ 契约测试 want 映射 + PhoneServer zod 白名单(2.4)**——三件套缺一即静默失效。

## 4. 遗留账(并入 TODOS)

- 传输腿预热等既有八条不变;本批新增:无(公网对端的 lan_addrs 手填直拨已有能力,文档随发布说明提示)。
- MeshSettings `public_listen` 开关的 unconfigured 启发式不纳入(meshState 语义与直连声明无关)。

## 5. 实测情报存档

- 2026-11-18 本机家宽 STUN 探测:仅 stun.miwifi.com:3478 国内可达(google 主域机会性)——public_listen 的 STUN-独立候选是国内单点风险的直接逃生门。
- vm220(外地)= 云 VM;云 EIP 1:1 NAT 是否成立待装机后 `ip addr`/STUN 对照确认——若成立,本优化的真机收益以 vm220 为对端最大化。

## 6. 测试计划

- **单元(B 判据)**:自动检测三分支(映射∈接口集→声明/∉→不声明/接口快照为空→不声明);public_listen=true 时 classify 失败→接口快照候选;public_listen=false+classify 失败→不声明。
- **单元(A 行为)**:public=true→classify/自候选注入缝断言零调用(bind 照走)+DialPeer 照走;public 缺省→既有全流程零变化;public=true 但无候选→无候选错误落中继。
- **契约**:session.result Optional 补 `public` 三件套(agent 契约表+want 映射+PhoneServer zod)。
- **PhoneServer**:punch.ts 中继 schema 用例补 purpose/transfer_id/public 三键透传断言(白名单剥除回归钉)。
- **集成**:test/ 包沿 TransferLeg 拓扑,经 public_listen 配置使 B 判 public,断言 A 拨通+跳过探测;反向(不声明)照旧。
- **web**:public_listen 开关归一/透传/不产伪 diff(沿 punch_enable 用例形态)。
