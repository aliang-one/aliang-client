# 文件传输 punch 直连腿 + STUN 可配置化(feat/transfer-punch-stun)设计决策记录

## 0. 一句话

让 peer 文件传输在 LAN 直推腿不通(跨网段)时走 UDP 打洞直连(失败落 COS),并把打洞的 STUN 服务器从硬编码 Google 两台换成可配置、默认国内可达档——使 vm220(外地)↔liang-dev(172.16.222.x)这类跨网场景的文件传输与组网转发真正获得直连能力。

## 1. 拍板决策(2026-11-18 用户确认)

1. **STUN 默认档换国内可达**:默认 `[stun.miwifi.com:3478, stun.l.google.com:19302]`;`customer.mesh.stun_servers` 可配置覆盖;自托管 coturn 部署后填首位(部署手册属 ops,不在本批装)。
2. **传输直推腿载体顺序:LAN → punch → COS**;启用条件维持三闸合取不变(双端 `ssh_ingress.enable` + `mesh.enable` + `punch_enable`)。
3. **PhoneServer「server 双端可达」三利用**:①信令中继(存量,punch 三帧已走);②自托管 STUN(ops 侧,同出口部署后可达性与控制面同源);③预热时序协调(计划期修订:被预算吸收替代,入遗留账)。
4. **真机实测为部署后验收阶段**,不单独出码;顺带补 mesh 转发面打洞率真机数据(TODOS ① 二期闸门)。

## 2. 关键设计裁决

### 2.1 STUN 可配置化

- `MeshConfig` 加 `StunServers []string`(json `stun_servers,omitempty`,types.go MeshConfig):逐条 TrimSpace 剔空行、上限 4 条、`host:port` 形态校验(`net.SplitHostPort`);非法整条拒(保存通道,沿 ValidateMeshDependency 同款只挂保存面)。
- **空 = 默认档** `[stun.miwifi.com:3478, stun.l.google.com:19302]`。实测依据见 §5:miwifi 国内家宽 binding 成功(111.206.174.2);qq/cloudflare/bilibili 全超时;Google 本机经隧道通,外地裸网预期不通。默认档语义=miwifi 是国内主档,Google 机会性副档。
- 生产消费点改造极小:`agentPeerPunchB.servers` 注入缝已存在(`app/http/services/agent_peer_punch.go:136`),A 侧判型同源;生产实现从 `punch.DefaultSTUNServers()` 换成「读配置,空回落默认档」。默认档常量 `MeshDefaultStunServers` 住 `processor/config/types.go`(计划期修订:分层上 processor 不得反向依赖 app,`punch.DefaultSTUNServers` 删除)。
- **判型零新逻辑**:沿用现有 `punch.Classify` 行为——某台不可达即该台 `(mapping, ok=false)`,双台比对凑不齐就不记账 memo、不阻断打洞;**不引入按列表台数跳过的剪枝**(STUN 健康探测/择优已留 §4 二期)。评审确认既有行为天然覆盖默认档国内单通形态。
- UI:`MeshSettings` 卡新增 STUN 列表多行输入;顺手收敛 TODOS ⑤(punch_enable 无 UI 开关致 unconfigured 启发式不可达)——本批一并把 punch_enable 开关补进卡片。
- 配置链口径:STUN 配置属**设备本地配置**(agent 仪表盘 → 本地 HTTP API → config.json),不经平台 customer allowlist;规划期核对平台 push 链无字段拦截即可。

### 2.2 文件传输 punch 直连腿

