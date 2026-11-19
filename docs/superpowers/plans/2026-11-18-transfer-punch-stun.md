# 文件传输 punch 直连腿 + STUN 可配置化 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** peer 文件传输上传腿获得 UDP 打洞直连载体(LAN→punch→COS),并把打洞 STUN 服务器从硬编码 Google 换成可配置、默认国内可达档。

**Architecture:** STUN 列表挂 `customer.mesh.stun_servers`(默认档常量住 config 层,服务编排读配置);传输 punch 腿复用打洞一期全部内核(信令三帧/门禁/SessionCache),`peer.punch.session` 帧加 `purpose` 字段做会话目的路由,lan_push 上传协议体拆层后由 punch 流直接承载;PhoneServer 在 upload 帧补 `punch_peer_id`(发送方据此寻址接收方发起打洞信令)。

**Tech Stack:** Go(quic-go/pion/stun)、Vue2 风格 options 组件 + vitest、AliangPhoneServer(Node/TS + vitest)。

**Spec:** `docs/superpowers/specs/2026-11-18-transfer-punch-stun-design.md`

---

## 计划期裁决(相对 spec 的四处修订,实施前生效)

1. **预热 → 预算吸收**。spec §2.2 的「接收方并行预热」在计划期发现需要接收侧新增生命周期表+handleSession 接线,而收益仅限「同对端首次冷缓存传输」(SessionCache 5min 复用吸收后续)。改为:**传输腿拨号预算加长至 12s**(`agentTransferPunchBudget`),覆盖 B 侧 bind+classify 最坏 ~6s + 正常拨号;预热移入遗留账。诚实代价:STUN 全死的最坏路径上,传输落 COS 前多等 ≤17.5s(信令围栏 10s+5s+拨号 2.5s,均为既有常量)。
2. **STUN 默认档搬家**。spec 原文「`DefaultSTUNServers` 保留为默认档常量」违反分层(processor/config 不能 import app/mesh/punch)。改为:默认档常量 `MeshDefaultStunServers` 定义在 `processor/config/types.go`,`punch.DefaultSTUNServers` **删除**(消费面只有 services 两处+stun_test,全部改道)。
3. **`punch_peer_id` 服务端字段**。计划期实锤:`parseAgentTransferIdentity` 无目标设备身份,发送方拿不到打洞信令的寻址目标。AliangPhoneServer 的 `buildTransferUploadPayload` 加顶层可选字段 `punch_peer_id: row.targetDeviceId`(老 agent 零感知;字段缺省=纯 COS 语义不变)。
4. **会话缓存复合键**。计划期实锤:SessionCache/sockets 表按裸 peerID 键,mesh 会话与传输会话同 peer 混用会互相喂错 handler(缓存命中的 mesh 会话把传输数据流喂给 sshin)。解法:传输腿键=`"transfer:"+peerID`,mesh 键形态不变;skipMemo 保持裸 peerID(NAT 属性按对端不按用途)。

---

## Task 0: 工作区与规格修订

**Files:**
- Create: worktree `../alianggate-transfer-punch`(分支 `feat/transfer-punch-stun`,基于 master)
- Create: worktree `../AliangPhoneServer-tp`(分支 `feat/transfer-punch-peer-id`,基于 main)——仓库根 `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`
- Modify: `docs/superpowers/specs/2026-11-18-transfer-punch-stun-design.md`(六处修订见 Step 3:§2.2 预热改预算吸收、§2.1 STUN 默认档搬家、§2.2 punch_peer_id 与复合缓存键补注、writeJSON 捕获口径、§4 遗留账加预热条)

- [ ] **Step 1: 建 alianggate worktree**(项目铁律:所有开发一律 worktree)

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-transfer-punch -b feat/transfer-punch-stun master
```

- [ ] **Step 2: 建 PhoneServer worktree**

```bash
cd /Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer
git worktree add ../../vibe_on_phone/AliangPhoneServer-tp -b feat/transfer-punch-peer-id main
```

注意:PhoneServer worktree 需要能跑 vitest(node_modules)——若新 worktree 缺 node_modules,`cd` 过去 `npm ci`(或从主仓软链,沿项目先例)。

- [ ] **Step 3: spec 修订**(四处裁决逐条落 spec,外加一条口径注记):
  1. §2.2「预热」条目整条替换为:「拨号预算吸收:传输腿 `agentTransferPunchBudget=12s` 覆盖 B 侧 bind+classify 最坏时延;预热移入遗留账(收益仅冷缓存首传,SessionCache 复用吸收后续)」;
  2. §2.1「`DefaultSTUNServers` 保留为默认档常量」句改为「默认档常量 `MeshDefaultStunServers` 住 `processor/config/types.go`(分层:processor 不得反向依赖 app),`punch.DefaultSTUNServers` 删除」;
  3. §2.2「A 侧拨号」条目补:「目标 device_id 由 PhoneServer upload 帧顶层可选键 `punch_peer_id` 下发(计划期实锤:upload 帧原不带目标身份);PhoneServer 侧一并改(本计划 Task 11)」;§3 的「server 直接下发候选不可行」句补注「server 下发的是寻址 ID,UDP 公网映射候选仍必须真 STUN 探测——该边界不变」;
  4. §2.2「cache-first 通道与 AgentPeerPunchDial 同源」句补:「会话缓存/sock 表键带用途前缀(`transfer:<peerID>`),mesh 与传输会话互不交叉(计划期实锤:同 peer 混缓存会喂错 handler)」;
  5. §2.2「传输路由的 WS writeJSON 经 agentPunchFrameWriter() 前例」句改为「writeJSON 取 listen 帧时刻的 writer 捕获(断连后进度帧静默丢,非致命;result 真值走流内 ACK 与上传链 HTTP 双通道)」;
  6. §4 遗留账追加:「传输腿预热(接收方并行 punch 会话)——收益仅冷缓存首传,被预算吸收替代;二期看真机数据再议」。

- [ ] **Step 4: Commit(spec 修订)**

```bash
cd ../alianggate-transfer-punch
git add docs/superpowers/specs/2026-11-18-transfer-punch-stun-design.md
git commit -m "文档:transfer-punch-stun spec 计划期修订(预热改预算吸收/STUN 默认档搬家/punch_peer_id)"
```

---

## Feature A:STUN 可配置化

### Task 1: MeshConfig.StunServers 字段+校验+默认档常量

**Files:**
- Modify: `processor/config/types.go`(MeshConfig 结构体 ~847、Validate ~991、Effective 区 ~905)
- Test: `processor/config/mesh_punch_config_test.go`(追加)

- [ ] **Step 1: 写失败测试**(追加到 mesh_punch_config_test.go 末尾)

```go
// —— stun_servers(transfer-punch-stun 批次)——

