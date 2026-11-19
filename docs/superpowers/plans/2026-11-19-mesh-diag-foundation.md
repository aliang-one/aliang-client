# 组网诊断地基修复批(阶段 0)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修四个地基缺陷(roster sync 孤儿接线 / mesh status 假绿 / mesh 端点单边注册 / mesh 域日志盲区),为 AI 组网面铺真数据底座。

**Architecture:** 纯只读/接线级改动:boot 接线补两行 + 三个运行态真值访问器 + status 只增字段 + 56433 forward-to-core 透传 + parity 护栏扩围 + 20 处日志换 sink。零 MCP 工具、零写路径、零跨仓契约。

**Tech Stack:** Go(仓库 `aliang.one/nursorgate`),测试 `go test`,spec 见 `docs/superpowers/specs/2026-11-19-mesh-diag-foundation-design.md`。

**工作目录:** 全部命令在 worktree `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/mesh-diag-foundation` 内执行。

**预存账(勿动):** `TestAssemblySeamsPunchThreeStates` 在 master(1b8c36b)即失败,三子项 `装配产物应为 *ExitClient, got <nil>`。本批不修;Task 8 验证其状态保持同态即可。

**本批新增残余账:** ①mesh forward coreBase 解析链不覆盖「core 56431 被占回退随机端口」场景(Task 5 质量评审实锤:ownerBaseURL 在 56433 进程的生效腿解析落回默认 56431,与旧硬编码同;--host 与 ALIANG_MANAGEMENT_ADDR 两场景已真修)——挂 TODOS;②环境预存:本机 utun7 劫持 255.255.255.255,lanbeacon 3 个广播测试在本机恒败(stash 循环实锤,非代码问题);③services 包全量存在预存 flaky(首跑偶败复跑绿,仓内已记载);④**mesh 诊断日志按 [MESH-*]/[AGENT-TCP] 等 token 查,勿按 level 过滤**——约 10 行错误态行按原样等价原则留在 Info 级,显式 level=error 查询会漏看、运行时日志级别升至 WARN+ 会静默出 buffer(Task 7 评审立字据);⑤日志 sink stderr→stdout:stderr 分流部署(如 `2>err.log`)会丢这些行,journald 合流部署无影响;⑥mesh_assembly.go:490 无参 fmt.Sprintf(S1039 味,仓无 lint 有先例,保留);⑦**mesh_roster_sync.go 全程无日志**——roster 同步失败仅经 /api/mesh/status 的 roster_sync 三态呈现,不经 /api/logs,写 AI 组网 runbook 时须知(Task 7 评审信息项)。

**提交纪律(全仓铁律):** 各 Task 只做到「测试绿」为止,**不执行 git commit**;全部提交集中在 Task 9,且仅在用户明确同意后执行。

---

### Task 1: `Beacon.Running()` 运行位访问器

**Files:**
- Modify: `app/mesh/lanbeacon/beacon.go`(Running 加在 Stop 之后)
- Test: `app/mesh/lanbeacon/beacon_test.go`(追加)

- [x] **Step 1: 写失败测试**(追加到 `beacon_test.go` 末尾;`pickUDPPort(t)` 是包内既有 helper)