- **载体顺序**:LAN 直推 → punch 腿 → COS;任一环失败(或门禁不通)落下一环,全失败沿现 COS 常规链路。
- **角色分工**:发送方 = punch A(dialer,信令发起方),接收方 = punch B(listener + serve lan_push 协议)。
- **会话目的路由**:`peer.punch.session` 帧加可选 `purpose` 字段(缺省/`"mesh"` = 现语义,向后兼容;`"transfer"` 带 `transfer_id`)。B 侧按 purpose 把过门流路由到对应 handler:`agentPeerPunchB.handler` 已是按会话注入缝(`agent_peer_punch.go:140`),扩展为按会话注册的路由表(mesh 会话→sshin 现路径;transfer 会话→lan_push 协议体)。传输路由的 WS writeJSON 取 **listen 帧时刻的 writer 捕获**(断连后进度帧静默丢,非致命;result 真值走流内 ACK 与上传链 HTTP 双通道)。
- **凭据**:token 复用 lan_push 单次传输 token(单次有效,空 token 门禁/拒因三分 punchGateErrorCode 全照旧);TLS 用 punch 现签证书 + A钉B 指纹钉扎 fail-closed(mTLS v1 降级模型,B 侧只要求证书+token 门禁)。
- **协议层零新设计**:打通的 punch 流(`punchStreamConn`)就是干净 net.Conn,lan_push 协议(hello/token/size_bytes/ACK 行)原样跑。实施形:把 `agentLanPushServeOnce` 的 TLS 包装拆成内外两层——TCP 路径包 tls.Server 后进协议体;punch 路径(punch 流已自带 TLS)直接进协议体,协议体函数共享。
- **拨号预算吸收**(计划期修订,原「预热」条):传输腿拨号预算 `agentTransferPunchBudget=12s`,覆盖 B 侧 bind+双 STUN 判型最坏 ~6s + 正常拨号;预热(接收方并行 punch 会话)移入遗留账——收益仅同对端冷缓存首传,SessionCache 复用吸收后续。诚实代价:STUN 全死最坏路径落 COS 前多等 ≤17.5s(信令围栏 10s+5s+拨号 2.5s,均既有常量)。
- **A 侧拨号**:按目标 device_id 直拨(传输腿不依赖 `mesh.peers` 条目;信令走 PhoneServer `target_device_id` 信封,中继已部署);**目标 device_id 由 PhoneServer upload 帧顶层可选键 `punch_peer_id` 下发**(计划期实锤:upload 帧原不带目标身份;PhoneServer 侧一并改,老 agent 零感知);cache-first 通道与 `AgentPeerPunchDial` 同源,**会话缓存/sock 表键带用途前缀(`transfer:<peerID>`)**——mesh 与传输会话互不交叉(计划期实锤:同 peer 混缓存会喂错 handler)。
- **预算/兜底**:`DialBudget` 2.5s 照旧;预算耗尽/门禁拒单落 COS。日志 `[AGENT-TRANSFER-PUNCH] via carrier=lan|punch|cos` 标记每条传输的最终载体。
- **收摊**:传输会话沿 listener TTL 收摊现例;同 transfer_id 重发=**新 punch session_id**(旧会话靠 TTL 自然过期,文件被服务两次无害;close 帧系 TODOS ⑦,不在本批——实施计划须显式确认该语义)。

### 2.3 真机实测(部署后验收阶段)

- vm220 ↔ liang-dev:三闸开齐(vm220 无本地 config.json,配置经仪表盘/DB 快照链)、STUN 默认档可达验证、文件传输观察 `[AGENT-TRANSFER-PUNCH] via carrier=` 落哪条腿。
- 顺带收 mesh 转发面打洞率真机数据(`[MESH-OUT] via carrier=`)——TODOS ① 二期闸门数据一并落账。
- coturn 自托管:出部署手册(集群 UDP 端口暴露面),不给码。

## 3. 诚实预期(验收口径)

- punch 成功率锚 70%±7.1%(ACM IMC 2025 学术基线);对称 NAT / 蜂窝 CGNAT 必败落 COS——打洞失败不是错误态。
- 默认档国内形态**判型 memo 恒缺失**(只有 miwifi 单通):要准确 NAT 判型请自托管 coturn 填双台;不影响打洞本身。
- 传输 punch 腿受益面 = **声明大小的单文件直推**;zip 流式归档(总量未知)仍恒落 COS(与现 lan_push 腿同款语义,`attemptAgentTransferLANPush` 开头 skip 分支)。
- PhoneServer 只能观测设备 WS 的 TCP 源,**拿不到 UDP 映射端口**——「server 直接下发候选」不可行,公网映射必须真 STUN(UDP)探测;这是本批必须动 STUN 配置的根本原因。计划期细化:server 下发的是**寻址 ID**(`punch_peer_id`,PhoneServer 一并改),不是 UDP 候选——该边界不变。
- 接收方常驻 UDP socket 仅在 punch 会话窗内存在(会话 TTL 口径,与 mesh punch 一致):让渡面不变,由三闸门禁约束,不新增暴露面;传输腿拨号预算加长只影响发起方等待时长,不改变资源占用面。