func TestMeshStunServersEffective(t *testing.T) {
	// (a) 空/nil 回落默认档(拷贝,调用方改切片不污染常量)。
	var nilMesh *MeshConfig
	got := nilMesh.EffectiveStunServers()
	if len(got) != len(MeshDefaultStunServers) {
		t.Fatalf("nil mesh EffectiveStunServers 应回落默认档, got %v", got)
	}
	got[0] = "mutated"
	if MeshDefaultStunServers[0] == "mutated" {
		t.Fatal("EffectiveStunServers 必须返回拷贝,不得暴露底层常量")
	}
	empty := &MeshConfig{}
	if len(empty.EffectiveStunServers()) != len(MeshDefaultStunServers) {
		t.Fatal("空配置 EffectiveStunServers 应回落默认档")
	}
	// (b) 配置非空原样返回(TrimSpace 在 Validate 就地做过)。
	cfg := &MeshConfig{StunServers: []string{"stun.example.com:3478"}}
	if got := cfg.EffectiveStunServers(); len(got) != 1 || got[0] != "stun.example.com:3478" {
		t.Fatalf("配置非空应原样返回, got %v", got)
	}
}

func TestMeshStunServersValidate(t *testing.T) {
	// (a) 合法:TrimSpace 就地规整后通过。
	ok := &MeshConfig{StunServers: []string{"  stun.example.com:3478  ", "stun2.example.com:3478"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法 stun_servers 不应报错: %v", err)
	}
	if ok.StunServers[0] != "stun.example.com:3478" {
		t.Fatalf("Validate 应就地 TrimSpace, got %q", ok.StunServers[0])
	}
	// (b) 非 host:port 拒。
	bad := &MeshConfig{StunServers: []string{"stun.example.com"}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "stun_servers[0]") {
		t.Fatalf("非 host:port 应报错含 stun_servers[0], got %v", err)
	}
	// (c) 超 4 条拒。
	over := &MeshConfig{StunServers: []string{"a:1", "b:2", "c:3", "d:4", "e:5"}}
	if err := over.Validate(); err == nil || !strings.Contains(err.Error(), "上限 4") {
		t.Fatalf("超 4 条应报错含上限, got %v", err)
	}
}
```

注意:若该测试文件未 import `strings`,补 import。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/config/ -run 'TestMeshStunServers' -v`
Expected: FAIL(EffectiveStunServers/MeshDefaultStunServers 未定义,编译错误)

- [ ] **Step 3: 实现**(types.go)

MeshConfig 结构体 PunchEnable 字段后追加:

```go
	// StunServers 打洞 STUN 服务器列表(transfer-punch-stun 批次)。空/缺 =
	// 默认档 MeshDefaultStunServers;上限 4 条,host:port 形态(Validate 就地
	// TrimSpace 规整,与 peers[].lan_addrs 同款纪律)。
	StunServers []string `json:"stun_servers,omitempty"`
```

Effective 区(EffectivePunchEnabled 之后)追加:

```go
// MeshDefaultStunServers 打洞 STUN 默认档(transfer-punch-stun 批次,2026-11-18
// 本机家宽真 STUN Binding 实测定稿):miwifi 国内可达(111.206.174.2 try1 即答);
// Google 机会性副档(国内裸网常不可达,可达时补足双台判型)。自托管 coturn
// 部署后应配置进 customer.mesh.stun_servers 首位(部署手册见 spec §2.3)。
// 默认档住 config 层而非 punch 包:processor 不得反向依赖 app(punch.Default
// STUNServers 已删,消费面见 services agentPunchConfiguredServers)。
var MeshDefaultStunServers = []string{"stun.miwifi.com:3478", "stun.l.google.com:19302"}

const meshStunServersMax = 4

// EffectiveStunServers 打洞判型/映射发现的服务器列表:配置非空返回配置拷贝,
// 空/nil(含 nil receiver)回落默认档拷贝——调用方改切片不得污染常量与配置。
func (c *MeshConfig) EffectiveStunServers() []string {
	if c == nil || len(c.StunServers) == 0 {
		return append([]string(nil), MeshDefaultStunServers...)
	}
	return append([]string(nil), c.StunServers...)
}
```

Validate()(MaxHops 校验之后、peers 循环之前)追加:

```go
	if len(c.StunServers) > meshStunServersMax {
		return fmt.Errorf("customer.mesh.stun_servers 上限 %d 条, got %d", meshStunServersMax, len(c.StunServers))
	}
	for i := range c.StunServers {
		// 与 peers[].lan_addrs 同款就地规整:校验与落盘同一份值。
		c.StunServers[i] = strings.TrimSpace(c.StunServers[i])
		if _, _, err := net.SplitHostPort(c.StunServers[i]); err != nil {
			return fmt.Errorf("customer.mesh.stun_servers[%d] invalid %q: must be host:port", i, c.StunServers[i])
		}
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/config/ -run 'TestMeshStunServers' -v`
Expected: PASS(2 用例)

- [ ] **Step 5: Commit**

```bash
git add processor/config/types.go processor/config/mesh_punch_config_test.go
git commit -m "新增:customer.mesh.stun_servers 可配置 STUN 列表(默认档国内可达,上限4,保存期校验)"
```

### Task 2: punch.DefaultSTUNServers 删除+services 改读配置

**Files:**
- Modify: `app/mesh/punch/stun.go:20-35`(删默认档常量与 DefaultSTUNServers)
- Modify: `app/http/services/agent_peer_punch.go`(newAgentPeerPunchB:175、newAgentPeerPunchA:583)
- Modify: `app/mesh/punch/stun_test.go`(DefaultSTUNServers 引用改字面量)
- Test: `app/http/services/agent_peer_punch_test.go`(追加 servers 取值用例;若该文件无 servers 用例先例,新建小用例挂既有测试风格)

- [ ] **Step 1: 写失败测试**(agent_peer_punch_test.go 追加)

```go
// agentPunchConfiguredServers 读配置取 STUN 列表:配置空回落默认档,配置了
// 原样返回(transfer-punch-stun 批次)。
func TestAgentPunchConfiguredServers(t *testing.T) {
	// 全局配置摆置沿 agent_ai_agentcore_skills_test.go:104 同款:
	// SetGlobalConfig + t.Cleanup 回滚(仓内无 swapGlobalMeshConfig 这类现成缝)。
	withCfg := func(t *testing.T, mesh *config.MeshConfig) {
		old := config.GetGlobalConfig()
		config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{Mesh: mesh}})
		t.Cleanup(func() { config.SetGlobalConfig(old) })
	}
	withCfg(t, &config.MeshConfig{}) // 空 mesh → 回落默认档
	got := agentPunchConfiguredServers()
	if len(got) != 2 || got[0] != "stun.miwifi.com:3478" {
		t.Fatalf("空配置应回落默认档, got %v", got)
	}
	withCfg(t, &config.MeshConfig{StunServers: []string{"stun.custom:3478"}})
	if got := agentPunchConfiguredServers(); len(got) != 1 || got[0] != "stun.custom:3478" {
		t.Fatalf("配置了应原样返回, got %v", got)
	}
}
```

摆置形态若与 skills_test 既有写法有出入(如 old 为 nil 的回滚语义),以既有用例为准照抄,断言不变。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentPunchConfiguredServers -v`
Expected: FAIL(agentPunchConfiguredServers 未定义)

- [ ] **Step 3: 实现**

agent_peer_punch.go(agentPunchConfigEnabled 附近)追加:

```go
// agentPunchConfiguredServers 生产 STUN 服务器取值:customer.mesh.stun_servers
// 读侧(空回落 MeshDefaultStunServers,EffectiveStunServers 语义)。闭包逐调用
// 回读全局配置——保存即生效,不缓存(工厂 CacheKey 派生式置位同款哲学)。
func agentPunchConfiguredServers() []string {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil {
		// 与 EffectiveStunServers 同款拷贝纪律:不外泄共享常量切片。
		return append([]string(nil), config.MeshDefaultStunServers...)
	}
	return cfg.Customer.Mesh.EffectiveStunServers()
}
```

`newAgentPeerPunchB` 的 `servers: punch.DefaultSTUNServers,` → `servers: agentPunchConfiguredServers,`;`newAgentPeerPunchA` 同改。

stun.go:删除 `stunServerPrimary/stunServerSecondary/defaultSTUNServers/DefaultSTUNServers`(20-35 区),文件头注释补一行「默认服务器档已迁 processor/config.MeshDefaultStunServers(分层:processor 不得反向依赖 app);本包只做 Discover/Classify 原语」。stun_test.go 中 DefaultSTUNServers 引用改字面量 `[]string{"stun.miwifi.com:3478", "stun.l.google.com:19302"}`(先 grep 确认引用处)。

⚠️ 默认档第二台是 **`stun.l.google.com`(主域)**,与被删的旧常量 `stun1.l.google.com`(次域)**有意不同**(spec §1 拍板的主域机会性副档)——勿「顺手改回」stun1。

- [ ] **Step 4: 全包回归(防漏改引用)**

Run: `go build ./... && go test ./app/mesh/punch/ ./app/http/services/ -count=1`
Expected: BUILD OK,全部 PASS(若有漏网 DefaultSTUNServers 引用,此步现形)

- [ ] **Step 5: Commit**

```bash
git add app/mesh/punch/stun.go app/mesh/punch/stun_test.go app/http/services/agent_peer_punch.go app/http/services/agent_peer_punch_test.go
git commit -m "修复:STUN 服务器改读配置(punch.DefaultSTUNServers 删除,默认档迁 config 层)"
```

### Task 3: mesh status 面回读 stun_servers

**Files:**
- Modify: `app/http/handlers/mesh_handler.go`(meshStatusResponse ~69、HandleStatus ~99)
- Test: `app/http/handlers/mesh_handler_test.go`(若不存在则新建,沿 handlers 包既有 handler 测试形态;先 `ls app/http/handlers/ | grep mesh`)

- [ ] **Step 1: 写失败测试**:构造带 `Mesh{StunServers: []string{"stun.x:1"}}` 的全局配置(swap 缝或 handler config 注入缝——NewMeshHandler 的 `config` 字段就是注入缝,直接构造 `&MeshHandler{config: func() *config.Config {...}}`),GET /api/mesh/status,断言 `stun_servers` 数组含该值;空配置断言回落默认档两值。

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现**:

meshStatusResponse 追加 `StunServers []string \`json:"stun_servers"\``(零值 nil 数组;resp 初始化处给 `StunServers: []string{}` 保持「恒数组」家族口径);HandleStatus 的 `Mesh != nil` 块内 `resp.StunServers = mesh.EffectiveStunServers()`。

- [ ] **Step 4: 跑测试确认通过** → **Step 5: Commit**

```bash
git add app/http/handlers/mesh_handler.go app/http/handlers/mesh_handler_test.go
git commit -m "新增:/api/mesh/status 回读 stun_servers(默认档可见,前端数据源)"
```

### Task 4: MeshSettings 前端(stun_servers 归一+punch 开关+输入+启发式)

**Files:**
- Modify: `app/website/src/components/settings/MeshSettings.js`(normalizeMeshConfig)
- Modify: `app/website/src/components/settings/MeshSettings.vue`(开关+textarea+meshState 启发式 ~477)
- Modify: `app/website/src/i18n/zh.js`、`en.js`(新键)
- Test: `app/website/src/components/settings/MeshSettings.test.js`
- Test: `app/website/tests/e2e/mesh-settings.spec.js`

- [ ] **Step 1: js 归一失败测试**(MeshSettings.test.js 追加,沿文件既有 describe/expect 形态):

```js
it('normalizeMeshConfig 归一 stun_servers(TrimSpace/剔空/非数组归空)', () => {
  expect(normalizeMeshConfig({ stun_servers: ['  a:1 ', '', 'b:2'] }).stun_servers).toEqual(['a:1', 'b:2']);
  expect(normalizeMeshConfig({}).stun_servers).toEqual([]);
  expect(normalizeMeshConfig({ stun_servers: 'x' }).stun_servers).toEqual([]);
});
```

- [ ] **Step 2: 跑失败**:`cd app/website && npx vitest run src/components/settings/MeshSettings.test.js`(⚠️ 必须在 app/website 目录跑,仓库根跑会吞假警报——项目既知坑)

- [ ] **Step 3: js 实现**(normalizeMeshConfig 的 fallback 行后追加):

```js
    // stun_servers(transfer-punch-stun):TrimSpace/剔空;非数组(老配置/脏
    // 数据)归空数组=回落后端默认档。读侧恒产出数组,保存 patch 不缺键。
    stun_servers: Array.isArray(source.stun_servers)
      ? source.stun_servers.map((v) => String(v ?? '').trim()).filter(Boolean)
      : [],
```

文件头线格式注释同步加 `stun_servers`。

- [ ] **Step 4: vue 实现**:⚠️ MeshSettings.vue **没有既有开关块可镜像**(mesh 开关已迁 proxy 卡,本文件只有文本输入;punch_enable 在 vue 中零出现)——开关控件形态从 `AgentCoreSettings.vue` / `AgentMCPRegistrySettings.vue` 的既有开关块取同一形态(模板+样式+disabled 联动)。新增:①punch_enable 开关(数据源 `configMesh.punch_enable`,TODOS ⑤ 一并收敛);②STUN 列表 textarea(每行一条,v-model 中转数组⇄多行文本,保存前 `split('\\n')` 归一;空=用默认档的 hint 说明);③`meshState()` 的 unconfigured 行(~477,现有唯一 forward_enable 引用处)补 `&& !this.meshStatus.punch_enable`。i18n 四键(zh/en 同步):`mesh_punchEnable`、`mesh_stunServers`、`mesh_stunServersHint`(「留空使用默认档(stun.miwifi.com / Google),自建 coturn 填 host:port 每行一条」)、`mesh_punchEnableHint`。

- [ ] **Step 5: vue/e2e 断言追加**:MeshSettings.test.js 加 punch 开关与 stun_servers 保存透传断言(沿既有 punch_enable 测试 ~13 处的摆置);e2e mesh-settings.spec.js 沿既有 punch_enable 断言(210-216 区「未变开关不产伪 diff」)加 stun_servers 同款用例。

- [ ] **Step 6: 全绿**(两套 runner,勿混:tests/e2e 是 **Playwright** 套件,vitest 不认)

Run: `cd app/website && npx vitest run src/components/settings/MeshSettings.test.js`
Run: `cd app/website && npx playwright test tests/e2e/mesh-settings.spec.js`(或 `npm run test:e2e -- tests/e2e/mesh-settings.spec.js`,沿 package.json script 口径)
Expected: 两条全 PASS

- [ ] **Step 7: Commit**

```bash
git add app/website/src/components/settings/MeshSettings.js app/website/src/components/settings/MeshSettings.vue app/website/src/components/settings/MeshSettings.test.js app/website/tests/e2e/mesh-settings.spec.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "新增:组网卡 punch 开关+STUN 列表输入(归一/透传/unconfigured 启发式收口 TODOS⑤)"
```

---

## Feature B:传输 punch 直连腿(alianggate)

### Task 5: dialMiss 变体输入+复合缓存键+传输拨号入口

**Files:**
- Modify: `app/http/services/agent_peer_punch.go`(dialSource/dialMiss ~631-690、结构体字段、AgentPeerPunchDial 导出面 ~942)
- Test: `app/http/services/agent_peer_punch_test.go`(追加)

- [ ] **Step 1: 写失败测试**(同包测试,注入缝齐全;沿既有 dialMiss 测试的摆置):

```go
// 传输腿复合键:同 peer 的 mesh 会话与传输会话在缓存/sock 表互不可见——
// mesh 会话喂传输流=喂错 handler,反之亦然(计划期裁决 4)。
func TestAgentPunchTransferCacheKeyIsolation(t *testing.T) {
	a := newAgentPeerPunchA()
	key := punchCacheKey("dev-1", "transfer")
	if key == "dev-1" {
		t.Fatal("transfer 键必须带前缀,不得与 mesh 键同形")
	}
	if punchCacheKey("dev-1", "") != "dev-1" {
		t.Fatal("mesh 键形态不变(裸 peerID)")
	}
	_ = a // 结构体级断言在集成测试覆盖;此处锁键形态契约
}
```

- [ ] **Step 2: 跑失败**(`go test ./app/http/services/ -run TestAgentPunchTransferCacheKeyIsolation -v` → 编译错)

- [ ] **Step 3: 实现**(agent_peer_punch.go):

```go
// punchDialOpts 是拨号链的变体输入:mesh 语义全零值;传输腿带加长预算与
// purpose/transferID(会话帧捎带,B 侧按 purpose 路由过门流)。
type punchDialOpts struct {
	budget     time.Duration // DialPeer 预算;0=punch.DialBudget
	purpose    string        // ""=mesh;"transfer"=文件传输腿
	transferID string        // purpose=transfer 时必填(信令寻址+接收侧查表)
}

// punchCacheKey 会话缓存/sock 表键(计划期裁决 4):mesh 沿用裸 peerID(存量
// 键形态不变);transfer 加前缀——同 peer 两用途的会话绝不交叉复用,否则
// 缓存命中会把数据流喂给对端的错误 handler。skipMemo 保持裸 peerID(NAT
// 属性按对端,与用途无关)。
func punchCacheKey(peerID, purpose string) string {
	if purpose == "" {
		return peerID
	}
	return purpose + ":" + peerID
}

// agentTransferPunchBudget 传输腿拨号预算:覆盖 B 侧 bind+双 STUN 判型最坏
// ~6s + 正常拨号,替代预热(spec 计划期裁决 1);耗尽落 COS 由上传链兜底。
const agentTransferPunchBudget = 12 * time.Second

// AgentPeerPunchTransferDial 文件传输打洞拨号入口(上传腿 punch 载体):
// transfer purpose 会话+加长预算;门禁/信令/缓存与 mesh 同链,仅键与
// 会话目的不同。失败恒错误,由上传链落 COS。
func AgentPeerPunchTransferDial(ctx context.Context, peerID, transferID string) (net.Conn, error) {
	return sharedAgentPeerPunchA().dialSourceOpts(ctx, peerID, punchDialOpts{
		budget:     agentTransferPunchBudget,
		purpose:    "transfer",
		transferID: transferID,
	})
}
```

结构改造:`dialSource(ctx, peerID)` 保持签名(mesh 注册缝不变),体改 `return a.dialSourceOpts(ctx, peerID, punchDialOpts{})`;新增 `dialSourceOpts`;`dialMiss(ctx, peerID)` → `dialMiss(ctx, peerID, opts punchDialOpts)`,内部:tryCached/dialMiss 的 `peerID` 键改 `key := punchCacheKey(peerID, opts.purpose)`(cache Acquire/Put、sockets 表、旧 socket 驱逐全用 key);skipMemo `ShouldSkip/Record` 保持裸 peerID;session 帧组装加:

```go
	if opts.purpose == "transfer" {
		if opts.transferID == "" {
			return nil, errors.New("agent punch: transfer purpose 缺 transfer_id,fail-closed")
		}
		framePurposed(&frame, opts) — 或直接在 map 字面量后:
	}
```

实际写法:session 帧 map 字面量保持既有字段,追加:

```go
	// 会话目的(transfer-punch-stun):缺省=mesh 语义向后兼容;transfer 带
	// transfer_id 供 B 侧查接收上下文。target_device_id 路由信封不变。
	"purpose":     purposeOrMesh(opts.purpose),
	"transfer_id": opts.transferID,
```

其中 `purposeOrMesh` 实现(3 行,显式化便于 B 侧单分支判读;老 B 侧忽略未知字段,向后兼容仍成立——契约测试若枚举字段须同步,见 Task 10 Step 1):

```go
// purposeOrMesh 缺省 purpose 显式化为 "mesh"(B 侧单分支判读;帧面语义:
// 老版本 A 侧发的无 purpose 帧 = mesh)。
func purposeOrMesh(purpose string) string {
	if purpose == "" {
		return "mesh"
	}
	return purpose
}
```

**预算接线(勿漏)**:dialMiss 末尾 DialPeer 组装处显式 `punch.DialOpts{ ..., Budget: opts.budget }`——漏接=传输腿静默退回 2.5s 默认预算,裁决 1(预算吸收)整个失效。

- [ ] **Step 4: 全链回归**

Run: `go test ./app/http/services/ -run 'Punch' -count=1 -v && go build ./...`
Expected: PASS(既有 punch 用例全绿=mesh 语义零回归)

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_punch.go app/http/services/agent_peer_punch_test.go
git commit -m "新增:传输腿打洞拨号入口(punchDialOpts/复合缓存键防串流/12s 加长预算)"
```

### Task 6: lan_push 协议体拆层(收发两侧)

**Files:**
- Modify: `app/http/services/agent_peer_lan.go`(agentLanPushServeOnce:380、attemptAgentTransferLANPush:600)
- Test: `app/http/services/agent_peer_lan_push_test.go`(追加:协议体在裸 net.Conn(net.Pipe)上行为与 TLS 路径等价)

- [ ] **Step 1: 写失败测试**:用 `net.Pipe()` 两端直接喂 `serveAgentLanPushConn`/`pushAgentLanStream`(无 TLS),走完 hello→ok→字节流→ACK 全程,断言落盘 sha/size 与 ACK 一致。inbox 根替换用既有缝 `newAgentTransferEnv(t)`(设 `ALIANG_TRANSFER_INBOX_DIR`,`agent_transfer_test.go:36`)——`lanServeTestServer` 是 ssh-relay 路径夹具、不换 inbox,勿用。

- [ ] **Step 2: 跑失败** → **Step 3: 实现**:

**接收侧**:`agentLanPushServeOnce` 拆成 TLS 包装(现状 ~381-387 行:函数开头 defer/deadline+tls.Server+Handshake 段)+ `serveAgentLanPushConn(secConn net.Conn, expectedToken, expectedTransferID, filename, targetDir string, maxBytes int64, writeJSON func(interface{}) error)`(其后协议体原样搬移,`tlsConn` 改名 `secConn`;签名带齐上下文,不再依赖闭包捕获)。文件头注释补一行「punch 路径直接喂协议体(punch 流自带 TLS,禁再包一层)」。

**发送侧**:`attemptAgentTransferLANPush` 拆成 拨号+TLS(605-620 行)+ `pushAgentLanStream(ctx context.Context, secConn net.Conn, ident agentTransferIdentity, token string, sourcePath string, totalSize int64, writeJSON func(interface{}) error) (int64, string, string, error)`(621-714 行原样搬移;`tlsConn` 改 `secConn`)。`attemptAgentTransferLANPush` 变薄壳,行为逐字节不变。

- [ ] **Step 4: 全绿**:`go test ./app/http/services/ -run 'LanPush|Transfer' -count=1`
- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_lan.go app/http/services/agent_peer_lan_push_test.go
git commit -m "重构:lan_push 上传协议体拆层(载体无关,TCP/punch 双路径共用)"
```

### Task 7: 接收侧传输上下文注册表+purpose 路由

**Files:**
- Modify: `app/http/services/agent_peer_lan.go`(handleAgentPeerLanPushListen:525 追加注册)
- Modify: `app/http/services/agent_peer_punch.go`(handleSession:226 purpose 解析+路由;agentPeerPunchB 加 transferServe 缝)
- Test: `app/http/services/agent_peer_punch_test.go`(路由用例)+ `agent_peer_lan_push_test.go`(注册表 TTL 用例)

- [ ] **Step 1: 写失败测试**(两件):①注册表:register→take 拿到、再 take 空(单次消费)、TTL 过后 take 空;②B 侧路由:注入缝下 purpose=transfer 会话的过门流进 transferServe 缝、purpose 缺省进 b.handler 缝、purpose=transfer 但无上下文回 fail("transfer_context_missing")。

- [ ] **Step 2: 跑失败** → **Step 3: 实现**:

agent_peer_lan.go 追加(上传腿分节末尾):

```go
// —— 传输腿 punch 接收上下文(transfer-punch-stun)——
// handleAgentPeerLanPushListen 注册(传输参数在 listen 帧里齐备);B 侧
// purpose=transfer 会话按 transfer_id 查表。单次消费:第一个打洞会话取走
// 即删——重拨=新会话查空回 transfer_context_missing,上传链落 COS(v1
// 不做会话间续借;close 帧系 TODOS ⑦)。

type agentPunchTransferContext struct {
	token     string
	filename  string
	targetDir string
	maxBytes  int64
	writeJSON func(interface{}) error // listen 帧时刻的 WS writer;断连后进度帧静默丢,非致命
}

var (
	agentPunchTransferCtxMu sync.Mutex
	agentPunchTransferCtx   = map[string]agentPunchTransferContext{}
)

// registerAgentPunchTransferContext 入表+TTL 摘除(listen TTL + 30s 余量,
// 覆盖 punch 会话 90s 寿命口径不对齐的窗口——宁可表多活一会,查空由单次
// 消费语义兜住)。
func registerAgentPunchTransferContext(transferID string, tctx agentPunchTransferContext, ttl time.Duration) {
	agentPunchTransferCtxMu.Lock()
	agentPunchTransferCtx[transferID] = tctx
	agentPunchTransferCtxMu.Unlock()
	time.AfterFunc(ttl+30*time.Second, func() {
		agentPunchTransferCtxMu.Lock()
		delete(agentPunchTransferCtx, transferID)
		agentPunchTransferCtxMu.Unlock()
	})
}

// takeAgentPunchTransferContext 单次消费取表(无/已摘 → nil)。
func takeAgentPunchTransferContext(transferID string) (agentPunchTransferContext, bool) {
	agentPunchTransferCtxMu.Lock()
	defer agentPunchTransferCtxMu.Unlock()
	tctx, ok := agentPunchTransferCtx[transferID]
	if ok {
		delete(agentPunchTransferCtx, transferID)
	}
	return tctx, ok
}
```

handleAgentPeerLanPushListen 的 ttlSeconds 收敛之后、证书生成之前追加注册:

```go
	// 传输腿 punch 接收上下文(transfer-punch-stun):传输参数此刻齐备,
	// 打洞会话(purpose=transfer)按 transfer_id 查用。TCP listener 与 punch
	// 会话并行不悖——同网段走 TCP 腿,跨网段走 punch 腿,先到先得。
	registerAgentPunchTransferContext(transferID, agentPunchTransferContext{
		token:     token,
		filename:  filename,
		targetDir: targetDir,
		maxBytes:  maxBytes,
		writeJSON: writeJSON,
	}, time.Duration(ttlSeconds)*time.Second)
```

(文件若缺 `sync`/`time` import 补齐——time 已有,sync 需查。)

agent_peer_punch.go:`agentPeerPunchB` 加缝 `transferServe func(transferID string, tctx agentPunchTransferContext) func(net.Conn)`,生产默认:

```go
// agentPunchTransferServe 生产传输服务体:过门流直接进 lan_push 协议体
// (punch 流自带 TLS,禁再包 tls.Server——协议体拆层契约,Task 6)。
func agentPunchTransferServe(transferID string, tctx agentPunchTransferContext) func(net.Conn) {
	return func(conn net.Conn) {
		serveAgentLanPushConn(conn, tctx.token, transferID, tctx.filename, tctx.targetDir, tctx.maxBytes, tctx.writeJSON)
	}
}
```

handleSession 在 `advertise_ip` 注释行后追加解析:

```go
	purpose := remoteString(msg, "purpose")
	if purpose != "mesh" && purpose != "transfer" {
		// 缺省与显式 mesh 同路;未知 purpose 拒帧(老 A 侧恒发 mesh 或缺省)。
		fail("bad_purpose")
		return
	}
	transferID := remoteString(msg, "transfer_id")
```

**时序裁决(评审坐实)**:`ListenerOpts.Handler` 是 `b.serve` 的**入参,先于 serve 求值**——上下文必须在 serve 之前取出,「serve 失败不消费表项」改由**失败分支放回**实现:

```go
	// 传输上下文 serve 前取出(Handler 是 serve 入参,先于 serve 求值);
	// serve 失败在下方 listen_failed 分支放回表项,消费语义归成功路径独占。
	tctx, haveTctx := agentPunchTransferContext{}, false
	if purpose == "transfer" {
		if !validAgentTransferID(transferID) {
			fail("bad_transfer_id")
			return
		}
		tctx, haveTctx = takeAgentPunchTransferContext(transferID)
		if !haveTctx {
			// 声明了传输用途却无接收上下文(listen 未发生/已被消费/TTL 摘除):
			// fail-loud 回 A,绝不静默喂 ssh 入口(用途错配=数据喂错 handler)。
			fail("transfer_context_missing")
			return
		}
	}
	handlerFn := b.handler(sessionID)
	if haveTctx {
		handlerFn = b.transferServe(transferID, tctx)
	}
```

此段插在「证书生成」之前(purpose 校验先行,失败早退不白签证书);既有 `cert, fingerprint, err := generateAgentLanCertificate()` 起的流程不动。b.serve 失败分支(`listen_failed`)在 fail 之前放回:

```go
	if err != nil {
		_ = transport.Close()
		_ = socket.Close()
		if haveTctx {
			// 放回表项:serve 失败不消费(单次消费语义由成功路径独占)。
			registerAgentPunchTransferContext(transferID, tctx, time.Duration(ttlSeconds)*time.Second)
		}
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] listener failed session=%s err=%v", sessionID, err))
		fail("listen_failed")
		return
	}
```

ListenerOpts 的 `Handler: b.handler(sessionID)` 改 `Handler: handlerFn`。`newAgentPeerPunchB` 补 `transferServe: agentPunchTransferServe,`。文件头注释「B 端流程」补 purpose 路由一行。

- [ ] **Step 4: 全绿**:`go test ./app/http/services/ -run 'Punch|LanPush' -count=1`
- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_lan.go app/http/services/agent_peer_punch.go app/http/services/agent_peer_punch_test.go app/http/services/agent_peer_lan_push_test.go
git commit -m "新增:punch 会话 purpose 路由(transfer 上下文注册表/单次消费/serve 失败不消费)"
```

### Task 8: 发送侧 punch 载体接入上传链

**Files:**
- Modify: `app/http/services/agent_peer_lan.go`(attemptAgentTransferLANPush 旁追加 attemptAgentTransferPunchPush + parseAgentPunchPeerID)
- Modify: `app/http/services/agent_transfer_upload.go`(handleAgentTransferUpload:195-235 LAN 分支后插 punch 分支)
- Test: `app/http/services/agent_transfer_test.go`(分支编排用例)

- [ ] **Step 1: 写失败测试**(注入缝!punch 拨号必须可测):`attemptAgentTransferPunchPush` 不直呼 `AgentPeerPunchTransferDial`,改走包级变量缝 `agentPunchTransferDialFn`(生产=AgentPeerPunchTransferDial)。测试:①缝返回成功 conn(net.Pipe 对端喂协议应答)→ 文件推完,断言返回 size/sha;②缝返回错误 → classify 阶段映射非空;③归档/总量未知 → errAgentTransferLanSkip。上传链编排:无 punch_peer_id 不进 punch 分支(伪造帧断言 COS 兜底路径未变)。

- [ ] **Step 2: 跑失败** → **Step 3: 实现**:

agent_peer_lan.go:

```go
// agentPunchTransferDialFn punch 拨号缝(测试替身替换;生产=导出入口)。
var agentPunchTransferDialFn = AgentPeerPunchTransferDial

// parseAgentPunchPeerID 提取 upload 帧顶层 punch_peer_id(PhoneServer 下发,
// 老服务端缺字段=空串=不进 punch 腿,纯 COS 语义不变)。TrimSpace+长度闸。
func parseAgentPunchPeerID(msg map[string]interface{}) string {
	peer := strings.TrimSpace(remoteString(msg, "punch_peer_id"))
	if peer == "" || len(peer) > 128 {
		return ""
	}
	return peer
}

// attemptAgentTransferPunchPush 上传腿 punch 载体(transfer-punch-stun §2.2):
// LAN 直推失败后、COS 兜底前,打洞直连推流。协议体与 LAN 腿逐字共用
// (pushAgentLanStream,punch 流自带 TLS);token 仍用 lan_push 候选里的
// 单次凭据(接收侧 listen 上下文同值核验)。任何失败恒错误,调用方落 COS。
func attemptAgentTransferPunchPush(ctx context.Context, ident agentTransferIdentity, punchPeerID, lanToken, sourcePath string, totalSize int64, writeJSON func(interface{}) error) (int64, string, string, error) {
	if totalSize <= 0 {
		return 0, "", "", fmt.Errorf("%w: unknown source size", errAgentTransferLanSkip)
	}
	conn, err := agentPunchTransferDialFn(ctx, punchPeerID, ident.TransferID)
	if err != nil {
		return 0, "", "", fmt.Errorf("punch dial %s: %w(%v)", punchPeerID, errAgentTransferLanSkip, err)
	}
	defer conn.Close()
	logger.Info(fmt.Sprintf("[TRANSFER-PUNCH] punched peer=%s transfer_id=%s, streaming", punchPeerID, ident.TransferID))
	return pushAgentLanStream(ctx, conn, ident, lanToken, sourcePath, totalSize, writeJSON)
}
```

注意:拨号失败包 `errAgentTransferLanSkip` 让上传链走「skip 日志」而不是伪失败阶段?——**不**:拨号失败是真实失败,应进 lan_fail_stage 审计(接收腿语义:probe 阶段)。改回:

```go
	if err != nil {
		return 0, "", "", fmt.Errorf("punch dial %s: %w", punchPeerID, err)
	}
```

(classifyAgentLanFailStage 对 "punch dial" 前缀落 default=mid_transfer;可接受,v1 审计粗粒度。)

handleAgentTransferUpload:LAN push 分支(`if pushCand := ...` 块,含其 `ident.LanPushFailStage = stage` 收尾)之后、`startAgentTransferUpload(...)` 之前插入:

```go
	// 上传腿 punch 载体(transfer-punch-stun):LAN 失败后、COS 兜底前。
	// 前置=lan_push 候选在手(token 是接收侧核验凭据,listen 失败=无候选
	// =无 punch,纯 COS)+目标设备 ID(PhoneServer punch_peer_id)+非归档。
	if punchPeer := parseAgentPunchPeerID(msg); punchPeer != "" && pushCand != nil && !ident.IsDirectoryArchive {
		if info, statErr := os.Stat(sourcePath); statErr == nil && !info.IsDir() {
			size, sha, finalPath, punchErr := attemptAgentTransferPunchPush(ctx, ident, punchPeer, pushCand.Token, sourcePath, info.Size(), writeJSON)
			if punchErr == nil {
				logger.Info(fmt.Sprintf("[TRANSFER-PUNCH] complete transfer_id=%s bytes=%d path=%s", ident.TransferID, size, finalPath))
				payload := agentTransferResultPayload(ident.RequestID, ident.TransferID, true)
				payload["via"] = "punch"
				payload["size_bytes"] = size
				payload["sha256"] = sha
				if finalPath != "" {
					payload["path"] = finalPath
				}
				if err := writeJSON(payload); err != nil {
					logger.Warn(fmt.Sprintf("[TRANSFER] upload result write failed transfer_id=%s err=%v", ident.TransferID, err))
				}
				return
			}
			if errors.Is(punchErr, errAgentTransferLanSkip) {
				// 零字节文件等 skip 形态:记 skip 日志不进失败审计(LAN 腿同款,
				// 评审 R5:stat 成功但 size=0 不该被记成 mid_transfer 失败阶段)。
				logger.Info(fmt.Sprintf("[TRANSFER-PUNCH] skipped transfer_id=%s: %v", ident.TransferID, punchErr))
			} else if ctx.Err() != nil {
				// 取消落在 punch 腿:取消必须完全失效,绝不落 COS 重传(LAN 腿同款)。
				_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrCancelled, punchErr)
				return
			} else {
				stage := classifyAgentLanFailStage(punchErr)
				logger.Info(fmt.Sprintf("[TRANSFER-PUNCH] failed transfer_id=%s stage=%s err=%v — falling back to COS", ident.TransferID, stage, punchErr))
				ident.LanPushFailStage = stage
			}
		}
	}
