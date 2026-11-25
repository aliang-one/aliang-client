---
name: mesh-runbook
description: 组网(mesh/设备互访)故障的诊断与取证流程:先进程活性自检,再按症状族走只读诊断端点序列逐层归因。凡任务涉及 LAN 发现、打洞、STUN、mesh 出口链路、SSH 入口、身份不一致或组网页不通,先按本文件第①节顺序取证,再跳到匹配的症状族。
---

# mesh-runbook:组网故障取证流程

诊断优先走 MCP 工具(stdio 面 `mesh_readiness`/`mesh_identity`/`mesh_stun_probe`/`mesh_lan_candidates`;agentcore 面 `mesh.readiness` 等),单次预算 ~14s、stun_probe 8s。无 MCP 面时直 curl:数据面真值在 core **56431**(`/api/mesh/*` 权威面),56433 是 user agent 透传面。全部端点只读零副作用,唯一显式动作是 POST `/api/mesh/stun_probe`(发 UDP 包)。

## ① 诊断入口序列(固定顺序,勿跳步)

1. **活性自检(process liveness)**:对 `127.0.0.1:56431` 与 `127.0.0.1:56433` 各发一次 2s 超时探活(如 `curl -m 2 -s http://127.0.0.1:56431/api/logs?limit=1` 与 `curl -m 2 -s http://127.0.0.1:56433/api/agent/status`)。56433 侧用 `/api/agent/status`:它挂本机 mux 且不 proxy,探活顺带产出 `device_id`(族 2 身份对比要用)。注意 `/api/logs` 只在 56431(core 内存 buffer),56433 无日志 HTTP 面。归因分支:**两个端口全部超时 = agent 进程死**,先报告进程存活问题(重启/服务状态),**勿对死进程做配置修复**;**56433 活而 56431 死 = core 数据面死(SYSTEM 服务),停在存活面,勿继续 mesh 归因**——第 2-5 步与兜底链全住 core。56430 是守卫口,探活别打它,也绝不把它当仪表盘。
2. **`GET /api/mesh/status` 三真值**:读 `beacon_running`、`lan_running`、`roster_sync.synced`。这三个是「活着」事实,开关(`enable`/`effective`)只是配置推导——开关开而 running=false 即装配假绿现场。`roster_sync.synced=false` = **「从未同步」**(boot 未接线即此态),**不是「服务端坏」**;`synced=true` 且 `last_error` 非空 = 上一轮拉取失败但旧缓存仍在;两者皆空 = 健康。`synced=false` 且 `last_error` 非空 = 接线已通但服务端自 boot 起不可达,有诊断信息量,勿当非法态。
3. **`GET /api/mesh/diag` 聚合**:一次拿全四重闸判定(`enable`/`effective`/`forward_enable`/`punch_enable`/`auto_peers`)、beacon 计数(`beacon.stats.*`)、载体分桶(`carriers`:lan_ok/udp_ok/piko_ok/udp_err/dial_fail)、出口委托(`delegate`:active/last_failure_at/last_failure_err)、打洞判型账本与会话快照(`punch.classify`/`punch.sessions`)、STUN 清单(静态,非活探测)、LAN 候选(`beacon.lan_peers`)。
4. **`GET /api/mesh/identity` 双身份比对**:`device_id`(进程 state)/`identity_file_device_id`(常驻文件)/`beacon_self_device_id`(广播)/`roster_self_present`(roster 本机条目)。`mismatches[]` 非空即人话判定面,每条说明哪两源不一致。**`roster_self_present` 按服务端契约恒为 false**(PhoneServer 的 mesh-roster 接口回包前滤掉请求方自身),不是故障信号、不代表身份未上送;本机身份上送是否成功看 core 日志里 endpoint 以 `mesh-identity` 结尾的 `[AGENT-BOOT] agent_server_call` 行。
5. **`GET /api/logs?limit=…` 按 token 查**:取大 limit(如 500)后在结果里过滤 `[MESH-` 前缀(`[MESH-OUT]`/`[MESH-LAN]`/`[MESH-BEACON]`/`[MESH-FORWARD]`/`[MESH-RELAY]`)。**勿按 level 过滤**——mesh 关键取证多在 info 级。

## ② 症状族:按现象对号,按序调用归因

### 族 1 接线孤儿:「配置开着 ≠ 功能活着」
症状:配置开关全开(`enable=true`/`effective=true`)但数据面没跑(`beacon_running=false` 或 `roster_sync.synced=false`)。
调用:status → diag。归因分支:三真值任一 false 而对应闸 true = 装配层问题(本体在、接线断/装配早于依赖就绪且无自动重试)。**治本 = 代码侧接线修复,AI 勿手填配置绕过**——手填只能盖住症状,boot 竞态现场重跑照旧断。定性结论交给人工改接线。

