# 组网诊断读面+MCP 聚合工具+runbook 技能(阶段 1)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 三个只读/显式探测端点(diag/identity/stun_probe)+ 七数据源运行态补账 + MCP 聚合工具 4 枚(stdio+agentcore)+ runbook 技能(embed+自愈安装),把历史故障人工取证压成 AI 分钟级。

**Architecture:** 读端点零副作用(活探测独立显式端点);聚合优于细粒度;新状态全是进程内计数/账本(重启即清);core 直答+56433 透传(parity 自动覆盖);MCP 两面裸表(免审读档,D-2);runbook go:embed+内容寻址自愈安装。

**Tech Stack:** Go(`aliang.one/nursorgate`);spec=`docs/superpowers/specs/2026-11-19-mesh-diag-tools-design.md`(§0 拍板记录 D-1~D-11 是契约的一部分,评审已修 D-1 类比/D-4 机制)。

**工作目录:** `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/mesh-diag-tools`

**预存账(勿动勿扩波):** tunnel `TestAssemblySeamsPunchThreeStates`(master 预存)/lanbeacon 3 广播测试(utun7 环境)/services flake(`TestSteerReviveViaMessageRestoresSession` 首跑偶败复跑绿)/gofmt 4 个 services 测试文件基线未净。

**提交纪律:** 各 Task 只做到测试绿;提交集中在 Task 10,**仅在用户明确同意后执行**。

---

### Task 1: beacon 运行计数 + Stats()

**Files:**
- Modify: `app/mesh/lanbeacon/beacon.go`
- Test: `app/mesh/lanbeacon/beacon_test.go`(追加)

- [x] **Step 1: 写失败测试**(三段:计数归零初始态/注入失败增长/正常路径增长)

```go
// TestBeaconStatsCounters 运行计数(D-8):sent/err 原始计数+实例 started_at;
// utun7 吞广播的回放数据特征=sendErr 持续增长且 rxPackets=0。
func TestBeaconStatsCounters(t *testing.T) {
	b := NewBeacon("self-dev", 56441, pickUDPPort(t))
	st := b.Stats()
	if st.BcastSent != 0 || st.BcastSendErr != 0 || st.RxPackets != 0 {
		t.Fatalf("初始计数应全零: %+v", st)
	}
	if st.StartedAt != "" {
		t.Fatalf("未启动 started_at 应空: %+v", st)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer b.Stop()
	st = b.Stats()
	if st.StartedAt == "" {
		t.Fatal("启动后 started_at 应非空")
	}
	// 广播是 3s 周期,首播立即(broadcastLoop 立即首播)——轮询等首次发送落账
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.Stats().BcastSent+b.Stats().BcastSendErr > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.Stats().BcastSent+b.Stats().BcastSendErr == 0 {
		t.Fatal("启动后应至少一次发送落账(成功或失败)")
	}
	// utun7 特征对齐(T10 验收引用):广播发得出去≠收得到——本机无对端时
	// rx 维度恒零是合法态,断言钉住该可观测区分度。
	if b.Stats().RxPackets != 0 {
		t.Fatalf("本机无对端场景 rx_packets 应恒零: %+v", b.Stats())
	}
}
```

- [x] **Step 2: 跑确认失败**(`b.Stats undefined`)

Run: `go test ./app/mesh/lanbeacon/ -run TestBeaconStatsCounters -v`

- [x] **Step 3: 实现**

①Beacon 结构体(:177-215 区域)加:

```go
	// 运行计数(atomic,读不需 mu;生命周期=当前实例,装配对账换实例即清零)
	bcastSent      atomic.Uint64
	bcastSendErr   atomic.Uint64
	rxPackets      atomic.Uint64
	rxMalformed    atomic.Uint64
	rxSelf         atomic.Uint64
	rxStale        atomic.Uint64
	rxAcctRejected atomic.Uint64 // 跨账号
	rxSigRejected  atomic.Uint64
	rxDegradeBlocked atomic.Uint64
	candAccepted   atomic.Uint64
	candRefreshed  atomic.Uint64
	candEvicted    atomic.Uint64
	startedAt      atomic.Int64 // unix 秒;0=未启动
```