```go
// TestBeaconRunningReflectsStartStop 运行位真值(D2 假绿治理):构造即
// false,Start 成功后 true,Stop 后回到 false。ctx 收摊路径(Broadcast
// watcher 代调 Stop)同样复位——不单测,同一 started 位。
func TestBeaconRunningReflectsStartStop(t *testing.T) {
	b := NewBeacon("self-dev", 56441, pickUDPPort(t))
	if b.Running() {
		t.Fatal("未 Start 时 Running() 应为 false")
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer b.Stop()
	if !b.Running() {
		t.Fatal("Start 成功后 Running() 应为 true")
	}
	b.Stop()
	if b.Running() {
		t.Fatal("Stop 后 Running() 应为 false")
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `go test ./app/mesh/lanbeacon/ -run TestBeaconRunningReflectsStartStop -v`
Expected: FAIL,`b.Running undefined`

- [x] **Step 3: 实现**(加在 `Stop` 方法之后,与 Start/Stop 同锁序)

```go
// Running beacon 是否处于运行态(Start 成功至 Stop/父 ctx 收摊之间为真)。
// /api/mesh/status 运行态真值消费(D2 假绿治理):这是配置推导字段之外的
// 唯一「活着」事实。nil 接收者对齐 Stop 的防御惯例。
func (b *Beacon) Running() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}
```

- [x] **Step 4: 跑测试确认通过 + 包内全量**

Run: `go test ./app/mesh/lanbeacon/ -v 2>&1 | tail -5`
Expected: 该测试 PASS,包内其余 PASS

### Task 2: `MeshLANRunning()` 装配生效态访问器

**Files:**
- Modify: `app/tunnel/mesh_assembly.go`(`MeshLANPort` 之后追加)
- Test: `app/tunnel/mesh_assembly_test.go`(追加)

- [x] **Step 1: 先读既有装配测试 helper**

Run: `grep -n "startMeshLANAssembly(\|StopMeshLANAssembly()" app/tunnel/mesh_assembly_test.go | head -8`
读一处既有装配测试(如 TestAssemblySeams 族之前的 setup),确认测试期装配入口与配置启用的写法——Task Step 3 的正向断言要镜像它的配置形态,**不得触碰 TestAssemblySeamsPunchThreeStates(预存失败账)**。

- [x] **Step 2: 写失败测试**(追加到 `mesh_assembly_test.go`;负向断言零依赖先落,正向断言镜像既有 helper)

```go
// TestMeshLANRunningReflectsAssembly 生效态真值(D2):未装配恒 false;
// 装配起后 true;Stop 后 false。正向段镜像文件内既有装配测试的配置写法
// (步骤 1 已确认),只在测试内起停,不留全局态。
func TestMeshLANRunningReflectsAssembly(t *testing.T) {
	t.Cleanup(StopMeshLANAssembly)
	if MeshLANRunning() {
		t.Fatal("未装配时 MeshLANRunning() 应为 false")
	}
	// —— 正向段:镜像既有装配测试的配置启用形态(步骤 1 确认后填)——
	// startMeshLANAssembly(deviceIDFn, 缩短周期, 测试beacon端口) 起装配,
	// 轮询等待 running 翻真(对账周期内),断言 MeshLANRunning() == true。
	// 参考 TestAssemblySeams 族之外的「装配起停」测试的配置构造。
}
```

- [x] **Step 3: 补正向段**(直接镜像 `TestMeshLANAssemblyLifecycle`(mesh_assembly_test.go:185-232)的四件套,helper 均已存在于该文件,照抄调用:`pickFreeUDPForAssembly(t)` 挑口 → `swapGlobalMeshConfig(t, &config.MeshConfig{...})` 按该测试的启用形态填 → `startMeshLANAssembly(deviceIDFn, 缩短interval, 测试beacon口)` 起装配 → `waitForMeshLAN(t, true)` 轮询断言翻真)

- [x] **Step 4: 跑测试确认通过**

Run: `go test ./app/tunnel/ -run TestMeshLANRunningReflectsAssembly -v`
Expected: PASS(若负向段就已失败,先修访问器)

- [x] **Step 5: 实现访问器**(加在 `MeshLANPort()` 之后)

```go
// MeshLANRunning mesh LAN 装配当前生效态;/api/mesh/status 运行态真值消费
// (D2 假绿治理,访问器形态对齐 MeshLANPort)。running 是装配器对账后的
// 生效态,与配置开关解耦——开关开而 running=false 即假绿现场本身。
func MeshLANRunning() bool {
	meshLAN.mu.Lock()
	defer meshLAN.mu.Unlock()
	return meshLAN.running
}
```

- [x] **Step 6: 复跑测试**

Run: `go test ./app/tunnel/ -run TestMeshLANRunningReflectsAssembly -v`
Expected: PASS

### Task 3: `MeshRosterSyncStatus()` 三态快照 + last_error 记账

**Files:**
- Modify: `app/http/services/mesh_roster_sync.go`
- Test: `app/http/services/mesh_roster_sync_test.go`(存在则追加,不存在新建)

- [x] **Step 1: 写失败测试**(同包直测;`seedMeshRosterForTest` 既有)

```go
// TestMeshRosterSyncStatusThreeStates 三态齐全(D2):从未同步(synced=false)
// / 同步过且健康(last_error 空)/ 同步过但上一轮失败(last_error 非空)。
func TestMeshRosterSyncStatusThreeStates(t *testing.T) {
	t.Cleanup(func() {
		meshRosterMu.Lock()
		meshRosterCache = nil
		meshRosterFetchedAt = time.Time{}
		meshRosterLastError = ""
		meshRosterMu.Unlock()
	})

	// 态 1:从未同步(D1 未接线即此态)
	meshRosterMu.Lock()
	meshRosterCache = nil
	meshRosterFetchedAt = time.Time{}
	meshRosterLastError = ""
	meshRosterMu.Unlock()
	st := MeshRosterSyncStatus()
	if st.Synced || st.LastSyncAt != "" || st.LastError != "" {
		t.Fatalf("从未同步态应为全零: %+v", st)
	}

	// 态 2:同步过且健康
	seedMeshRosterForTest(map[string]meshRosterEntry{"dev-a": {PubKeyLine: "k", HostKeyFp: "f"}})
	st = MeshRosterSyncStatus()
	if !st.Synced || st.LastSyncAt == "" || st.LastError != "" {
		t.Fatalf("健康态应为 synced=true+时间+无错: %+v", st)
	}
	if _, err := time.Parse(time.RFC3339, st.LastSyncAt); err != nil {
		t.Fatalf("last_sync_at 应为 RFC3339: %v", err)
	}

	// 态 3:上一轮拉取失败(旧缓存仍在)
	meshRosterMu.Lock()
	meshRosterLastError = "502 bad gateway"
	meshRosterMu.Unlock()
	st = MeshRosterSyncStatus()
	if !st.Synced || st.LastError == "" {
		t.Fatalf("失败态应为 synced=true+last_error 非空: %+v", st)
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestMeshRosterSyncStatusThreeStates -v`
Expected: FAIL,`undefined: MeshRosterSyncStatus`

- [x] **Step 3: 实现**(mesh_roster_sync.go:var 块加 `meshRosterLastError string`;`fetchMeshRoster` 错误路径记账、成功路径清零;文件末尾加类型+访问器)

```go
var meshRosterLastError string // 上一轮 roster 拉取失败原因;成功轮清空
```

`fetchMeshRoster` 改造(两个错误 return 变记账 return,成功路径清零):

```go
func fetchMeshRoster(s *AgentService, cfg *config.Config) {
	raw, err := s.callAgentServer("GET", cfg.GetAgentMeshRosterURL(), nil, "")
	if err != nil {
		recordMeshRosterFetchError(err)
		return
	}
	var resp struct {
		Roster []meshRosterWireEntry `json:"roster"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		recordMeshRosterFetchError(err)
		return
	}
	next := make(map[string]meshRosterEntry, len(resp.Roster))
	for _, e := range resp.Roster {
		id := strings.TrimSpace(e.DeviceID)
		if id == "" {
			continue
		}
		next[id] = meshRosterEntry{
			PubKeyLine: strings.TrimSpace(e.PubKeyLine),
			HostKeyFp:  strings.TrimSpace(e.HostKeyFp),
		}
	}
	meshRosterMu.Lock()
	meshRosterCache = next
	meshRosterFetchedAt = time.Now()
	meshRosterLastError = ""
	meshRosterMu.Unlock()
}

// recordMeshRosterFetchError 失败记账(保留旧缓存不动——fail-open with
// cached roster 语义不变);/api/mesh/status 经 MeshRosterSyncStatus 消费。
func recordMeshRosterFetchError(err error) {
	meshRosterMu.Lock()
	meshRosterLastError = err.Error()
	meshRosterMu.Unlock()
}

// MeshRosterSyncSnapshot roster 同步运行态快照载荷;/api/mesh/status 消费
// (D2)。命名拍板:类型带 Snapshot 后缀,与同名查询函数 MeshRosterSyncStatus()
// 错开(Go 包级类型与函数共用一个标识符命名空间,同名=重声明编译错)。
// synced=false=从未同步(boot 未接线即此态);synced=true+last_error 非空=
// 上一轮拉取失败(旧缓存仍在);两者皆空=健康。三态齐全后「roster 空表」
// 不再被误读成服务端故障——接线孤儿(start.go 漏接线同族)一眼可见。
type MeshRosterSyncSnapshot struct {
	Synced     bool   `json:"synced"`
	LastSyncAt string `json:"last_sync_at"` // RFC3339 UTC;synced=false 恒空串
	LastError  string `json:"last_error"`   // 上一轮拉取失败原因;成功轮清空
}

func MeshRosterSyncStatus() MeshRosterSyncSnapshot {
	meshRosterMu.RLock()
	defer meshRosterMu.RUnlock()
	st := MeshRosterSyncSnapshot{LastError: meshRosterLastError}
	if !meshRosterFetchedAt.IsZero() {
		st.Synced = true
		st.LastSyncAt = meshRosterFetchedAt.UTC().Format(time.RFC3339)
	}
	return st
}
```

- [x] **Step 4: 跑测试确认通过 + services 包 mesh 相关全量**

Run: `go test ./app/http/services/ -run "TestMeshRoster" -v && go test ./app/http/services/ 2>&1 | tail -2`
Expected: PASS

### Task 4: `/api/mesh/status` 三字段 + handler seams

**Files:**
- Modify: `app/http/handlers/mesh_handler.go`
- Test: `app/http/handlers/mesh_handler_test.go`(追加)

- [x] **Step 1: 写失败测试**(追加;seam 直注风格对齐文件内既有 `&MeshHandler{...}` 构造)

```go
// TestHandleStatusRuntimeTruthFields 运行态真值三字段(D2):只增不改,
// 既有字段断言不受影响;nil seam 安全回零值。
func TestHandleStatusRuntimeTruthFields(t *testing.T) {
	h := &MeshHandler{
		beaconRunning: func() bool { return true },
		lanRunning:    func() bool { return false },
		rosterSync:    func() services.MeshRosterSyncSnapshot { return services.MeshRosterSyncSnapshot{Synced: true, LastSyncAt: "2026-11-19T00:00:00Z", LastError: "boom"} },
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil)
	h.HandleStatus(w, r)
	var body struct {
		Data struct {
			BeaconRunning bool                          `json:"beacon_running"`
			LANRunning    bool                          `json:"lan_running"`
			RosterSync    services.MeshRosterSyncSnapshot `json:"roster_sync"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Data.BeaconRunning || body.Data.LANRunning {
		t.Fatalf("seam 注入值未透传: %+v", body.Data)
	}
	if !body.Data.RosterSync.Synced || body.Data.RosterSync.LastError != "boom" {
		t.Fatalf("roster_sync 未透传: %+v", body.Data.RosterSync)
	}

	// nil seam:零值安全(NewMeshHandler 生产构造不得 nil-panic)
	h2 := &MeshHandler{}
	w2 := httptest.NewRecorder()
	h2.HandleStatus(w2, httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("nil seam 应零值安全, got %d", w2.Code)
	}
}
```

- [x] **Step 2: 跑测试确认失败**(未知字段)

Run: `go test ./app/http/handlers/ -run TestHandleStatusRuntimeTruthFields -v`
Expected: FAIL(编译错:`unknown field beaconRunning`)

- [x] **Step 3: 实现**(mesh_handler.go)

`MeshHandler` 加三个 seam 字段(紧随既有四 seam):

```go
	beaconRunning func() bool
	lanRunning    func() bool
	rosterSync    func() services.MeshRosterSyncSnapshot
```

`NewMeshHandler()` 钉生产源:

```go
		beaconRunning: func() bool {
			if b := lanbeacon.Default(); b != nil {
				return b.Running()
			}
			return false
		},
		lanRunning: tunnel.MeshLANRunning,
		rosterSync: services.MeshRosterSyncStatus,
```

`meshStatusResponse` 尾部加三字段(注释注明 D2):

```go
	// 运行态真值三字段(D2 假绿治理):Enable/Effective 是配置推导,下面
	// 三个才是「活着」事实——开关开而 beacon_running=false 即装配假绿现场。
	BeaconRunning bool                           `json:"beacon_running"`
	LANRunning    bool                           `json:"lan_running"`
	RosterSync    services.MeshRosterSyncSnapshot `json:"roster_sync"`
```

`HandleStatus` 填充(`resp := meshStatusResponse{...}` 初始化块):

```go
		BeaconRunning: h.beaconRunning != nil && h.beaconRunning(),
		LANRunning:    h.lanRunning != nil && h.lanRunning(),
		RosterSync:    func() services.MeshRosterSyncSnapshot { if h.rosterSync != nil { return h.rosterSync() }; return services.MeshRosterSyncSnapshot{} }(),
```

(若嫌内联函数字面量丑,可拆三行 nil 守卫后赋值——执行者自定,保持 nil 安全即可。)

- [x] **Step 4: 跑测试确认通过 + handlers 包 mesh 全量**

Run: `go test ./app/http/handlers/ -run "TestHandleStatus|TestLANPeers|TestMesh" -v && go test ./app/http/handlers/ 2>&1 | tail -2`
Expected: PASS

### Task 5: forward-to-core 透传 + 56433 双注册 + parity 扩围

**Files:**
- Create: `app/http/handlers/mesh_forward_handler.go`
- Modify: `app/agentruntime/server.go`(registerAgentRoutes 末段追加)
- Modify: `app/agentruntime/route_parity_test.go`(两条 `regexp.MustCompile` 正则各扩 `api/mesh`,正则文本唯一)
- Test: `app/http/handlers/mesh_forward_handler_test.go`(新建)

- [x] **Step 1: 写透传失败测试**

```go
package handlers

// MeshCoreForwardHandler 透传契约(D3):真值住 core(56431),56433 侧
// 一律 forward-to-core;core 不可达=common.Error 不可达形态(HTTP 500,
// 与 proxyIfNeeded 一致),绝不本地伪答。
func TestMeshCoreForwardPassthrough(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != "v" {
			t.Errorf("query 未透传: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"beacon_running":true}}`))
	}))
	defer core.Close()

	h := NewMeshCoreForwardHandler()
	h.coreBase = func() string { return core.URL }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/status?k=v", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "beacon_running") {
		t.Fatalf("透传断裂: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMeshCoreForwardCoreDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 立刻关掉制造不可达

	h := NewMeshCoreForwardHandler()
	h.coreBase = func() string { return deadURL }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil))
	// 断言 500 而非 503:common.Error 走 ErrorCodeToHTTPStatus,业务码
	// CodeServiceUnavailable 映射为 HTTP 500——与 proxyIfNeeded 对
	// user-agent 不可达的既有形态逐字节一致(评审实锤 errors.go 映射表),
	// 下游消费者(runbook/AI 面)只需认一种不可达错误形状。
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("core 不可达应回 common.Error 映射的 500, got %d body=%s", w.Code, w.Body.String())
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/handlers/ -run TestMeshCoreForward -v`
Expected: FAIL(编译错:undefined NewMeshCoreForwardHandler)

- [x] **Step 3: 实现透传 handler**(新文件)

```go
package handlers

// MeshCoreForwardHandler 56433 侧 /api/mesh/* 透传(user-agent runtime
// 注册用)。mesh 数据面(beacon/装配)住 core 进程——56433 裸挂
// MeshHandler 会恒报 running=false+空候选(反向假红,违反 D2 治理原则),
// 故本端点一律 forward-to-core(56431)取真值。core 侧路由表(routes.go)
// 直挂 MeshHandler,本 handler 在 56431 不存在;即使单进程形态经 56433
// 命中,转发目标 56431 仍由 MeshHandler 直答,无转发环。
// 不可达错误走 common.Error(CodeServiceUnavailable→HTTP 500,与
// proxyIfNeeded 的下游不可达形态一致,不另造 503 形状)。
// D3 双注册纪律(2026-10-21/10-23/10-29 三次 404 透传事故同族)。
type MeshCoreForwardHandler struct {
	coreBase func() string
	client   *http.Client
}

func NewMeshCoreForwardHandler() *MeshCoreForwardHandler {
	return NewMeshCoreForwardHandlerWithBase(nil)
}

// NewMeshCoreForwardHandlerWithBase 注入式构造(生产接线用):coreBase 由
// 注册方(同包 ownerBaseURL)供给,解析链=Override > ALIANG_MANAGEMENT_ADDR
// > DefaultSessionOwnerAddr。质量评审 Important 修订:硬编码 127.0.0.1:56431
// 在 --host 定制绑定/ALIANG_MANAGEMENT_ADDR 部署 env 两场景会打错目标——
// 此链真修好这两场景;56433 进程 override 腿结构性恒空,回退端口/脑裂场景
// 解析落回默认 56431 与旧硬编码相同(残余账,挂计划文档)。nil=回退 loopback
// 默认(仅测试/单进程形态兜底)。
func NewMeshCoreForwardHandlerWithBase(coreBase func() string) *MeshCoreForwardHandler {
	if coreBase == nil {
		coreBase = func() string {
			return "http://127.0.0.1:" + strconv.Itoa(config.DefaultManagementPort)
		}
	}
	return &MeshCoreForwardHandler{
		coreBase: coreBase,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ServeHTTP 同路径透传(method/query 原样;method 闸归 core 侧 handler——
// core 对非 GET 已回 405,本层不再重复判)。不可达错误体走 common.Error
// 包裹形态(HTTP 500,见类型注释),与 agent_handler proxyIfNeeded 的
// 不可达语义一致。
func (h *MeshCoreForwardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := h.coreBase() + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		common.Error(w, common.CodeServiceUnavailable, "core process is not reachable", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
```

imports:`io`/`net/http`/`strconv`/`time` + `aliang.one/nursorgate/app/http/common` + `aliang.one/nursorgate/processor/config`。

- [x] **Step 4: 跑透传测试确认通过**

Run: `go test ./app/http/handlers/ -run TestMeshCoreForward -v`
Expected: PASS

- [x] **Step 5: parity 正则先扩围(此步后 parity 必红)**

`app/agentruntime/route_parity_test.go` 内两条 `regexp.MustCompile` 正则(正则文本 `(/(?:api/agent|api/proxy|mcp/aliang)[^"]*)` 全文件唯一,直接替换)改为 `(/(?:api/agent|api/proxy|api/mesh|mcp/aliang)[^"]*)`(两处同改)。

Run: `go test ./app/agentruntime/ -run TestAgentRouteParity -v`
Expected: FAIL——报两条 `/api/mesh/*` 仅注册在 core

- [x] **Step 6: 56433 侧补注册**(server.go `registerAgentRoutes` 内,`/api/proxy/ssh/test` 注册行之后)

```go
	// mesh 面端点(D3 双注册):数据面真值住 core,本侧一律 forward-to-core
	// (56431)取真值——裸挂 MeshHandler 会恒报 running=false 反向假红。
	// 方法前缀形态与 core 侧 register(..., http.MethodGet) 对齐;parity
	// 护栏正则已扩 api/mesh(缺任一侧=404 透传,2026-10-21 教训同族)。
	meshForward := handlers.NewMeshCoreForwardHandlerWithBase(ownerBaseURL)
	mux.HandleFunc("GET /api/mesh/lan_peers", meshForward.ServeHTTP)
	mux.HandleFunc("GET /api/mesh/status", meshForward.ServeHTTP)
```

- [x] **Step 7: parity 复跑确认绿**

Run: `go test ./app/agentruntime/ -run TestAgentRouteParity -v && go test ./app/agentruntime/ 2>&1 | tail -2`
Expected: PASS

### Task 6: D1 boot 接线(审计测试先行,TDD)

**Files:**
- Create: `cmd/boot_wiring_audit_test.go`
- Modify: `cmd/start.go:136` 后、`cmd/core.go:199` 后各加一行

- [x] **Step 1: 写审计测试(此刻必红——roster 接线不存在)**

```go
package cmd

// boot 接线审计(接线孤儿治理):源码级断言 mesh 装配族三函数在两个 boot
// 形态(start.go 网关直跑/core.go daemon)各接线一次。运行时看不见没被
// 被调用的代码,「本体在、接线断」族(start.go 漏接线=第一例、utun7 吞
// 广播=第二例、StartMeshRosterSync 零调用方=第三例)只能在构建期拦。
// 手法与 route_parity_test.go 同款:剥注释行后精确计数。
func TestBootMeshWiringAudit(t *testing.T) {
	cases := []struct{ file, symbol string }{
		{"start.go", "tunnel.StartMeshLANAssembly("},
		{"start.go", "tunnel.WireMeshAutoPeerProbe("},
		{"start.go", "services.StartMeshRosterSync("},
		{"core.go", "tunnel.StartMeshLANAssembly("},
		{"core.go", "tunnel.WireMeshAutoPeerProbe("},
		{"core.go", "services.StartMeshRosterSync("},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(trimmed, c.symbol) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("boot 接线审计失败: %s 中 %s 出现 %d 次(应恰 1 次;0=接线孤儿,>1=重复接线)", c.file, c.symbol, n)
		}
	}
}
```

imports:`os`/`strings`/`testing`。

- [x] **Step 2: 跑测试确认红**

Run: `go test ./cmd/ -run TestBootMeshWiringAudit -v`
Expected: FAIL——`start.go 中 services.StartMeshRosterSync( 出现 0 次`

- [x] **Step 3: 补接线**

`cmd/start.go:136`(`WireMeshAutoPeerProbe` 行)之后:

```go
	// roster 同步循环接线(接线孤儿治理,第三例):本函数实现早已存在
	// (mesh_roster_sync.go,注释声称 boot 两形态各调一次),但两处 boot
	// 均漏接——roster 缓存永不填充,MeshRosterEntryFor 恒 miss,auto_peers
	// 恒 fail-closed。sync.Once 幂等 + Mesh==nil 自守卫,失败仅日志不阻断。
	services.StartMeshRosterSync()
```

`cmd/core.go:199` 之后同款(注释可精简为「同 start.go:接线孤儿治理第三例」+一行说明)。

- [x] **Step 4: 审计复跑确认绿 + cmd 包全量**

Run: `go test ./cmd/ -run TestBootMeshWiringAudit -v && go test ./cmd/ 2>&1 | tail -2`
Expected: PASS

### Task 7: mesh 域 20 处 log.Printf 收编

**Files(全部 Modify,无新建):**
- `app/mesh/lanbeacon/beacon.go:459`
- `app/mesh/punch/listener.go:173`
- `app/tunnel/mesh_assembly.go:108,178,309,315,489`
- `app/tunnel/piko_logger.go:52,61`
- `app/tunnel/bridge.go:67,70,73,98,101`
- `app/tunnel/mesh_dial.go:92,94`
- `app/tunnel/manager.go:308,376,401,425`

- [x] **Step 1: 逐文件替换**

统一手法:`log.Printf("FMT", args...)` → `logger.Info(fmt.Sprintf("FMT", args...))`。`[MESH-*]`/`[PUNCH-*]`/`[AGENT-TCP]`/`[AGENT-TUNNEL]` 前缀与全部格式参数**原样保留**(前端/runbook 依赖该 token)。manager.go:376/401 的 `func() any` 参数是**立即执行闭包**(评审实锤),`fmt.Sprintf` 原样照搬 args 即可,输出不变。

- [x] **Step 2: import 卫生**

每文件:删 `log` import(若文件内已无其他 `log.` 用法——逐文件 `grep -n "log\." <file>` 确认),加 `"aliang.one/nursorgate/common/logger"`;缺 `fmt` 的补 `fmt`。`app/mesh` 两文件(beacon.go/listener.go)当前不依赖 logger,评审已核 common/logger 只依赖 stdlib/sentry/lumberjack,无 import 环。

- [x] **Step 3: 收编验证测试**(buffer API 已按评审实锤写死:`LogBuffer` 无 `Reset` 有 `Clear`;`logger.Info` 经 `mainLogger.logf` 以 `Level=INFO, Source="main"` 落 buffer,`DefaultLogConfig.Level=DEBUG` 不过滤;`LogEntry` 消息字段名 `Message`)

在 `app/tunnel/piko_logger_test.go`(存在则追加)加:

```go
func TestReportDisabledLinesEnterLogBuffer(t *testing.T) {
	logger.GetGlobalBuffer().Clear()
	// 两函数首参是 state 回调,实现体内以 `_` 忽略——传 nil 安全(评审实锤
	// 签名:func reportTCPDisabled(_ func(state string), message string))。
	reportTCPDisabled(nil, "unit-test")
	reportMeshRelayDisabled(nil, "unit-test")
	entries := logger.GetBufferEntries(50, logger.INFO, "main") // 与 /api/logs handler 同口径
	joined := ""
	for _, e := range entries {
		joined += e.Message
	}
	if !strings.Contains(joined, "tcp bridge disabled: unit-test") ||
		!strings.Contains(joined, "mesh relay disabled: unit-test") {
		t.Fatalf("收编后应进 buffer: %s", joined)
	}
}
```

(注意:此测试须在 Step 1 替换 `piko_logger.go` 两处**之后**才有意义——替换前两函数走 stdlib log 不进 buffer,测试天然红,正是收编的 TDD 信号。)

- [x] **Step 4: 跑验证**

Run: `go build ./... && go test ./app/tunnel/ ./app/mesh/... 2>&1 | tail -5 && grep -rn "log\.Printf" --include="*.go" app/mesh/ app/tunnel/ | grep -v "_test.go" | wc -l`
Expected: build OK;测试绿(预存账 TestAssemblySeamsPunchThreeStates 同态失败不计);`wc -l` = **0**

### Task 8: 全量验证

- [x] **Step 1: 全量构建 + 受影响包全测**

Run: `go build ./... && go test ./app/mesh/... ./app/tunnel/ ./app/http/handlers/ ./app/http/services/ ./app/agentruntime/ ./cmd/... 2>&1 | tail -12`
Expected: 全绿,唯 `app/tunnel` 的 `TestAssemblySeamsPunchThreeStates` 保持预存失败(与 master 同态,三子项相同报错)

- [x] **Step 2: 预存账同态核对**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate && go test ./app/tunnel/ -run TestAssemblySeamsPunchThreeStates 2>&1 | tail -2; cd - >/dev/null; go test ./app/tunnel/ -run TestAssemblySeamsPunchThreeStates 2>&1 | tail -2`
Expected: 主树与 worktree 失败输出一致(本批未改其语义)

- [x] **Step 3: 验收清单复核**

- `go test ./app/agentruntime/ -run "TestAgentRouteParity" -v` PASS(mesh 入护栏)
- `go test ./cmd/ -run TestBootMeshWiringAudit -v` PASS(接线审计)
- `go test ./app/http/handlers/ -run "TestMesh" -v` PASS(status 真值 + 透传)
- `grep -rn "log.Printf" --include="*.go" app/mesh/ app/tunnel/ | grep -v _test | wc -l` = 0

### Task 9: 提交(⚠️ 全仓铁律:仅在用户明确同意后执行)

提交前跑 `git status` + `git diff` 全览;五笔提交,全中文「新增:/修复:」前缀,HEREDOC 多行,每笔带 CLAUDE.md 规定的 footer(Generated with Claude Code + Co-Authored-By)。用户放行后才逐笔执行:

1. `修复:mesh roster sync 孤儿接线——boot 两形态补 StartMeshRosterSync(第三例「本体在、接线断」,auto_peers 信任锚恢复)+接线审计测试`
2. `新增:/api/mesh/status 运行态真值三字段(beacon_running/lan_running/roster_sync 三态)——灭配置推导假绿`
3. `修复:mesh 端点双注册——56433 forward-to-core 透传+parity 护栏扩围 api/mesh`
4. `修复:mesh 域 20 处 stdlib log.Printf 收编进 /api/logs buffer`
5. `新增:阶段0 组网诊断地基设计文档+实施计划随批入仓`