```

⚠️ `pushCand` 作用域:LAN 分支的 `pushCand` 是 if 语句内变量——把 `pushCand := parseAgentLanPushCandidates(msg)` 提到 LAN 分支外作为局部变量,两分支共用(空值=两腿都不进)。

- [ ] **Step 4: 全绿**:`go test ./app/http/services/ -run 'Transfer|Punch|LanPush' -count=1`
- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_lan.go app/http/services/agent_transfer_upload.go app/http/services/agent_transfer_test.go
git commit -m "新增:上传腿 punch 直连载体(LAN失败后COS前,协议体共用,via=punch 终态)"
```

### Task 9: 集成测试传输腿用例

**Files:**
- Modify: `test/punch_integration_test.go`(追加用例,复用 punchSignalingBridge/夹具)

- [ ] **Step 1: 追加 `TestPunchIntegrationTransferLeg`**:沿 FullChain 拓扑(sink 接桥+双端夹具),差异点:①接收侧先 `registerAgentPunchTransferContext(tid, agentPunchTransferContext{token: "tok-x", filename: "f.bin", targetDir: "", maxBytes: 0, writeJSON: 吞帧函数}, 30*time.Second)`;②拨号侧调 `services.AgentPeerPunchTransferDial(ctx, 接收侧devID, tid)` 拿 conn;③独立 goroutine 用 `pushAgentLanStream` 把临时文件推过去;④断言接收侧 inbox 落盘 sha/size 一致;⑤反向断言:不注册上下文时同链路回 `transfer_context_missing`(session.result ok=false)。