## 4. 遗留账(并入 TODOS.md)

- ⑦ close 帧(同 session_id 顶替语义的显式关闭)——延续不在本批。
- 诊断面板传输腿 carrier 统计(④ 扩展:按 transfer 维度的 lan/punch/cos 分桶)。
- coturn 自托管部署手册落 docs(§3)。
- 传输腿预热(接收方并行 punch 会话)——收益仅同对端冷缓存首传,被拨号预算吸收(`agentTransferPunchBudget=12s`)替代;二期看真机数据再议。
- STUN 健康探测/多档 fallback(列表内逐台探测择优)——本批固定「首台可达即用」简单语义,启发式留二期。

## 5. 实测情报存档(真机冒烟前置)

- **2026-11-18 本机家宽 STUN 探测**(真实 STUN Binding,2s 超时 ×2 重试):`stun.miwifi.com:3478` OK(111.206.174.2,try1);`stun.qq.com` timeout×2;`stun.chat.bilibili.com` timeout;`stun.cloudflare.com` timeout;`stun.counterpath.com` timeout;`stun.sipgate.net` timeout;`stun.syncthing.net` DNS fail;`stun.l.google.com` OK(本机经隧道,外地裸网预期不通);`stun.hit.edu.cn` DNS 不存在。
- **punch_enable 现有同步链**(grep 全仓,`stun_servers` 新字段照此链逐一核对):`processor/config/types.go`×2、`processor/tcp/handler.go`×1、`app/http/handlers/mesh_handler.go`×2、`app/http/services/agent_peer_punch.go`×1、`app/website/src/components/settings/MeshSettings.js`×4、`MeshSettings.test.js`×13、`PlatformProxySettings.test.js`×2、`SettingsPage.vue`×1、`app/website/tests/e2e/mesh-settings.spec.js`×5。
- **两端配置现状(2026-11-18 实测)**:liang-dev `~/.aliang/config.json` 已开 mesh+ssh_ingress(00:23,备份 `config.json.bak-mesh-20261118`),缺 punch_enable;**vm220 在册(agent 1.1.116,online;原 vm459 已下线退役,同形态重装)**——`C:\Program Files\Aliang\aliang.exe` 服务 Running,user profile 与 ProgramData 均无本地 config.json(配置在 DB 快照),mesh/ssh_ingress/punch 全缺;本机 mac mesh/ssh_ingress 全 null。liang(Win25H2)当日 offline,非本批测试对象。
- **前置规格**:`docs/superpowers/specs/2026-11-12-mesh-udp-punch-design.md`(打洞一期:载体六族/信令三帧/门禁三闸/2.5s 预算语义)。

## 6. 测试计划

- **单元(types)**:`stun_servers` 校验(TrimSpace/剔空/上限 4/host:port 非法整条拒)、空回落默认档、`EffectiveStunServers` 读出;Classify 对不可达台的行为断言(凑不齐双台→无 memo 不阻断,守住既有行为不回归)。
- **单元(punch 传输腿)**:session 帧 purpose 路由(缺省=mesh 向后兼容/transfer=transfer_id 校验);协议体拆层后 TCP 路径与 punch 路径行为等价;载体顺序编排(LAN 成功不进 punch/LAN 失败进 punch/punch 失败落 COS/三闸不全直接 COS)。
- **集成**:`test/punch_integration_test.go` 扩传输腿用例(loopback 拓扑,信令桥 sink 注入):发送方 LAN 不通 → punch 打通 → 接收方收满落盘 ACK;信令晚到候选在预算内纳入(既有 DialPeer 逐轮取数语义)。
- **web**:MeshSettings stun_servers 输入 + punch_enable 开关;e2e mesh-settings.spec.js 扩断言。
