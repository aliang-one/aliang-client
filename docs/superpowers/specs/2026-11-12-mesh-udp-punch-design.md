# mesh UDP 打洞载体(feet/mesh-udp-punch)设计决策记录

- 日期:2026-11-12 评审 → 2026-11-13 实施
- 状态:已实施(feat/mesh-udp-punch 分支);真机冒烟与两类真实网络打洞率实测待做
- 前置:2026-11-11 组网零打洞审计(11-agent 工作流,5 子系统零反驳)+ 同日 Tailscale 四路线评审(D 正解 22/25)
- 实施计划:`docs/superpowers/plans/2026-11-12-mesh-udp-punch.md`(15 任务)

## 0. 一句话

> mesh 第三载体 `udp`:STUN 地址发现 + 平台 WS 信令候选交换 + QUIC-over-punched-socket 直连;LAN 失败后先试打洞、失败恒落 piko(piko 即本仓现成 DERP 角色)。

## 1. 拍板决策(2026-11-12 用户确认)

| 决策 | 内容 | 依据 |
|---|---|---|
| 准入类别 | **全类别**(self/ai/custom) | SPEC:560 对未来 UDP 载体的预留语义;punch 是直连非共享中继,custom 无「禁公共中继」约束 |
| 交付范围 | **全栈**(alianggate+PhoneServer) | 真机端到端验收需要 |
| 载体会话缓存 | **允许**(QUIC 会话跨 SSH 拨号复用) | 与 SPEC §5.4 禁的「SSH client 连接池」是两层物:缓存的是载体,每条 SSH 拨号仍独立握手独立 gossh.Client(session.go 文件头显式辨析) |
| 拨号预算 | 2.5s fail-fast 落 piko | 对称 NAT 对端不白烧预算 |

## 2. 关键设计裁决(实施期落地)

1. **物理接口绑定是 TUN 逃逸的唯一防线**(评审从「风险」升格「致命」):进程无天然逃逸路径——TUN-on 时引擎捕获后换 socket 换端口重发,STUN 应答描述的是引擎 socket 的映射,洞打错 5-tuple。punch socket 三平台出厂即绑(darwin IP_BOUND_IF/linux SO_BINDTODEVICE 需 CAP_NET_RAW/windows IP_UNICAST_IF),**绑定失败=载体自禁落 piko,绝不静默裸奔**(socket.go)。
2. **传输框架 quic-go 单选**(评审纠正):yamux 跑不了裸 UDP;Go 无 stdlib DTLS。QUIC Initial 重试即探测(A 侧 DialPeer 300ms 轮;B 侧 candidates 到达后起火即忘探测轮,双方 Initial 空中交叉互开映射)。
3. **手工 STUN 事务弃用 pion Client**(10a 实证):Client 的常驻读 goroutine 会偷走打洞 socket 后续报文,且裸 Write 不适配绑定 socket——手工驱动(事务 ID 匹配+来源过滤+XOR→MAPPED 回落)。
4. **单 Transport 铁律**:同 UDP conn 只容一个 quic.Transport(多实例互偷读循环);A 侧 DialPeer 一 Transport 多轮 Dial;B 侧 listener+探测共用;services 串行化 miss 重拨。
5. **B 侧 mTLS v1 降级**(评审 ACCEPTED_V1):线协议 session 帧不带 A 指纹→B 只要求出示证书不钉扎,信任=token 首流门禁(一次性经云端下发)+A 钉 B fail-closed 原样。**严格强于 peer.lan 已接受模型**(其 B 侧纯 token)。二期扩契约携带 A 指纹即恢复双向。
6. **target_device_id 路由信封**(Task 13 跨仓缺口):session 帧契约无目标设备字段,server 无从寻址 B——PhoneServer 按信封必填、转发前剥除(B 收到的帧与 Task 1 契约逐字一致);A 侧发送面补该字段(b048ab3)。
7. **CacheKey punch 位派生式置位**(Task 12 评审认可):`Punch = (punchDial != nil)` 同源表达式,位⟺udp 实际进切片在工厂与装配两侧同时成立,翻 `punch_enable` 运行期即时生效(缓存键变→重建)。

## 3. 诚实预期(验收口径)

- **打洞率锚学术基线 70%±7.1%**(ACM IMC 2025,libp2p DCUtR 大样本「条件于 relay 预订+地址发现均成功」的纯打洞阶段成功率),**不是** Tailscale 自报 90%+(端口预测/生日攻击全套)。v1 不做端口预测;对称 NAT×CGNAT 组合基本必败走中继。
- **验收按网络族分桶**(家宽非对称/蜂窝 CGNAT/企业对称),单一办公网数据不算数。一期闸门:两类真实网络实测拿真数据,再决定二期(coturn 自托管/端口预测)。
- **iPhone 全路线不进数据面**(2026-10-29 spec §1.1「手机永久只做控制面」):产品口径=「桌面/服务器 agent 间获得 Tailscale 的连接性(允许时直连、不允许时无缝中继),手机恒走云端控制面」——发布说明必须前置,否则实施完会被判不及格。
- **失败不是事故**:兜底恒在线,打洞失败只伤延迟带宽不伤连通性;诊断面载体可见性已落([MESH-OUT] conn_id=… via carrier=…,udp 失败附原因)。
- 一次性系统弹窗(macOS 本地网络权限/Windows 防火墙)被拒→UDP 静默失败恒走 piko;诊断面板暴露 UDP 可用性属后续批次,本期以日志行为准。

## 4. 遗留账(并入 TODOS.md)

- 端口预测/生日攻击、coturn 自托管:二期看真机打洞率数据再定
- IPv6 打洞(v1 udp4;候选解析 To4 断言与 agent_peer_lan 对齐)
- advertise_ip 收下不消费(LAN 提示作候选源)
- 诊断面板 UDP 可用性/载体统计 UI
- MeshSettings.vue:521 unconfigured 启发式未考虑 punch_enable(现不可达:punch 无 UI 开关)
- PhoneServer punch.ts 两条 NIT:result 查表无 TTL 过期检查(无害)、audit 元数据缺 from/to 对端 ID
- v1 无 close 帧:同 session_id 重发=顶替收摊

## 5. 实测情报存档(真机冒烟前置)

- B 侧 bind+双 STUN 判型最坏 ~6s 才回 result(A 侧 result 等待 10s 覆盖)
- B 侧 TTL 收摊是**静默死亡**(quic-go Transport.Close 不发 CONNECTION_CLOSE):A 侧 ~30s 空闲超时才可观测,僵尸窗口由拨号 deadline 兜底
- 信令无响应→软失败落 piko(设计天然安全),但「以为在打洞其实在走中继」需载体日志辨别(已落)
- Go 侧测试注入缝:SetMeshPunchDialSource(nil) 复位/TransportNames()/DialTrace{Used,UDPErr}