②落点(:469 失败分支 `bcastSendErr.Add(1)`/:474 成功 `bcastSent.Add(1)`;readLoop :483 `rxPackets.Add(1)`、:486-488 `rxMalformed.Add(1)`;ingest 七 reject 分支逐一、接受/刷新/挤逐)——每处一行,不改正文逻辑;Start 成功置 `startedAt.Store(time.Now().Unix())`,Stop 不清(实例级)。**beacon.go 需补 `"sync/atomic"` import(现无,评审实锤;build 期自暴露)。**
③BeaconStats 类型+访问器(snake_case json,对齐 LanPeerCandidate 风格):

```go
// BeaconStats beacon 运行计数快照(D-8):sent/err 原始计数,消费方算比率
// (广播是周期节拍非事件驱动);生命周期=当前 beacon 实例。utun7 吞广播的
// 数据特征=sendErr 增长且 rxPackets 恒零。
type BeaconStats struct {
	StartedAt        string `json:"started_at"` // RFC3339;未启动恒空
	BcastSent        uint64 `json:"bcast_sent"`
	BcastSendErr     uint64 `json:"bcast_send_err"`
	RxPackets        uint64 `json:"rx_packets"`
	RxMalformed      uint64 `json:"rx_malformed"`
	RxSelf           uint64 `json:"rx_self"`
	RxStale          uint64 `json:"rx_stale"`
	RxAcctRejected   uint64 `json:"rx_acct_rejected"`
	RxSigRejected    uint64 `json:"rx_sig_rejected"`
	RxDegradeBlocked uint64 `json:"rx_degrade_blocked"`
	CandAccepted     uint64 `json:"cand_accepted"`
	CandRefreshed    uint64 `json:"cand_refreshed"`
	CandEvicted      uint64 `json:"cand_evicted"`
}

func (b *Beacon) Stats() BeaconStats {
	if b == nil {
		return BeaconStats{}
	}
	st := BeaconStats{
		BcastSent:        b.bcastSent.Load(),
		BcastSendErr:     b.bcastSendErr.Load(),
		RxPackets:        b.rxPackets.Load(),
		RxMalformed:      b.rxMalformed.Load(),
		RxSelf:           b.rxSelf.Load(),
		RxStale:          b.rxStale.Load(),
		RxAcctRejected:   b.rxAcctRejected.Load(),
		RxSigRejected:    b.rxSigRejected.Load(),
		RxDegradeBlocked: b.rxDegradeBlocked.Load(),
		CandAccepted:     b.candAccepted.Load(),
		CandRefreshed:    b.candRefreshed.Load(),
		CandEvicted:      b.candEvicted.Load(),
	}
	if ts := b.startedAt.Load(); ts > 0 {
		st.StartedAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	return st
}
```

- [x] **Step 4: 跑绿 + 包内全量**(lanbeacon 3 广播测试 utun7 环境预存不计;`go vet`)

Run: `go test ./app/mesh/lanbeacon/ -run TestBeaconStatsCounters -v -count=1 && go vet ./app/mesh/lanbeacon/ && gofmt -l app/mesh/lanbeacon/`

### Task 2: SkipMemo Snapshot + classify 结果账本

**Files:**
- Modify: `app/mesh/punch/stun.go`(Snapshot)
- Modify: `app/http/services/agent_peer_punch.go`(A/B 两侧 classify 账本+聚合快照缝)
- Test: `app/mesh/punch/stun_test.go`(若存在 stun 相关测试文件追加;否则 `agent_peer_punch_test.go`)+services 侧测试

- [x] **Step 1: stun.go 加纯读快照**(SkipMemo struct :188-192,ShouldSkip :238 之后)

```go
// SkipMemoEntry 快照单项;Expired=true 表示 remaining<=0(惰性清理仍归
// Record/ShouldSkip——快照零副作用,绝不删表)。
type SkipMemoEntry struct {
	PeerID           string `json:"peer_id"`
	RemainingSeconds int64  `json:"remaining_seconds"`
	Expired          bool   `json:"expired"`
}

// Snapshot memo 表纯读快照;空表≠STUN 健康(不可达不进 memo,见 classify
// 账本)——消费方必须并排读 classify 账本,勿单读。now 走 m.clock()(零值
// 兜底约定:生产以 &SkipMemo{} 零值构造,now 可 nil——评审 Issue 实锤,
// 直呼 m.now() 即生产 nil-func panic)。
func (m *SkipMemo) Snapshot() []SkipMemoEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	out := make([]SkipMemoEntry, 0, len(m.until))
	for id, until := range m.until {
		rem := int64(until.Sub(now) / time.Second)
		out = append(out, SkipMemoEntry{PeerID: id, RemainingSeconds: rem, Expired: rem <= 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}
```

