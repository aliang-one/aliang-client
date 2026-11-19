# punch 公网对端直连优化 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 打洞链路识别「对端无 NAT 可直连」:B 侧自声明(`session.result.public`)+ A 侧跳过 classify/自候选直拨——公网服务器对端省掉双侧 STUN(数秒),云 EIP 服务器经 `public_listen` 配置纳入,STUN 全挂场景开逃生门;并修复 PhoneServer 中继 zod 白名单剥除 `purpose`/`transfer_id`/`public` 的上批遗留缺陷。

**Architecture:** B 侧判据纯函数 `isPublicMapping(mapping, snapshot)`(映射 IP ∈ 接口集)+ `customer.mesh.public_listen` 显式覆盖(默认关);声明经既有 `session.result` 帧(Agent 契约三件套:契约表+want+PhoneServer zod);A 侧 dialMiss 收到声明后跳 classify/自候选(bind 保留),QUIC 同五元组握手天然过 NAT 过滤。

**Tech Stack:** Go(quic-go/pion/stun)、Vue options 组件+vitest+playwright、Node/TS+vitest(AliangPhoneServer)。

**Spec:** `docs/superpowers/specs/2026-11-19-punch-public-peer-design.md`(两轮评审通过)

**Worktree:** `/Users/mac/MyProgram/GoProgram/nursor/alianggate-punch-public-peer`(feat/punch-public-peer,基于 master@bb64a49,spec 已提交 6b6db8c+a4e04c7)

---

## 计划期裁决(spec 评审两轮产物的定稿口径)

1. **跳过清单 = classify + 自候选发送;socket bind 保留**(DialPeer 必须持有 conn,绑定铁律不可绕;bind 成本=毫秒级接口解析,本优化省的是秒级 STUN)。
2. **skipMemo = 不写、预读照旧**(ShouldSkip 先于 public 可知;残留读取有界 ≤5min TTL)。
3. **契约「三件套」口径** = agent 契约表 + 契约测试 want 映射 + **PhoneServer zod 白名单**(punch.ts strip 模式,未注册键静默剥除——缺 zod=public 永远到不了 A;**且上批 purpose/transfer_id 同样缺登=传输腿生产退化缺陷,本批一并修**)。
4. **B 判据抽纯函数** `isPublicMapping(mapping, snapshot)`(机器无关单测);集成走 `public_listen` 配置(loopback 拓扑 classify 恒失败,恰好练 fallback 候选路径)。
5. **public_listen 的 STUN-独立候选**:显式声明时 classify 失败回落「首个非回环 IPv4 : 监听口」(注意:云 EIP 机接口快照是私网 IP→候选不可拨→退化落中继,安全降级勿过度承诺——spec §3 精度注记)。

---

### Task 1: public_listen 配置字段

**Files:**
- Modify: `processor/config/types.go`(MeshConfig 结构体 StunServers 后 + Effective 区 EffectiveStunServers 后)
- Test: `processor/config/mesh_punch_config_test.go`(追加)

- [ ] **Step 1: 写失败测试**(mesh_punch_config_test.go 末尾追加)

```go
// —— public_listen(transfer-punch-stun 后续优化:公网对端直连)——

func TestMeshPublicListenEffective(t *testing.T) {
	var nilMesh *MeshConfig
	if nilMesh.EffectivePublicListen() {
		t.Fatal("nil receiver EffectivePublicListen 应为 false(让渡型默认关)")
	}
	empty := &MeshConfig{}
	if empty.EffectivePublicListen() {
		t.Fatal("零值 EffectivePublicListen 应为 false(默认关)")
	}
	on := &MeshConfig{PublicListen: boolPtr(true)}
	if !on.EffectivePublicListen() {
		t.Fatal("显式 true 应生效")
	}
	off := &MeshConfig{PublicListen: boolPtr(false)}
	if off.EffectivePublicListen() {
		t.Fatal("显式 false 应为关")
	}
}
```

