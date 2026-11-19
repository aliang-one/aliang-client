# 组网诊断读面+MCP 聚合工具+runbook 技能(阶段 1)设计

日期:2026-11-19(夜间自主批)
分支:`feat/mesh-diag-tools`(基线 = master 3a3388ec,阶段 0 已合)
前序:阶段 0 地基批(2026-11-19,已合 master)+ 可行性评估共识(用户已批准的排序:读面聚合工具 3-4 枚+runbook 技能,验收线=历史故障回放 AI ≤5 次工具调用正确归因)
定位:AI 组网面阶段 1——只读诊断读面 + MCP 聚合工具 + runbook 技能。**零写路径、零高危暴露**(永不暴露清单继续有效:punch 会话链/传输上下文单次消费原语/keygen/known_hosts DELETE/authorized_keys 写/原始凭据参数 probe/roster 写面/整份 config)。

## 0. 拍板记录(用户离席,按既有共识保守拍板,晨审重点)

| # | 问题 | 拍板 | 理由 |
|---|---|---|---|
| D-1 | LLM 注册表第三面(agent_tool_registry.go,云端编排 WS 链)是否纳入 | **不纳入**,本期只上 stdio + agentcore 两面 | mesh 读数住 core 进程本地真值,与云端编排链不同链;PeerMCPContractSpecs(agent_peer_mcp.go:567-587)刻意只钉两面。**注意(评审实锤):peer 五件套在第三面有完整注册先例(rev4/rev5)——mesh 暂缓第三面系读面价值排序,而非形态不可行**;将来补第三面成本不高(rev 漂移重拉机制现成),残余账 |
| D-2 | 新工具组级门控键(customer.mesh_diag 三态) | **v1 不加新组键**,工具裸进 stdio/agentcore 表(读面天然默认开、免审) | ①loopback-only+纯读+无凭据暴露,信任级=既有 /api/mesh/* 免审读端点;②新 config 键=allowlist 5 处逐字同步的真实成本与出错面;③评估原文「读默认开」。残余账:若未来出现噪音再收编组级三态键(照 PeerMCPConfig 模板) |
| D-3 | STUN probe HTTP 形态 | **POST /api/mesh/stun_probe 零请求体**(照 HandleSSHTest POST-only 先例,显式动作语义正确;stdio 工具 method=POST 免 RawQuery 特判) | GET 带副作用语义错;带参需 invokeTool 特判 |
| D-4 | STUN probe 进程归属 | **core 侧执行**,56433 经既有 forward 透传 | 数据面真值住 core 原则(反向假红原则);物理口绑定在 core 数据面进程才有意义。预算链三层对齐:probe 内部硬顶 8s → forward 10s → tool 14s |
| D-5 | 载体分桶记账落点 | **outbound/proxy/mesh 包级 atomic**(DialContextTraced 内成功点/UDPErr 点),全局维度 v1 | DialTrace「刻意无跨连接状态」定谳不可违反(trace struct 本身不动);ExitClient 实例频繁重建,包级计数不随实例蒸发;peer 维度留二期 |
| D-6 | egress 委托「激活态」语义 | **配置判定现值**(meshDelegateActive)+ 末次失败 atomic 记账(platformDialContext 两失败分支),不记流量经过 | 与「读配置派生 vs 运行态真值」原则的边界:激活态=生效门判定,末次失败才是运行态 |
| D-7 | identity 端点源清单 | core 进程侧:**6 字段**{device_id(=进程 state 值,不设 state_device_id 别名键——T6 实现拍板,别名键无信息量正式合并), identity_file_path, identity_file_device_id, beacon_self_device_id, roster_self_present, mismatches[]};agent(56433)侧对比不进本端点(透传只能报 core 侧) | core 是 mesh 数据面身份(beacon 住 core);identity_file_path 本身暴露「走的哪条凭据路径」=脑裂诊断核心价值;agent 侧对比=残余账+runbook 双轨写(/api/agent/status) |
| D-8 | beacon 计数语义 | 暴露 sent/err 两原始计数(消费方算比率)+ 实例 started_at;bcastErrLogged 一次性日志行为**不动**(只加计数) | 广播是周期节拍非事件驱动;门控语义已评审定谳 |
| D-9 | runbook 落地形态 | **go:embed 内嵌源真值(app/assets/skills/mesh-runbook/SKILL.md)+ 启动自愈安装 ~/.claude/skills/mesh-runbook/**(对齐 peer MCP 托管注册先例:managed 标记+内容寻址幂等跳写+绝不碰用户自装技能);尊重 customer.agentcore.skills_enabled 总闸(关=不装),默认开 | 技能=磁盘文件制每 run 现读,装好下一 run 即生效;舰队多设备漏装=断头,自愈安装是唯一到达全舰队的形态;仓内先例齐备(embed/写用户内核侧文件) |
| D-10 | 三案例誊写脱敏 | 语义保留、设备号/内网 IP/账号泛化(vm220→「Windows VM A」等) | 记忆文件含敏感细节且不随二进制分发;泛化保留诊断语义 |
| D-11 | 「AI ≤5 次调用归因」验收线的自动化口径 | 仓内验收=三案例**数据特征在位测试**(roster 未同步态可见/身份 mismatch 可见/广播哑证据可见)+runbook 引用端点齐备;完整 AI 会话回放验收需真机人工/agent 会话实测,留待装机 | LLM 行为无法单测;诚实记账不假装自动化 |

## 1. 背景

阶段 0 铺了真值地基(三字段/双注册/日志收编/接线审计)。阶段 1 把组网诊断变成 AI 可直接消费的聚合读面:历史故障(四重闸审计/utun7 吞广播/双身份脑裂/egress 死螺旋/STUN 硬编码)的人工取证是会话级成本,聚合读端点+MCP 工具+runbook 把它压成分钟级。

## 2. 范围

**做**:三个新只读/显式探测端点(diag/identity/stun_probe)+ 七个数据源运行态补账(beacon 计数/SkipMemo 快照+classify 结果账本/会话快照/载体分桶/egress 记账)+ MCP 工具 4 枚(stdio+agentcore 两面)+ runbook 技能(embed+自愈安装)。
**不做**:LLM 注册表第三面(D-1,残余账:peer 有三面先例,补齐成本不高)/写面/阶段 3 审批制写原语/peer 维度分桶/agent 侧身份对比端点/组级门控键(D-2 残余账,**隐式耦合记账:mesh 工具裸进两面后继承 customer.peer_mcp 总闸与三子键——关 peer_mcp 的用户连带失去 mesh 读工具**,晨审知情即可)。
**阶段 1 残余账(随批入 TODOS)**:①D-1 第三面补齐;②D-2 组级三态键;③agent 侧身份对比(56433 进程身份 vs core);④peer 维度载体分桶;⑤runbook 安装器「运行期开闸→下一进程周期生效」语义的用户文档;⑥完整 AI 会话回放验收(真机)。

## 3. 子项设计

### 3.1 beacon 运行计数(lanbeacon/beacon.go)

Beacon 结构体加 atomic 计数组:bcastSent/bcastSendErr/rxPackets/rxMalformed/rxSelf/rxStale/rxRejectedAccount/rxRejectedSig/rxDegradeBlocked/candAccepted/candRefreshed/candEvicted。落点:broadcastOnce 失败分支(:469)/成功分支(:474);readLoop 收包(:483)/畸形(:486);ingest 七个 reject 分支+接受/刷新/挤逐。计数生命周期=当前实例(对账 Stop→re-Start 清零),Stats() BeaconStats 访问器带 started_at。bcastErrLogged 门控保持原语义。

### 3.2 punch 侧快照(stun.go/session.go/agent_peer_punch.go)

- SkipMemo.Snapshot():[{peer_id, until_unix, remaining_seconds, expired bool}],纯读零副作用(过期按 remaining<=0 标注,惰性清理仍归 Record/ShouldSkip)。
- **classify 结果账本(新增状态)**:A/B 两侧 per-peer outcome∈{ok, symmetric, unreachable, public_declared}+时刻。落点:A dialMiss classify 块(:950-965)、B handleSession(:367-378)。这是「STUN 不可达≠symmetric 不记账」在快照里的唯一表达——只快照 memo 表无法区分「不可达」与「从未探测」。A/B 分开报并标注键语义(side=a 键=对端 device_id / side=b 键=本机 device_id=「本机 NAT」)。
- SessionCache:cachedSession 加 createdAt;Snapshot() 每键 {key, refs, idle_seconds, dead(punchSessionDead 纯 select 非阻塞), age_seconds};**纯读**——绝不调 reapIdleLocked/close/delete。A 侧快照含 cache 表+sockets 表(purpose 拆解:裸 peerID=mesh/"transfer:" 前缀=传输腿)+ hit/miss/fail 分类计数(hit :842/miss :830/fail 分类点 :884/:917/:960/:1007/:1044/:1079);B 侧在册数+每会话 age+done。
- services 聚合访问器:MeshPunchDiagnosticsSnapshot()(A/B memo+账本+cache+sockets 合并输出);**锁序约束:快照只读表所在锁(mu),绝不触 dialMu 执行路径**;handlers 不 import punch(状态经 services 快照 struct,MeshRosterSyncSnapshot 先例)。

### 3.3 载体分桶+egress 记账(outbound/proxy/mesh + processor/egress)

- mesh 包级 atomic:lan_ok/udp_ok/piko_ok/udp_err/dial_fail 五计数,落点 DialContextTraced 成功点(trace.Used 确定后 per-carrier)/udp 失败点(UDPErr)/聚合失败;Snapshot() 访问器。DialTrace struct 本身不动(定谳)。
- egress:delegate 激活态(meshDelegateActive 现值)+ delegate_last_failure{at, err}(atomic 记账,platformDialContext 两失败分支)。快照访问器。

### 3.4 三个新端点(handlers 同域 MeshHandler 形态)

- **GET /api/mesh/diag**:`meshDiagResponse` = 四重闸逐一判定(enable/Effective/forward/punch/auto_peers+roster 新鲜度三态复用)+ beacon Stats + 载体分桶 + egress 委托态+末次失败 + punch 诊断快照(memo+账本+会话) + STUN 列表静态(现值,不活探测)+ lan_peers 候选。全部 snake_case tag、恒数组不 null、func() 注入缝、nil 零值安全。**零副作用**(STUN 活探测不在内,输出带「上次探测结果」来自 classify 账本)。
- **GET /api/mesh/identity**:D-7 清单 + mismatches[](state vs identity 文件 vs beacon self vs roster self 四源比对)。读 identity 文件走零副作用入口(loadAgentDeviceIdentity 同款只读;禁 CurrentDeviceID——它会生成)。
- **POST /api/mesh/stun_probe**:HandleSSHTest 骨架(400 早退/无状态/分层结论)+ TryLock busy-early-return(独立包级锁,不与 peerExecMu 共锁——毫秒级 vs 秒级共锁互挡)+ 每请求 punch.NewBoundUDPConn 新建短命 socket→Classify→defer Close(**绝不复用在途 conn**——互偷包铁律三处出处);预算:每台 classifyDiscoverTimeout=3s,总硬顶 8s。**强制机制(评审 Issue 2 拍定):Classify/Discover 不收 ctx,串行逐台——probe 侧逐台循环内检查 ctx 截断(每台启动前查剩余预算,预算耗尽即停,已测台数如实报出+truncated 标志)**;用户配满 4 台全不可达最坏 12s>8s:截断后输出带 truncated 语境(「部分台未测,或超 forward 10s 走 503」如实报出,不假装完整)。busy=200 信封 data{busy:true}。输出强制带默认档语境(默认档大量超时是常态非故障)。
- 注册:core routes.go register 三行 + server.go meshForward 三行(透传防反向假红);parity 正则已含 api/mesh 自动覆盖。

### 3.5 MCP 工具 4 枚(stdio + agentcore 两面,D-1/D-2)

| 工具 | method/path | 语义 |
|---|---|---|
| mesh_readiness | GET /api/mesh/diag | 聚合四重闸+运行态快照;description 内嵌:「幂等只读/超时 14s/零副作用/STUN 结果为上次探测非实时」 |
| mesh_identity | GET /api/mesh/identity | 身份一致性四源比对;脑裂定位入口 |
| mesh_stun_probe | POST /api/mesh/stun_probe | 显式活探测;description:「非幂等(发 UDP 包)/busy 可能(结构化返回,勿立即重试)/硬顶 8s」 |
| mesh_lan_candidates | GET /api/mesh/lan_peers | 包既有端点(零新代码) |

接线触点:①cmd/mcp.go peerMCPTools 表尾 4 条(device_list 零参 GET 形态/stun_probe POST 形态,timeoutMs 14_000);②agentcore 桥 agent_peer_tools.go 同语义 4 条;③PeerMCPContractSpecs 双面条目;④core/56433 注册已在 3.4。**LLM 注册表不动(D-1)、不加组键(D-2)**。description 措辞选三面保得住的稳定词(busy/idempotent/timeout)。

### 3.6 runbook 技能(D-9/D-10/D-11)

- 源真值:`app/assets/skills/mesh-runbook/SKILL.md`(go:embed)。frontmatter:name: mesh-runbook / description ≤2 句(渐进披露触发依据)。
- 自愈安装器:启动时(与 peer MCP 注册自愈同位)把内嵌 SKILL.md 写 ~/.claude/skills/mesh-runbook/,内容寻址(未变幂等跳写)、managed 标记、绝不碰用户自装技能;skills_enabled 总闸关=跳过;默认开。
- 正文分节(渐进披露):①诊断入口序列(活性自检→status 三真值→diag 聚合→identity→logs token 查询);②故障类八节,每节=症状→工具调用序列→归因分支(接线孤儿族/身份脑裂族/发现 beacon 族/打洞 punch 族/STUN 族/出口 egress 族/SSH 入口族/端口-进程归属表);③语义铁律(hostkey 404=未启动/STUN 不可达≠symmetric/单 conn 互偷包/空 memo≠STUN 健康/roster 唯一观测口是 status/候选≠信任/**载体 ok 桶=transport 级连接成立即记,SSH 握手/建 channel 成败不回账——握手级失败看 delegate.last_failure_err 的 ssh handshake 字样**);④红线(永不暴露清单+「勿按 level 过滤日志」);⑤兜底双轨(端点未到时走 status+logs)。
- 素材誊写:三案例(utun7/双身份脑裂/boot 竞态)语义保留、D-10 脱敏;仓内素材(铁律注释/specs/TODOS)直引。

## 4. 测试与验收

1. beacon Stats:注入 sendConn 写失败→bcastSendErr 增长(utun7 回放数据特征);计数逐分支单测。
2. SkipMemo Snapshot:纯读断言(过期标注不删除);classify 账本三态(unreachable 不进 memo/进账本)。
3. SessionCache Snapshot:纯读断言(不触发 reap/close);dead 判据;age。
4. carrier 分桶:三载体成功/udp 失败计数;DialTrace 结构零改动。
5. egress:激活态+末次失败记账。
6. 三新端点:handler 单测(seam 注入+nil 安全+snake_case)+透传链+parity 自动。
7. stun_probe:TryLock busy(抢锁断言,agentPeerExecTest 同款)/bound socket 独立性/预算硬顶。
8. MCP 四枚:PeerMCPContractSpecs 双面 + stdio 表条目 + agentcore 桥条目。
9. runbook:安装器幂等(内容寻址)/总闸跳过/不碰用户自装;SKILL.md frontmatter 可解析(parseSlashFrontmatter 域内字段)。
10. 验收线(D-11 口径,已落地):三案例数据特征在位测试 + runbook drift 护栏测试(SKILL.md 全部 /api/ 路径断言在两侧 mux 注册+承重字段 token 断言在 handler json tag——T9 质量评审后落地,D-11 记账口径据此更正);完整 AI 会话回放留待真机(记账)。
11. 既有测试同态:tunnel 装配缝预存失败/lanbeacon 3 广播(utun7 环境)/services flake 三族照旧;新触达文件 gofmt。

## 5. 风险与回滚

- 全批只读+显式探测+纯文本技能;唯一新状态=计数器/账本/末次失败(进程内,重启即清)。
- runbook 安装器写用户家目录(~/.claude/skills)——peer MCP 写 ~/.claude.json 同级先例,managed+幂等+不碰用户文件三纪律照搬。
- stun_probe 并发面:TryLock+独立短命 socket,与生产打洞 socket 无共享(互偷包铁律遵守)。
- 回滚=单批 revert;安装器产物(~/.claude/skills/mesh-runbook/)残留无害(用户可手删)。