⚠️ stun.go 需补 `"sort"` import(现无)。

- [x] **Step 2: services 层 classify 结果账本**(agent_peer_punch.go)

A/B 两侧结构体(:138-146 与 :145 区域)**各自**加(评审 Advisory:计划原文只写 A 侧,B 侧「同款」必须显式):

```go
	// classify 结果账本(快照面新增):STUN 不可达≠symmetric 不写 memo
	// (铁律③),不可达只落这里——空 memo+账本 unreachable 才是「STUN 坏」,
	// 空 memo+账本空=「从未探测」。
	classifyMu       sync.Mutex
	classifyOutcomes map[string]ClassifyOutcome // side=a 键=对端 device_id;side=b 键=本机 device_id
```

```go
// ClassifyOutcome 单次 STUN 判型结果(扁平三字段:同时作内部 map 值与两侧
// 快照单项,不另设 SideEntry 重复类型)。side=a 键=对端 device_id;side=b
// 键=本机 device_id=「本机 NAT」,键语义沿 :377-378 Record(selfPeerID)
// 注释(实现时直引该注释防键语义漂移)。
type ClassifyOutcome struct {
	PeerID  string `json:"peer_id"`
	Outcome string `json:"outcome"` // ok|symmetric|unreachable|public_declared
	At      string `json:"at"`      // RFC3339
}
```

落点:A dialMiss classify 块(:950-965)ok/symmetric/error 三分支各记一条(`recordClassifyOutcome(peerID, "ok"/"symmetric"/"unreachable")`,新增小方法锁内写);B handleSession(:367-382)同款(键=selfPeerID,三分支)。public_declared 来源=A 侧收到 session.result public 声明跳 STUN 的分支(punch-public-peer 批,:920-931 `publicPeer = remoteBool(res,"public",false)` + :949 skip 分支)。
- [x] **Step 3: services 聚合快照缝**

```go
// MeshPunchClassifySnapshot classify 结果账本聚合(A/B 分开报,键语义显式标注)。
type MeshPunchClassifySnapshot struct {
	SideA []ClassifyOutcome `json:"side_a"` // 键=对端 device_id
	SideB []ClassifyOutcome `json:"side_b"` // 键=本机 device_id=本机 NAT
}
func MeshPunchClassifySnapshot() MeshPunchClassifySnapshot { ... }
// memo 快照出口拍定:diag 端点的 punch.memo[] 经 a/b.skipMemo.Snapshot()
// 并入本缝旁的 MeshPunchSessionsSnapshot 同级输出(勿另立第三访问器)。
```

A 经 sharedAgentPeerPunchA(:756) B 经 sharedAgentPeerPunchB(:278),nil 安全(未初始化=空数组)。**锁序:只碰 classifyMu,绝不触 dialMu/mu 执行路径。**
- [x] **Step 4: 测试**(punch 层:Record 两键→Snapshot 含 Expired 标注→不删表再 ShouldSkip 仍真;services 层:构造 A 结构体直接调 recordClassifyOutcome 三态→快照断言;键语义断言 side=b 键=selfPeerID)

Run: `go test ./app/mesh/punch/ ./app/http/services/ -run "SkipMemo|Classify" -v -count=1`

### Task 3: SessionCache Snapshot + services 会话快照缝

**Files:**
- Modify: `app/mesh/punch/session.go` + `app/http/services/agent_peer_punch.go`
- Test: punch + services 测试

- [x] **Step 1: session.go** — cachedSession(:105-109)加 `createdAt time.Time`(Put :181 赋值);Snapshot()(:234 之后):