(⚠️ `boolPtr` 在 processor/config 包测试内已存在(types_test.go),同包复用勿重定义——grep 范围=整个 processor/config 包)

- [ ] **Step 2: 跑失败**:`go test ./processor/config/ -run TestMeshPublicListenEffective -v` → 编译错(undefined)

- [ ] **Step 3: 实现**(types.go)

结构体 StunServers 字段后追加:

```go
	// PublicListen 本机监听口可被公网直连声明(punch-public-peer 批次)。
	// 默认关(nil=false):声明=对公网暴露打洞监听口,让渡型能力,与
	// PunchEnable 同族。语义:本机公网直挂(接口含公网 IP,B 侧自动判据
	// 天然命中,无需本字段)或云 EIP 1:1 NAT(接口只见私网 IP,自动判据
	// 漏判,须显式声明)。声明后候选 STUN-独立:classify 失败回落接口
	// 快照(首个非回环 IPv4 : 监听口)。消费处与 EffectivePunchEnabled
	// 合取,保存期不硬拒(手改配置绕过由运行期合取兜住)。
	PublicListen *bool `json:"public_listen,omitempty"`
```

Effective 区(EffectiveStunServers 之后)追加:

```go
// EffectivePublicListen 公网直连声明开关:三态指针默认关(nil=false),
// 与 EffectivePunchEnabled 同族。nil receiver 安全。
func (c *MeshConfig) EffectivePublicListen() bool {
	return c != nil && c.PublicListen != nil && *c.PublicListen // 默认关
}
```

- [ ] **Step 4: 跑测试确认通过** → **Step 5: Commit**

```bash
git add processor/config/types.go processor/config/mesh_punch_config_test.go
git commit -m "新增:customer.mesh.public_listen 公网直连声明配置(默认关,EIP 场景覆盖)"
```
+署名两行(`🤖 Generated with [Claude Code](https://claude.com/claude-code)` / `Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>`;全仓纪律:只 add 列出文件,绝不 `git add -A`——以下各任务同,不再重复)

### Task 2: B 侧判据纯函数 + result 声明接线

**Files:**
- Modify: `app/http/services/agent_peer_punch.go`(handleSession classify 块后判 public;result payload 带声明;isPublicMapping 纯函数;cfgEffectivePublicListen)
- Test: `app/http/services/agent_peer_punch_test.go`(追加)

- [ ] **Step 1: 写失败测试**

```go
// isPublicMapping 纯函数:STUN 映射 IP 命中任一非回环接口 IPv4 → 公网直挂。
// 机器无关(snapshot 注入),单测不依赖真实网卡。
func TestIsPublicMapping(t *testing.T) {
	snap := agentNetSnapshot{Interfaces: []agentNetInterface{
		{IPv4: "203.0.113.7"},
		{IPv4: "192.168.1.10"},
	}}
	if !isPublicMapping(punch.PublicMapping{IP: net.ParseIP("203.0.113.7"), Port: 3478}, snap) {
		t.Fatal("映射=接口公网地址应判 public")
	}
	if isPublicMapping(punch.PublicMapping{IP: net.ParseIP("198.51.100.9"), Port: 3478}, snap) {
		t.Fatal("映射≠任何接口地址(典型 NAT)不应判 public")
	}
	if isPublicMapping(punch.PublicMapping{IP: nil}, snap) {
		t.Fatal("空映射不应判 public")
	}
}
```

(注意:`agentNetSnapshot`/`agentNetInterface` 的真实类型名与字段名先 grep `app/http/services/agent_system.go` 确认——快照结构含 `Interfaces` 且元素带 `IPv4 string`;若元素类型名不同,以实际为准照 `agentLanAdvertiseAddr` 的遍历写法适配,断言语义不变。`net`/`punch` import 按需补。)

- [ ] **Step 2: 跑失败**(`go test ./app/http/services/ -run TestIsPublicMapping -v` → 编译错)