- [ ] **Step 2: 跑**:`go test ./test/ -run TestPunchIntegration -count=1 -v`(六既有用例+新用例全绿)
- [ ] **Step 3: Commit**

```bash
git add test/punch_integration_test.go
git commit -m "新增:传输腿 punch 集成用例(上下文查表/单次消费/context_missing 拒单)"
```

### Task 10: 契约测试同步+全仓回归

- [ ] **Step 1**:跑 `go test ./app/http/models/ -run Contract -v`——session 帧新增 purpose/transfer_id 若触发契约枚举断言,按「Optional」语义补两键(字段缺省=mesh 向后兼容,老 B 侧零感知)。
- [ ] **Step 2**:全仓回归 `go build ./... && go test ./... -count=1`(已知预存 flake 按 [[preexisting-test-flakes-subprocess-timeout]] 单包复跑口径处置,不洗白)
- [ ] **Step 3**:gofmt 全改文件;Commit(若有契约改动)

```bash
git commit -m "测试:session 帧契约补 purpose/transfer_id 可选键"
```

---

## PhoneServer: punch_peer_id 下发(独立仓)

### Task 11: upload 帧补 punch_peer_id

**Files:**
- Modify: `../AliangPhoneServer-tp/server/src/modules/transfer/service.ts`(buildTransferUploadPayload:178)
- Test: `../AliangPhoneServer-tp/server/test/transfer/serviceLanPush.test.ts`(或 payload 构造测试所在文件,先 grep buildTransferUploadPayload 的既有测试)