```go
// SessionSnapshotEntry 缓存会话快照单项;纯读——绝不调 reapIdleLocked/
// closeLocked/delete(Acquire 会 refs++ 也不能用)。
type SessionSnapshotEntry struct {
	Key          string `json:"key"`
	Refs         int    `json:"refs"`
	InFlight     bool   `json:"in_flight"` // refs>0
	IdleSeconds  int64  `json:"idle_seconds"`
	AgeSeconds   int64  `json:"age_seconds"`
	Dead         bool   `json:"dead"` // punchSessionDead 非阻塞 select
}
// Snapshot 缓存表纯读快照;90s 硬寿命语义见 listener.go:32。
func (c *SessionCache) Snapshot() []SessionSnapshotEntry { ... mu 内遍历,punchSessionDead 复用;时钟走 c.clock()(零值兜底约定,与 SkipMemo 同款) ... }
```

- [x] **Step 2: services 聚合缝** — MeshPunchSessionsSnapshot():A 侧(cache.Snapshot()+sockets 表 purpose 拆解:裸 peerID=mesh/"transfer:"+peerID=transfer;hit/miss/fail 六分类计数——hit :842/miss :830/fail :884 memo skip/:917/:960/:1007/:1044/:1079 加 atomic 计数)+B 侧(b.sessions 在册数+每会话 startedAt+done;agentPeerPunchSession :123-130 加 startedAt)。
- [x] **Step 3: 测试**(punch:Put 两键→Snapshot 断言 refs/age/dead/纯读;services:A 装配→快照断言 purpose 拆解与计数)

Run: `go test ./app/mesh/punch/ ./app/http/services/ -run "Session.*Snapshot|SessionsSnapshot" -v -count=1`

### Task 4: 载体分桶 + egress 委托记账

**Files:**
- Modify: `outbound/proxy/mesh/sshexit.go`(包级计数)+ `processor/egress/egress.go`
- Test: `outbound/proxy/mesh/sshexit_test.go` + `processor/egress/egress_test.go`

- [x] **Step 1: mesh 包级计数**(sshexit.go 包级 var;DialTrace struct :102 **零改动**——定谳):

```go
// 载体分桶(包级 atomic):ExitClient 实例频繁重建(缓存键翻转/egress 每调用
// 重建),实例字段会蒸发——包级计数是唯一稳定落点;DialTrace 本身保持
// 「无跨连接状态」定谳不动(trace 是每调用的返回值)。
var (
	carrierLanOK   atomic.Uint64
	carrierUDPOK   atomic.Uint64
	carrierPikoOK  atomic.Uint64
	carrierUDPErr  atomic.Uint64
	carrierDialFail atomic.Uint64
)
// CarrierBucketSnapshot 载体分桶快照(全局维度 v1;peer 维度留二期)。
type CarrierBucketSnapshot struct {
	LanOK    uint64 `json:"lan_ok"`
	UDPOK    uint64 `json:"udp_ok"`
	PikoOK   uint64 `json:"piko_ok"`
	UDPErr   uint64 `json:"udp_err"`
	DialFail uint64 `json:"dial_fail"`
}
func CarrierBucketSnapshot() CarrierBucketSnapshot { ... }
```

落点:DialContextTraced 内 :144(Used 确定后按 carrier 名记 ok)/:148(UDPErr)/:134 与 :150-154(dial_fail)。egress/DNS 路径经 DialContext→DialContextTraced 自动覆盖。
- [x] **Step 2: egress 记账**(egress.go 包级 atomic.Value 持 `delegateLastFailure{at, err string}`;落点 platformDialContext :87/:96 两失败分支;读访问器 DelegateDiagnostics(){Active bool(meshDelegateActive 现值,配置判定), LastFailureAt, LastFailureErr})。
- [x] **Step 3: 测试**(mesh:现有 sshexit_test 三载体场景跑过后断言分桶增长;egress:构造失败分支断言 last_failure 落账+Active 判定)

Run: `go test ./outbound/proxy/mesh/ ./processor/egress/ -count=1`

### Task 5: GET /api/mesh/diag 聚合端点

**Files:**
- Create: `app/http/handlers/mesh_diag_handler.go`(独立文件,勿塞爆 mesh_handler.go)
- Modify: `app/http/routes/routes.go`(register 一行)+ `app/agentruntime/server.go`(forward 一行)
- Test: `app/http/handlers/mesh_diag_handler_test.go`