- [ ] **Step 3: 实现**(agent_peer_punch.go)

纯函数(handleSession 附近):

```go
// isPublicMapping 公网直挂判据(纯函数,snapshot 注入便于机器无关单测):
// STUN 映射 IP 命中任一非回环接口 IPv4 → 本机无 NAT,候选可直拨。
// 云 EIP 1:1 NAT 接口只见私网 IP → 不命中 → 不自动声明(显式
// public_listen 配置覆盖该场景,spec §2.1)。DNAT 端口映射场景映射
// IP=路由器公网≠接口地址 → 同样不命中(保守正确)。
func isPublicMapping(mapping punch.PublicMapping, snap agentNetSnapshot) bool {
	if mapping.IP == nil {
		return false
	}
	for _, iface := range snap.Interfaces {
		ip := net.ParseIP(iface.IPv4)
		if ip != nil && ip.Equal(mapping.IP) {
			return true
		}
	}
	return false
}

// cfgEffectivePublicListen public_listen 配置读侧(nil 安全,拷贝语义同族)。
func cfgEffectivePublicListen() bool {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil {
		return false
	}
	return cfg.Customer.Mesh.EffectivePublicListen()
}
```

handleSession 的 classify 块(`if classifyErr != nil {...} else {...}`)之后追加:

```go
	// 公网直连声明(punch-public-peer):自动判据(映射=接口地址,本机
	// 直挂)∨ 显式 public_listen(云 EIP 场景配置覆盖)。声明后 result 捎带
	// public,A 侧跳过自身 classify 直拨。
	publicPeer := cfgEffectivePublicListen() ||
		(classifyErr == nil && mapping.IP != nil && isPublicMapping(mapping, collectAgentNetSnapshot()))
```

result 帧:把 `agentPunchSessionResultPayload(requestID, true, port, fingerprint, "")` 调用改传 public——**函数签名扩参**(追加尾参 `public bool`,payload 组装处 `if ok && public { payload["public"] = true }`,条件键不出键惯例);失败臂传 false。grep 该函数全部调用点(handleSession+测试)同步。

**fallback 候选(评审 Issue-1 修复,勿漏——裁决 5/STUN 逃生门的代码落点)**:public_listen=true 且 classifyErr != nil 时,自候选回落「首个非回环接口 IPv4 : 监听口」。**落点在 serve 返回之后、candidates 帧发送 gate 之前**(候选组装处,`port` 此刻才可得;**不在** classify 块内):

```go
	if publicPeer && len(selfCandidates) == 0 && classifyErr != nil {
		// public_listen 声明且判型失败(STUN 全挂/云 EIP 接口私网):接口
		// 快照候选兜底(spec §2.1 STUN-独立逃生门;裁决口径=接口快照,
		// 非 bind iface 字符串)。快照无非回环 IPv4 → 留空(退化落中继,
		// spec §3 精度注记)。
		if iface := firstNonLoopbackIPv4(collectAgentNetSnapshot()); iface != "" {
			selfCandidates = []string{net.JoinHostPort(iface, strconv.Itoa(port))}
		}
	}
```

配套纯函数+单测(spec §6 无家可归的两条用例落此):

```go
// firstNonLoopbackIPv4 接口快照兜底候选的地址源(纯函数,机器无关单测)。
func firstNonLoopbackIPv4(snap agentNetSnapshot) string {
	for _, iface := range snap.Interfaces {
		ip := net.ParseIP(iface.IPv4)
		if ip != nil && !ip.IsLoopback() {
			return iface.IPv4
		}
	}
	return ""
}
```