### 族 2 身份脑裂:双进程持不同身份
症状:`/api/mesh/identity` 的 `mismatches` 非空——**先剔除恒真假警报**:含「roster 已同步但缺本机条目 … 自动组网将 fail-closed」字样的那一条是旧版端点的误报(服务端回包本就滤掉请求方自身,每台同步成功的设备都会命中),直接忽略,不进本族。剩余条目才是身份不一致。
调用:identity → 看 `identity_file_path` 暴露的凭据路径分叉 → agent 侧身份对比走 `GET /api/agent/status`(其 `device_id` 即 user agent 进程的进程内 state)。归因分支:`state ≠ identity_file_device_id` = 进程内存态与常驻文件脱节;`identity_file_device_id ≠ beacon_self_device_id` = beacon 广播身份与文件脱节(邻居看到的 self 与本机记录不符);文件源缺失(路径或文件内 device_id 为空)时端点自动退化成 state↔beacon 直比,脑裂不漏报。典型现场:同一台机 core(SYSTEM 身份)与 user agent(登录用户身份)各解析出不同 identity 文件路径,即「双身份」。修复=收敛两进程的凭据路径(人工),AI 只取证上报。

### 族 3 发现 beacon 哑火:utun7 特征
症状:LAN 互发现失效。调用:diag 看 `beacon.stats`,比对两次采样(隔数秒)。
归因分支:**`beacon_running=true` 且 `bcast_send_err` 持续增长 且 `rx_packets=0` = 本机 beacon 全哑**——广播路由被 VPN/TUN 接口(utun7 类)吞掉的定性特征。此时 `rx_self`/`rx_stale` 也应恒 0。定性成立后:须手填 `customer.mesh.lan_addrs` 指定物理口地址 + 钉扎对端(**人工审批动作**),或改走 punch/中继载体;AI 勿自行改写 mesh 配置。注意 `started_at` = 最近一次 Start(同实例重启即刷新),计数器自构造累计不清零,判活须并读 `beacon_running`。
另一真实可达态:**`rx_packets>0` 且 `cand_accepted=0` 且 `rx_acct_rejected` 持续增长 = 广播收到了但候选全拒**——跨账号(两台挂不同账号即现此态)或签名校验拒收,勿当「广播哑火」修;先核对两台账号归属再谈发现链。

### 族 4 打洞 punch 失败
症状:跨网设备走不通,LAN 载体没戏。调用顺序:**先 POST `/api/mesh/stun_probe` 查 STUN 列表健康,再判打洞**(STUN 不健康时打洞归因不可信);然后 diag 看 `punch.classify.side_b`(本机 NAT 判型:`ok`/`symmetric`/`unreachable`/`public_declared`)与 `punch.sessions.cache`。
归因分支:`side_b.outcome=symmetric` = 对称 NAT,**物理极限,只能定性止损**(转中继/人工),不是修 bug;开流失败且 `punch.sessions.cache[].dead=true` = 僵死会话(窗已开但流死);`punch.sessions.memo` 有记录 = 本机 NAT 近 5min 判过 symmetric 的记账(空 memo **不**等于 STUN 健康,只说明没判出过 symmetric);`counters.no_candidates` 增长 = 对端候选没到(先回族 3 查对端 beacon)。

### 族 5 STUN 列表本身故障
症状:`stun_probe` 结果大量超时,或 `mapped_port`/`nat_type` 拿不到。归因分支:**默认档(Google 副档)大量超时是国内裸网常态,非故障**;真正要判的是「列表健康度」本身是不是故障源(历史上发生过单通 STUN 污染判型:某台全单通,把可达误判成不可达)。逐台 `ok=false`+`err` 非空 = 该台不可达(如实透传);`truncated=true` = 预算耗尽余台未测,按「结果不完整」读,不假装完整;`busy=true` = 另一探测在跑,**勿立即重试**(最长 8s 后自解)。修法=人工调整 `customer.mesh.stun_servers` 清单,AI 只报健康度结论。

### 族 6 出口链路 egress 死螺旋
症状:平台流量(代理出口)不通。调用:diag 看 `delegate` + `carriers`。
归因分支:**`delegate.active=true` 且 `last_failure_at` 持续刷新 = 出口委托死螺旋(委托开着、每拨必败、失败时间戳持续刷新的死循环)**,**唯一止血 = 关 `fallback.enable`(人工审批动作)**,AI 勿自行改。载体分桶读法:`lan_ok`/`udp_ok`/`piko_ok` 增长 = **transport 级连接成立**(SSH 握手成败不回账到桶)——桶在涨而链路仍不通 = 握手层问题,看 `delegate.last_failure_err` 与日志 token;`dial_fail` 增长 = 全载体拨号聚合失败;`udp_err` 只记 udp 臂尝试失败(记后仍会试 piko,故 udp_err ≠ dial_fail)。