- [x] **Step 1: 写失败测试**(MeshHandler 同款 func() seam 直注风格;seams:beaconStats/lanPeers/config/deviceID/rosterSync/punchDiag/carrierBuckets/delegateDiag;三断言:注入透传/nil seam 200 零值/恒数组不 null)

响应结构(meshDiagResponse,全 snake_case):四重闸判定组(enable/effective/forward_enable/punch_enable/auto_peers/roster_sync 复用三态)+ beacon stats + carriers + delegate{active,last_failure_at,last_failure_err} + punch{memo[],classify{side_a,side_b},sessions} + stun_servers(现值)+ lan_peers 候选。

- [x] **Step 2: 红**(`go test ./app/http/handlers/ -run TestMeshDiag`)
- [x] **Step 3: 实现**(NewMeshDiagHandler() 钉生产源:lanbeacon.Default().Stats/tunnel.MeshLANRunning 已有——直接组装;经 services 新访问器拿 punch 快照;mesh CarrierBucketSnapshot;egress DelegateDiagnostics 经注入缝——egress 在 processor 包,handlers 直 import 无环[盘点已核 processor/egress 零 app 依赖,反向 handlers→egress 安全];HandleDiag 方法手判 405 用 http.Error)
- [x] **Step 4: 双注册**(routes.go :367 后 `register("/api/mesh/diag", h.MeshDiag.HandleDiag, http.MethodGet)`——Handlers 结构体加 MeshDiag 字段或复用 h.Mesh 挂方法,以现场 Handlers 形态为准;server.go meshForward 加 `mux.HandleFunc("GET /api/mesh/diag", meshForward.ServeHTTP)`)
- [x] **Step 5: 绿 + parity 自动覆盖**(`go test ./app/agentruntime/ -run TestAgentRouteParity -count=1` 应绿——新端点两侧都有)

Run: `go test ./app/http/handlers/ -run TestMeshDiag -v -count=1 && go test ./app/agentruntime/ -run TestAgentRouteParity -count=1`

### Task 6: GET /api/mesh/identity 身份一致性端点

**Files:**
- Modify: `app/http/handlers/mesh_diag_handler.go`(同文件加 HandleIdentity)+ routes.go + server.go
- Test: 同测试文件追加

- [x] **Step 1: 失败测试**(seam 注入四源→断言 mismatches 判定:state≠文件/文件≠beacon self/roster 缺 self 各产一条;全一致→mismatches 空)
- [x] **Step 2: 实现** — **⚠️ 评审实锤:四个生产源在 handlers 包内不可达(agentIdentityPath/loadAgentDeviceIdentity/agentDeviceIdentity/s.state.DeviceID 全小写未导出;CurrentDeviceID 文档注释自称「只读查询」但实际会走 ensureDeviceIdentityLocked 生成身份——正是本端点要抓的反向陷阱,绝不可用)。落点钉死:services 新增两个导出只读访问器**(mesh_diag_handler 同批新建或挂 mesh_roster_sync.go 旁):
  - `LoadAgentIdentityFileReadonly() (path, deviceID string)`——ReadFile+Unmarshal 零副作用,缺失返 ("", "")(照 loadAgentDeviceIdentity :1381 同款只读逻辑,路径走 agentIdentityPath :1373-1379 同款分叉)
  - `StateDeviceIDReadonly() string`——s.mu 锁内读 s.state.DeviceID(持锁只读,不生成)
  handler 注入缝钉这两个访问器 + rosterSelfPresent func()(MeshRosterEntryFor,已导出)+ beaconSelfID func()(**默认解:`func (b *Beacon) SelfID() string { return b.selfID }` 两行 getter 加在 beacon.go,handler 经既有 lanbeacon.Default() 读**;tunnel 访问器仅在出现第二消费方时再加)。输出:{device_id, identity_file_path, identity_file_device_id, state_device_id, beacon_self_device_id, roster_self_present, mismatches[]}。
- [x] **Step 3: 双注册 + 绿**(同 T5 Step 4-5)

### Task 7: POST /api/mesh/stun_probe 显式活探测

**Files:**
- Create: `app/http/handlers/mesh_stun_probe_handler.go` + test
- Modify: routes.go + server.go