测试(Step 1 一并写,红=未实现):①`TestFirstNonLoopbackIPv4`(有非回环→返回之;全回环/空→空串);②`TestHandleSessionPublicListenFallbackCandidate`:classify 注入缝返回 error + `SetGlobalConfig` 摆 `Mesh{PublicListen: boolPtr(true)}` + 自建 B 实例注入 `gate:true`(punch_disabled 会先拒,沿既有 B 侧测试自建实例形态;全局配置 t.Cleanup 恢复)→ 驱动 handleSession → 断言桥上 candidates 帧 `selfCandidates == ["<测试机非回环接口IP>:<port>"]`;③对照 public_listen=false + classify error → 无候选帧。

- [ ] **Step 4: 跑测试确认通过**(新增用例+既有 Punch 族零回归:`go test ./app/http/services/ -run 'Punch' -count=1`)

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_punch.go app/http/services/agent_peer_punch_test.go
git commit -m "新增:B 侧公网直挂判据 isPublicMapping+result 帧声明 public(public_listen 配置覆盖 EIP 场景)"
```

### Task 3: A 侧 public 跳过 classify/自候选

**Files:**
- Modify: `app/http/services/agent_peer_punch.go`(dialMiss result 解析后分支)
- Test: `app/http/services/agent_peer_punch_test.go`(追加)

- [ ] **Step 1: 写失败测试**(沿既有全链夹具 newPunchAForTest/punchFakeB 的摆置;fake B 的 result 帧带 `public:true`,classify 注入缝计数断言零调用;对照用例 public 缺省→classify 照走):

```go
// public=true:A 跳过 classify/自候选内容(bind 照走,Socket 绑定铁律);
// public 缺省:既有全流程零变化(计划期裁决 1/2)。
func TestAgentPunchDialMissPublicSkipsClassify(t *testing.T) {
	// 摆置沿既有全链测试:注入 classify 计数缝 + fake B(result 带 public:true)
	// ① public=true:conn 拨通;classifyCalls==0
	// ② 候选帧口径(评审 Issue-2 定稿):A 仍发一帧空数组候选(最小 diff,
	//    B 侧 no_valid_candidate 空转既有语义)→ 断言桥上 A→candidates 帧
	//    数==1 且候选数组空(nil/空数组皆算空:len 判定,勿断言字面 []interface{})
	// ③ 对照:public 缺省 fake B → classifyCalls>=1(既有语义)
}
```

(完整摆置以夹具现状为准——先读 `TestAgentPunchTransferDialFullChainAndIsolation` 的注入方式照抄;**`punchFakeB` 是全 Punch 测试族共享夹具,`public:true` result 变体必须是逐用例 opt-in 开关(默认 false),不得无条件改 onFrame**。断言三件:拨通、classify 零调、候选帧=空数组一次。)

- [ ] **Step 2: 跑失败** → **Step 3: 实现**(dialMiss,result 解析后):

```go
	fingerprint := remoteString(res, "fingerprint")
	publicPeer := remoteBool(res, "public", false)
```

既有「绑 socket+classify+memo+自候选」块改为条件化——**bind 保留在条件外, classify/Record/自候选整块包进 `if !publicPeer`**(计划期裁决 1:bind 成本毫秒级保留;裁决 2:skipMemo 不写,ShouldSkip 预读照旧保留在原位)。自候选 `agentPunchCandidatesPayload(sessionID, selfCandidates)` 发送保持无条件调用(public 路径 selfCandidates 为 nil→空帧,B 侧 no_valid_candidate 空转语义既有),**最小 diff**。

- [ ] **Step 4: 全绿**:`go test ./app/http/services/ -run 'Punch' -count=1` + `go test ./test/ -run TestPunchIntegration -count=1`
- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_punch.go app/http/services/agent_peer_punch_test.go
git commit -m "新增:A 侧收到 public 声明跳过 classify/自候选(bind 保留,同五元组握手直拨)"
```

### Task 4: 契约三件套(agent 侧两件)

**Files:**
- Modify: `app/http/models/agent_protocol.go`(session.result Optional += `public`)
- Modify: `app/http/models/agent_protocol_contract_test.go`(want 侧同步)