- [ ] **Step 1: 写失败测试**:构造最小 TransferRow(含 targetDeviceId),断言 `buildTransferUploadPayload(row, cfg).punch_peer_id === row.targetDeviceId`。

- [ ] **Step 2: 跑失败**:`npx vitest run test/transfer/`(沿仓内脚本口径,先看 package.json scripts)

- [ ] **Step 3: 实现**(buildTransferUploadPayload 的 return 对象,source 行前加):

```ts
    // 传输腿 punch 直连(transfer-punch-stun):目标设备 ID,发送方据此发起
    // peer.punch 信令(信封路由 target_device_id=本字段)。顶层可选键,老
    // agent 零感知;agent 侧 punch 腿还须 lan_push 候选在手(token)才进。
    punch_peer_id: row.targetDeviceId,
```

- [ ] **Step 4: 全绿**:`npx vitest run test/transfer/`
- [ ] **Step 5: Commit**

```bash
git add server/src/modules/transfer/service.ts server/test/transfer/
git commit -m "新增:transfer.upload 帧下发 punch_peer_id(传输腿打洞寻址,老agent零感知)"
```

---

## 验收(部署后真机,ops 项不进代码)

- 两仓出包:alianggate agent 全端换装(vm220/liang-dev ≥新版本)、PhoneServer 镜像重建部署(全新 tag,避 IfNotPresent 陷阱)。
- vm220 ↔ liang-dev 真机:两端仪表盘开 `ssh_ingress→mesh.enable→punch_enable`;文件传输(单文件,非归档)跨网互传;观察 `[TRANSFER-PUNCH] via carrier` / `[TRANSFER] lan push` / `via=cos` 日志落哪条腿;顺带跑 mesh 转发面打洞率(`[MESH-OUT] via carrier=`),二期闸门数据落账。
- STUN 验证:两端日志确认 `stun.miwifi.com` 探测可达;自托管 coturn 时配置进 `stun_servers` 首位复测。

## 遗留账(收尾并入 TODOS.md)

- 传输腿预热(接收方并行 punch 会话)——被预算吸收替代,二期看真机数据。
- punch 失败阶段审计细分(probe/connect/mid_transfer 对 punch 载体的精确定义)。
- STUN 健康探测/多档 fallback(列表内择优)。
- 诊断面板传输腿 carrier 分桶统计。