- [x] **Step 1: 失败测试**(四条:busy——包级锁 Lock 抢住→200+data.busy=true[agentPeerExecTest :154-177 同款];正常——httptest 假 STUN?**不可行:STUN 是 UDP 协议**——改注入缝:probeFunc func(conn, servers, budget) results,测试注入假 probeFunc 断言传参/截断;截断——budget 1ms+4 台→truncated=true 且已测台数如实;独立性——probeFunc 收到的 conn 非 nil 且用后关)
- [x] **Step 2: 实现**:

```go
// 包级独立锁(不与 agentPeerExecMu 共锁:probe 毫秒级 vs exec 秒级,共锁互挡)
var meshStunProbeMu sync.Mutex
const meshStunProbeBudget = 8 * time.Second

// HandleStunProbe POST 零体显式活探测(D-3/D-4):TryLock busy-early-return
// (结构化 200 busy,绝不排队);每请求 punch.NewBoundUDPConn 新建短命 socket
// →逐台 Classify 语义(单台 3s),循环内查剩余预算截断——Classify/Discover
// 不收 ctx(评审 Issue 2),预算耗尽即停+truncated 如实报出;输出带默认档
// 语境(默认档大量超时是常态非故障)。绝不复用在途 conn(互偷包铁律)。
```

流程:method 闸 POST→TryLock(失败 200+busy)→读当前生效 STUN 清单(EffectiveStunServers)→`conn, err := punch.NewBoundUDPConn(punch.DefaultResolver)`(失败=500 上抛,**绝不静默回落**铁律)→defer conn.Close()→逐台:punch.Discover(conn, server, 3s)(每台前查 ctx/预算;Discover 返回映射→与下一台端口比对判型——**复用 stun.go Classify 语义但需逐台截断,若 Classify 签名不便截断则在 handler 内自己组织 Discover 循环,语义照 Classify :144-184 注释**;或给 stun.go 加 `ClassifyWithBudget(conn, servers, totalBudget)` 导出变体——推荐后者,语义单点)→输出 {results:[{server, ok, mapped_port, nat_type, err}], truncated, elapsed_ms(实耗时——T7 质量评审改名拍定,原名 budget_ms 名实不符会被 AI 消费方误读为上限), note:"默认档大量超时是常态"}。
- [x] **Step 3: 双注册**(POST 形态:`register("/api/mesh/stun_probe", h.MeshProbe.HandleStunProbe, http.MethodPost)`+`mux.HandleFunc("POST /api/mesh/stun_probe", meshForward.ServeHTTP)`)+绿+parity

Run: `go test ./app/http/handlers/ -run TestMeshStunProbe -v -count=1 && go test ./app/agentruntime/ -run TestAgentRouteParity -count=1`

### Task 8: MCP 工具 4 枚(stdio + agentcore 两面)

**Files:**
- Modify: `cmd/mcp.go`(peerMCPTools 表尾 4 条)+ `app/http/services/agent_peer_tools.go`(agentCorePeerTools 同语义 4 条)+ `app/http/services/agent_peer_mcp.go`(PeerMCPContractSpecs 条目)
- Test: 契约测试 + stdio 表测试(仓内现有形态)

- [x] **Step 1: stdio 表 4 条**(device_list 零参 GET 形态 :397-401 照抄;stun_probe 照 ai_run_start POST 形态;timeoutMs 14_000;description 契约措辞三面稳定词:mesh_readiness「idempotent, read-only, timeout 14s, stun results are last-probe not live」/mesh_stun_probe「NOT idempotent (sends UDP), may return busy (structured), budget 8s, do not retry immediately on busy」/mesh_identity「idempotent, read-only」/mesh_lan_candidates「idempotent, read-only」)
- [x] **Step 2: agentcore 桥 4 条**(agentCorePeerTools :1045 形态照抄,同语义 description;点名形态按现场)
- [x] **Step 3: PeerMCPContractSpecs 条目**(拍板[评审实锤:现有契约类型只钉 CoreName/StdioName/Required/Optional/Keywords,无 path/method/timeout 字段]:**保持契约类型 semantics-only 照 :587 形态填四条[名/参数/关键词],path/method/timeoutMs 的等价断言放 cmd 侧新测试直接钉 peerMCPTools 表条目**——不动既有契约类型,diff 最小;**D-1:不动 agent_tool_registry.go 第三面**)
- [x] **Step 4: 测试**(契约测试绿+stdio list 含四新工具+gate 关闭时 call 出口拒绝现有路径[若门控链覆盖];manual smoke:本机 56433 若在跑可直接 curl 四端点验证信封——不强制)