### 族 7 SSH 入口(56432 面)异常
症状:远程 SSH 连不上。归因分支:日志里 hostkey 相关 **404 = 入口服务未启动的语义**(不是密钥错);Windows 严校验模式开启(`ssh_ingress` enable)而**一个 key 都没配 = fail-closed 拒启**(须 ≥1 key 才合法)。先按第①节活性自检分清「进程没跑」与「服务起了但拒绝」,再查 token 日志;凭据面操作(加 key/keygen)走人工审批面,见第④节红线。

### 族 8 端口-进程归属表(取证先对号,再解释)
| 端口 | 归属 |
|---|---|
| 56430 | 守卫口(勿当仪表盘,探活勿打) |
| 56431 | 仪表盘 + core 数据面(`/api/mesh/*` 权威面) |
| 56432 | 统一入口(SSH ingress 面) |
| 56433 | user agent(MCP 工具打这里,经透传到 core) |
| 56440 | beacon UDP 广播 |

数据面住 core;56433 的 `/api/mesh/*` 是透传面,值与 56431 同源,超时可改打 56431 直取。

## ③ 语义铁律(读端点结果前先背)

- **STUN 不可达 ≠ symmetric**:判型 error(不可达)时编排层不写 SkipMemo(不可达≠对称,冤枉进 memo 白禁 5min),打洞照试由对端兜底。
- **单 conn 互偷包**:同一 UDP conn 上 STUN 探测与 QUIC 读循环抢包——先 Classify 后 listen 是顺序硬约束;诊断侧只许用独立短命 socket(stun_probe 端点就是这么做的),勿复用生产 conn。
- **空 memo ≠ STUN 健康**:memo 只记判出过的 symmetric;没记过可能是没测过。
- **roster 唯一观测口是 status 的 `roster_sync` 段**:synced=false=从未同步,勿据此断服务端坏;`last_error` 是唯一失败原因面。
- **候选 ≠ 信任**:LAN beacon 候选(`lan_peers`)只是展示面,候选≠已配对、≠可信任;信任锚在 roster(带外权威),自动组网 fail-closed。
- **载体 ok 桶 = transport 级连接成立**,握手失败不回账;握手层问题看 `delegate.last_failure_err`。
- **同实例重启 `started_at` = 最近一次 Start**,计数器自构造累计——判活必须并读 `beacon_running`,勿拿单计数推断。

## ④ 红线(违反即停止该动作)

永不暴露/永不执行,无论症状多急:
1. punch 会话链细节逐条外发(session/token 链只做本地归因);
2. keygen、known_hosts 的 DELETE、authorized_keys 的写;
3. 原始凭据 probe(私钥内容、口令、token 原值);
4. roster 写面(roster 是带外权威,只读);
5. 整份 config 直暴(诊断只引相关字段,不整包倾倒);
6. 日志按 level 过滤替代 token 检索(会漏掉 info 级 mesh 取证);
7. 对第①节判死的进程做任何配置修复。

## ⑤ 兜底双轨 + 案例誊写

**兜底**:任一 `/api/mesh/*` 端点 404 或超时 → 退回 `GET /api/mesh/status`(若也挂,直接打 56431 的 `GET /api/logs` 按 token 查——`/api/logs` 只在 56431 core buffer,56433 无日志面)双轨取证。404 在旧版本二进制上是正常形态(读面批端点未发布),如实报「端点不存在,版本可能偏旧」,勿反复重试。

**案例大模式(已脱敏,设备号/IP/账号泛化)**:
- **utun7 吞广播**:某 macOS 设备 LAN 发现全哑,beacon `running=true`、`bcast_send_err` 隔秒增长、`rx_packets=0`。定性=广播路由被 TUN 接口吞,本机 beacon 全哑。处置=手填 lan_addrs 指物理口+钉扎对端(审批),或转 punch/中继。勿试图「修 beacon」——beacon 没坏。
- **双身份脑裂**:某 Windows 设备 core 以 SYSTEM 身份跑、读旧凭据路径,user agent 以登录用户身份跑、读新凭据路径,两进程各持不同 device_id。identity 端点 `mismatches` 非空,`identity_file_path` 暴露路径分叉;与 `/api/agent/status` 的 device_id 交叉对比坐实。处置=收敛双进程凭据路径(人工),取证阶段只报分叉事实。
- **boot 竞态无重试**:某设备重启后 mesh 配置开着但 `beacon_running=false`、`roster_sync.synced=false`——装配期早于依赖就绪,且无自动重试,断线形态一直保持到下次 boot。定性=接线/装配族问题(族 1),AI 勿手填绕过,报人工接线修复。