- [ ] **Step 1: want 侧先改**(契约测试红) → **Step 2: 跑失败**(`go test ./app/http/models/ -run Contract -count=1`) → **Step 3: 注册表补 `public`**(Task 10 同款双侧口径,沿该文件既有 Optional 书写形态) → **Step 4: 绿** → **Step 5: Commit**

```bash
git add app/http/models/agent_protocol.go app/http/models/agent_protocol_contract_test.go
git commit -m "新增:session.result 契约补 public Optional(公网直连声明,两侧同步)"
```

### Task 5: web UI(public_listen 开关)

**Files:**
- Modify: `app/website/src/components/settings/MeshSettings.js` / `.vue` / `.test.js`、`app/website/src/i18n/zh.js` / `en.js`

- [ ] **Step 1: js 归一失败测试**(normalizeMeshConfig:`public_listen` 三态 bool,缺键归 false——沿 Task 4 stun_servers/punch_enable 用例形态) → **Step 2: 红**(`cd app/website && npx vitest run src/components/settings/MeshSettings.test.js`) → **Step 3: js 实现**(normalizeMeshConfig 加 `public_listen: typeof source.public_listen === 'boolean' ? source.public_listen : false,` + 文件头注释) → **Step 4: vue 开关**(逐字镜像本卡既有 punch_enable 开关块——Task 4 已立,同文件同卡!)+ i18n 两键(`mesh_publicListen`/`mesh_publicListenHint`:「本机可被公网直连打洞(公网直挂或云 EIP 1:1 NAT 服务器勾选);默认关」zh/en 成对) → **Step 5: 测试与 e2e**(沿 punch_enable 用例形态:透传/不产伪 diff 断言) → **Step 6: 全绿**(vitest 该卡+playwright 该 spec) → **Step 7: Commit**

```bash
git add app/website/src/components/settings/MeshSettings.js app/website/src/components/settings/MeshSettings.vue app/website/src/components/settings/MeshSettings.test.js app/website/tests/e2e/mesh-settings.spec.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "新增:组网卡 public_listen 公网直连声明开关(归一/透传,EIP 服务器场景)"
```

### Task 6: 集成测试(public 路径)

**Files:**
- Modify: `test/punch_integration_test.go`

- [ ] **Step 1: 追加 `TestPunchIntegrationPublicPeer`**:沿 TransferLeg 拓扑,**接收侧 env 摆置 `public_listen=true` 配置**(SetGlobalConfig 同款缝)→ loopback 拓扑 classify 恒失败 → B 走「public_listen+接口快照 fallback 候选」→ 断言:①result 帧带 `public:true`;②A 收到后拨通(echo 往返);③对照:不配置 public_listen 的同拓扑 → A 拨号失败落中继语义(candidates 空超时)。注意 loopback classify:测试 env 的 STUN 注入本就不可达,classifyErr 恒真——fallback 候选=接口快照里的测试机非回环接口 IPv4(真实局域网 IP,同机可拨)+ 监听口——**先跑通再调断言,若接口候选不可拨如实报告改断言面**(例:断言 public 声明到达+拨号错误归类,不硬锁拨通)。

- [ ] **Step 2: 跑**:`go test ./test/ -run TestPunchIntegration -count=1 -v`(7 旧+1 新全绿或如实报告) → **Step 3: Commit**

```bash
git add test/punch_integration_test.go
git commit -m "新增:公网对端直连集成用例(public_listen 声明/fallback 候选/对照臂)"
```

### Task 7: PhoneServer zod 白名单补登(独立仓,**含上批遗留缺陷修复**)

**Files:**
- Create: worktree `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer-pp`(分支 `feat/punch-relay-schema`,基于 main@f14e80f;node_modules 软链主仓)
- Modify: `server/src/modules/peer/punch.ts`(:48-62 session schema、:64-72 result schema)
- Test: `server/test/peerPunchRelaySchema.test.ts`(新建,server/test/ 平铺,沿 peerLan.test.ts 形态)