Run: `go test ./cmd/ -run "MCP" -count=1 && go test ./app/http/services/ -run "PeerMCPContract" -count=1`

### Task 9: runbook 技能(embed + 自愈安装)

**Files:**
- Create: `app/assets/skills/mesh-runbook/SKILL.md`(源真值)+ `app/assets/embed.go`(或复用 app/embed.go 加 embed 变量——以现场 embed 形态为准)+ `app/http/services/mesh_runbook_installer.go` + 启动接线(拍定:挂 agent(56433) 进程启动位——安装目标是 user 域 ~/.claude/skills,core(SYSTEM) 可能无用户家目录;与 peer MCP 注册自愈同位 server.go:93 附近 `EnsurePeerMCPMatchesConfig` 同位缝;**boot 审计测试 symbols 表不扩**[评审实锤:bootFiles 只含 start.go/core.go,server.go 不在其中,扩表必红;接线注释写明归属即可])
- Test: `app/http/services/mesh_runbook_installer_test.go`

- [x] **Step 1: SKILL.md 撰写**(渐进披露形态;frontmatter name: mesh-runbook+description ≤2 句;正文五节见 spec §3.6;素材:仓内铁律注释直引+三案例誊写脱敏[D-10];所有取证路径走本批端点+status 三真值+logs token,双轨兜底分支写明;红线节=永不暴露清单+勿按 level 过滤;含端口-进程归属表 56430-56433/56440)
- [x] **Step 2: 安装器**(内容寻址:读现有文件 bytes==embed bytes 幂等跳写;不同=整体替换 tmp+rename;目录 MkdirAll;目标 ~/.claude/skills/mesh-runbook/SKILL.md[userHome 经 os.UserHomeDir];总闸 EffectiveAgentCoreSkillsEnabled 关=跳过;绝不列目录/不碰其他技能)
- [x] **Step 3: 接线+审计**(agent 进程启动位 server.go:93 同位;boot 审计 symbols 表不扩[见 Files 拍定];接线注释写明「runbook 自愈安装,归属用户技能域非 mesh 装配族」;config 总闸关时诚实日志一条;**开闸生效语义:自愈只在进程启动位执行,运行期开闸要等下一进程周期——测试补此语义断言**[评审 Advisory])
- [x] **Step 4: 测试**(t.TempDir 仿 home:幂等跳写[两次 mtime 不变]/内容变替换/总闸关跳过/开闸后下次安装调用落盘[进程周期语义]/frontmatter 可被 parseSlashFrontmatter 解析[name/description 在域内])

Run: `go test ./app/http/services/ -run MeshRunbook -v -count=1 && go build ./...`

### Task 10: 全量验证 + docs + TODOS(提交仍须用户放行)

- [x] **Step 1: 全量**:`go build ./... && go test ./app/mesh/... ./app/tunnel/ ./app/http/handlers/ ./app/http/services/ ./app/agentruntime/ ./outbound/proxy/mesh/ ./processor/egress/ ./cmd/... 2>&1 | tail -20`(预存三族同态判定;tunnel -race 一轮)
- [x] **Step 2: 验收线(D-11 口径,已落地)**:三案例数据特征在位(T1 beacon 计数 rx 特征断言[utun7]/T5 roster_sync 三态透传[未接线态]/T6 identity mismatches[脑裂])+ **T9 drift 护栏测试**(SKILL.md 端点引用+承重字段 token 双断言,T9 质量评审后落地)
- [x] **Step 3: docs**:spec+计划已在;TODOS.md 追加「阶段 1 遗留账」段(§不做清单+残余账 6 条:第三面/组级键/agent 侧身份/peer 维度分桶/开闸生效文档/AI 回放真机验收)
- [x] **Step 4: 终审**(控制器派整批 final reviewer)→ 提交方案呈报,**等用户放行**