- [ ] **Step 1: 写失败测试**(直呼 zod schema parse,断言三键透传不被剥除):

```ts
import { describe, expect, it } from 'vitest';
import {
  peerPunchSessionFrameSchema,
  peerPunchSessionResultFrameSchema,
} from '../src/modules/peer/punch';

describe('punch 中继 zod 白名单透传(transfer-punch-stun/punch-public-peer)', () => {
  it('session 帧透传 purpose/transfer_id(剥除=B 侧误路由 mesh,传输腿退化 COS)', () => {
    const parsed = peerPunchSessionFrameSchema.parse({
      type: 'peer.punch.session',
      request_id: 'req-1',
      session_id: 'sess-1',
      token: 'tok',
      ttl_seconds: 90,
      target_device_id: 'dev-b',
      purpose: 'transfer',
      transfer_id: 'tr-1',
    });
    expect(parsed.purpose).toBe('transfer');
    expect(parsed.transfer_id).toBe('tr-1');
  });

  it('result 帧透传 public(公网直连声明)', () => {
    const parsed = peerPunchSessionResultFrameSchema.parse({
      type: 'peer.punch.session.result',
      request_id: 'req-1',
      ok: true,
      port: 40001,
      fingerprint: 'ab'.repeat(32),
      public: true,
    });
    expect(parsed.public).toBe(true);
  });
});
```

- [ ] **Step 2: 跑失败**(`npx vitest run --config vitest.config.ts server/test/peerPunchRelaySchema.test.ts` → parsed.purpose undefined) → **Step 3: 实现**:

session schema `advertise_ip` 行后追加:

```ts
  // 会话目的(punch-public-peer/transfer-punch-stun):缺省=mesh 语义;A→B
  // 传输腿显式带 transfer(剥除=B 侧误路由 sshin,传输腿退化 COS——上批
  // 遗留缺陷,本批修复)。
  purpose: z.enum(['mesh', 'transfer']).optional(),
  transfer_id: z.string().max(64).optional(),
```

result schema `fail_reason` 行后追加:

```ts
  // 公网直连声明(punch-public-peer):B 侧判「本机候选可直拨」,A 侧据此
  // 跳过自身 classify 直拨。剥除=A 永远收不到声明→优化静默失效。
  public: z.boolean().optional(),
```

- [ ] **Step 4: 全绿**:`npx vitest run --config vitest.config.ts server/test/` 全量(既有 peerLan 等零回归) → **Step 5: Commit**

```bash
git add server/src/modules/peer/punch.ts server/test/peerPunchRelaySchema.test.ts
git commit -m "新增:punch 中继 zod 白名单补 purpose/transfer_id/public 三键(剥除缺陷修复+公网声明透传)"
```

### Task 8: 全量回归 + 收口

- [ ] **Step 1**:alianggate worktree `go build ./... && go test ./... -count=1`(豁免清单:lanbeacon×3 广播环境坑 / TestAssemblySeamsPunchThreeStates 环境预存 / auth TestSessionAuthority…+steer revive 负载 flake——超清单新失败=BLOCKER;PhoneServer 侧 transfer 全量绿+两预存失败豁免归 roster 批次)
- [ ] **Step 2**:gofmt 全触达文件;PhoneServer 全量 vitest
- [ ] **Step 3**:commit(若有残留改动)

### 验收(部署后)

- 公网服务器对端:vm220 若为云 EIP 机器 → 仪表盘勾 `public_listen` → vm220↔liang-dev 打洞信令一轮直拨,`[AGENT-PEER-PUNCH-A] punched` 日志应无 classify 等待;对照组(不勾)走全流程。
- STUN 全挂演练:关 STUN 出口 → public_listen 机仍可被直连(逃生门实测)。

## 遗留账

- MeshSettings unconfigured 启发式不纳入 public_listen(spec §4);A 侧 fail-closed 三行守卫等既有八条沿袭 TODOS。
