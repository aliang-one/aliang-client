# 节点转发网(mesh)后端实施计划(阶段 1-4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 alianggate 网关内实现 L4 节点转发网:规则命中「转发到节点」的 TCP 连接经 LAN/中继双载体逐跳转发,DNS 代转发兜住离线解析,自身/AI 流量可经其他节点出网。

**Architecture:** 新增 `meshproto`(帧协议:首行 JSON 头+原始字节)与 `outbound/proxy/mesh`(实现既有 `proxy.Proxy` 接口)两个包;路由侧在 `ProxyRoute` 枚举与 `dialByRoute` 加 `RouteToNode` 分支;快照编译在 `SnapshotTarget` 加 `toNode:<deviceID>` 前缀形态;B 侧常驻 relay 监听口(LAN TCP + piko `<deviceID>.mesh` 端点)把来流注入 processor/tcp 既有管线;TUN UDP/53 增加 mesh 代转发;自身流量经 `processor/egress` 统一出口。**错误一律回源,仅链头执行兜底与失败策略**。

**Tech Stack:** Go 1.x(仓库现状);piko client v0.10.0(隧道桥,既有依赖);miekg/dns(既有依赖);无新第三方依赖。

**规范:** `docs/mesh-forwarding-network-design.md`(已评审定稿,§3.1 为实施期修正版)。

**执行前置:** 按 [[worktree-development-workflow]] 惯例,开工先建 worktree 切分支(如 `feat/mesh-forwarding`);所有提交全中文「新增:/修复:」格式;禁止自动 commit,每个任务步骤里的 commit 需执行者按计划明示执行。

---

## 文件结构总览

| 动作 | 路径 | 职责 |
|---|---|---|
| 新建 | `outbound/meshproto/header.go` | 帧头编解码、path vector、跳数上限 |
| 新建 | `outbound/meshproto/header_test.go` | 同上测试 |
| 新建 | `outbound/proxy/mesh/mesh.go` | mesh 出口:实现 `proxy.Proxy`,双载体拨出+帧头写入 |
| 新建 | `outbound/proxy/mesh/mesh_test.go` | 同上测试(loopback 假 relay) |
| 新建 | `processor/tcp/mesh_inbound.go` | B 侧接收:头部解析→防环→门禁 ACL→构 Metadata→注入管线 |
| 新建 | `processor/tcp/mesh_inbound_test.go` | 同上测试 |
| 新建 | `app/mesh/relay/relay.go` | 常驻 mesh 监听口:LAN TCP accept + piko `<deviceID>.mesh` 端点 |
| 新建 | `app/mesh/relay/relay_test.go` | 同上测试 |
| 新建 | `app/mesh/lanbeacon/beacon.go` | UDP 广播发现(离线可用) |
| 新建 | `app/mesh/lanbeacon/beacon_test.go` | 同上测试 |
| 新建 | `processor/egress/egress.go` | 自身流量统一出口(在线直连/离线 mesh 可切换) |
| 新建 | `processor/egress/egress_test.go` | 同上测试 |
| 修改 | `processor/config/types.go` | `MeshConfig` 配置 + Validate + 未知键 allowlist(两处) |
| 修改 | `app/http/services/customer_config_service.go` | allowlist 第三处 |
| 修改 | `processor/routing/snapshot.go` | `toNode:` 目标编译 |
| 修改 | `processor/routing/decision_engine.go` | RouteDecision 透传 + 能力检查 |
| 修改 | `processor/routing/snapshot_test.go` / `decision_engine_test.go` | 同上测试 |
| 修改 | `processor/tcp/interfaces.go` | `RouteToNode` 枚举 |
| 修改 | `processor/tcp/handler.go` | `dialByRoute` mesh 分支 + 链头失败策略/兜底执行 |
| 修改 | `inbound/tun/tunnel/udp.go` | DNS mesh 代转发钩子 |
| 修改 | `app/tunnel/manager.go` | piko `<deviceID>.mesh` 第二端点(照抄 tcpBridge 模式) |
| 修改 | `processor/auth/*.go` 等自身流量调用点 | 换 `processor/egress` 出口 |
| 修改 | `app/http/routes/routes.go` + `app/http/handlers/` | mesh 状态/发现候选 GET |
| 新建 | `test/mesh_integration_test.go` | 双节点 loopback 集成 |

---

### Task 1: meshproto — 帧头编解码与防环原语

**Files:**
- Create: `outbound/meshproto/header.go`
- Create: `outbound/meshproto/header_test.go`

- [ ] **Step 1: 写失败测试**

```go
package meshproto

import (
	"strings"
	"testing"
)

func TestHeaderEncodeParseRoundTrip(t *testing.T) {
	h := Header{
		From:   "dev-a",
		Class:  ClassAI,
		Target: "api.aliang.one:443",
		Via:    []string{"dev-a"},
		Hops:   0,
	}
	line, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Fatal("encoded header must end with newline")
	}
	got, consumed, err := Parse([]byte(line))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if consumed != len(line) {
		t.Fatalf("consumed = %d, want %d", consumed, len(line))
	}
	if got.From != h.From || got.Class != h.Class || got.Target != h.Target {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestParseRejectsOversizedHeader(t *testing.T) {
	long := strings.Repeat("x", MaxHeaderBytes)
	if _, _, err := Parse([]byte(long + "\n")); err == nil {
		t.Fatal("expected error for oversized header")
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, _, err := Parse([]byte("not-json\n")); err == nil {
		t.Fatal("expected error for malformed header")
	}
}

func TestAppendHopDetectsLoop(t *testing.T) {
	h := Header{From: "dev-a", Via: []string{"dev-a"}, Hops: 0}
	if err := h.AppendHop("dev-b", MaxHopsCap); err != nil {
		t.Fatalf("AppendHop dev-b: %v", err)
	}
	if err := h.AppendHop("dev-b", MaxHopsCap); err == nil {
		t.Fatal("expected loop error when appending duplicate device")
	}
}

func TestAppendHopEnforcesHopsLimit(t *testing.T) {
	h := Header{Via: []string{"dev-a"}, Hops: MaxHopsCap}
	if err := h.AppendHop("dev-b", MaxHopsCap); err == nil {
		t.Fatal("expected hops-limit error")
	}
}

func TestClassCarrierAllowed(t *testing.T) {
	// 用户自定义流量禁止走中继载体(design §4)
	if ClassCarrierAllowed(ClassCustom, CarrierRelay) {
		t.Fatal("custom class must not be allowed on relay carrier")
	}
	for _, c := range []string{ClassSelf, ClassAI, ClassCustom} {
		if !ClassCarrierAllowed(c, CarrierLAN) {
			t.Fatalf("class %q must be allowed on LAN carrier", c)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./outbound/meshproto/ -v`
Expected: FAIL(package 不存在编译错)

- [ ] **Step 3: 最小实现**

```go
// Package meshproto 实现 mesh 转发帧协议:首行 JSON 头 + 原始字节。
// 复刻 app/tunnel/bridge.go tcpRouteHeader 的「一行 JSON + 透传」风格(design §3.1)。
package meshproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// 流量类别(design §4)
const (
	ClassSelf   = "self"   // 自身流量:LAN + 中继
	ClassAI     = "ai"     // AI 流量:LAN + 中继
	ClassCustom = "custom" // 用户自定义:仅 LAN
)

const (
	CarrierLAN   = "lan"
	CarrierRelay = "relay"
)

// 跳数上限:默认 5、硬上限 8,可配置项由 config 层 clamp 到 [1,8](design §2 行7)。
const (
	DefaultHops = 5
	MaxHopsCap  = 8
)

// MaxHeaderBytes 防畸形超大首行(参考 bridge.go readHeader 的 4096 上限)。
const MaxHeaderBytes = 4096

type Header struct {
	From   string   `json:"from"`             // 发起节点 device_id(链头)
	Class  string   `json:"class"`            // self|ai|custom
	Target string   `json:"target"`           // host:port / 裸域名 / "dns:"
	Via    []string `json:"via"`              // path vector:已访 device_id(含链头)
	Hops   int      `json:"hops"`             // 已转发跳数
}

func (h *Header) Encode() (string, error) {
	if h.From == "" || h.Class == "" || h.Target == "" {
		return "", errors.New("meshproto: from/class/target required")
	}
	if h.Via == nil {
		h.Via = []string{h.From}
	}
	b, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("meshproto: marshal: %w", err)
	}
	if len(b)+1 > MaxHeaderBytes {
		return "", fmt.Errorf("meshproto: header too large (%d bytes)", len(b))
	}
	return string(b) + "\n", nil
}

// Parse 从流首解析一行 JSON 头,consumed 为含换行的字节数;余下字节为原始流量。
func Parse(data []byte) (*Header, int, error) {
	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		if len(data) > MaxHeaderBytes {
			return nil, 0, fmt.Errorf("meshproto: header exceeds %d bytes without newline", MaxHeaderBytes)
		}
		return nil, 0, errors.New("meshproto: header line incomplete")
	}
	if idx+1 > MaxHeaderBytes {
		return nil, 0, fmt.Errorf("meshproto: header too large (%d bytes)", idx+1)
	}
	var h Header
	if err := json.Unmarshal(data[:idx], &h); err != nil {
		return nil, 0, fmt.Errorf("meshproto: bad header json: %w", err)
	}
	if h.From == "" || h.Class == "" || h.Target == "" {
		return nil, 0, errors.New("meshproto: from/class/target required")
	}
	if h.Via == nil {
		h.Via = []string{h.From}
	}
	return &h, idx + 1, nil
}

// AppendHop 由每一跳在转发前调用:自查环路 + 递增跳数(design §7 层1/层2)。
// maxHops 由调用方从 config clamp 后传入(EffectiveMaxHops)。
func (h *Header) AppendHop(selfDeviceID string, maxHops int) error {
	for _, id := range h.Via {
		if id == selfDeviceID {
			return fmt.Errorf("meshproto: loop detected via %s", selfDeviceID)
		}
	}
	if h.Hops >= maxHops {
		return fmt.Errorf("meshproto: hop limit %d reached", maxHops)
	}
	h.Via = append(h.Via, selfDeviceID)
	h.Hops++
	return nil
}

// ClassCarrierAllowed 载体准入:用户自定义流量禁走中继(design §4,引擎层硬约束)。
func ClassCarrierAllowed(class, carrier string) bool {
	if carrier == CarrierLAN {
		return true
	}
	return class == ClassSelf || class == ClassAI
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./outbound/meshproto/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add outbound/meshproto/
git commit -m "新增:mesh 帧协议——JSON 头编解码+path vector 防环+跳数上限+载体准入"
```

---

### Task 2: meshproto — DNS 目标与常量补全

**Files:**
- Modify: `outbound/meshproto/header.go`
- Modify: `outbound/meshproto/header_test.go`

- [ ] **Step 1: 写失败测试(追加)**

```go
func TestDNSTarget(t *testing.T) {
	if !IsDNSTarget(DNSTarget) {
		t.Fatal("dns target must be recognized")
	}
	if IsDNSTarget("example.com:443") {
		t.Fatal("host target must not be dns")
	}
}

func TestSplitHostPort(t *testing.T) {
	host, port, isDomain, err := SplitTarget("api.aliang.one:443")
	if err != nil || host != "api.aliang.one" || port != "443" || !isDomain {
		t.Fatalf("SplitTarget domain: %v %q %q %v", err, host, port, isDomain)
	}
	host, port, isDomain, err = SplitTarget("10.1.2.3:8080")
	if err != nil || host != "10.1.2.3" || port != "8080" || isDomain {
		t.Fatalf("SplitTarget ip: %v %q %q %v", err, host, port, isDomain)
	}
	if _, _, _, err := SplitTarget(DNSTarget); err != nil {
		t.Fatalf("SplitTarget dns: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败** — `go test ./outbound/meshproto/ -v` → FAIL

- [ ] **Step 3: 实现(追加到 header.go)**

```go
// DNSTarget 特殊目标:DNS 代转发查询(每次查询独占一条 mesh 连接,design §6 层1)。
const DNSTarget = "dns:"

func IsDNSTarget(target string) bool { return target == DNSTarget }

// SplitTarget 拆 host:port;裸域名(无端口)由调用方按 443 兜底。
// 返回 isDomain=false 表示 host 为 IP 字面量。
func SplitTarget(target string) (host, port string, isDomain bool, err error) {
	if target == DNSTarget {
		return "", "", false, nil
	}
	h, p, splitErr := net.SplitHostPort(target)
	if splitErr != nil {
		return target, "", true, nil // 裸域名
	}
	if net.ParseIP(h) != nil {
		return h, p, false, nil
	}
	return h, p, true, nil
}
```

(import 补 `"net"`)

- [ ] **Step 4: 跑测试确认通过** — `go test ./outbound/meshproto/ -v` → PASS

- [ ] **Step 5: 提交** — `git commit -m "新增:mesh 帧协议 DNS 目标与 target 拆解"`

---

### Task 3: 配置层 — MeshConfig 字段 + 三处 allowlist 同步

**Files:**
- Modify: `processor/config/types.go`(CustomerConfig ~:519、PeerMCPConfig 参考 :680-685、Effective 系列参考 :690-725、Validate :968-1038、未知键 switch :1132、错误文案 :1095)
- Modify: `app/http/services/customer_config_service.go`(:411-413 allowlist)
- Modify: `processor/config/types_test.go`(Validate 类测试)
- Modify: `app/http/services/customer_config_service_test.go`(参照 :738 `TestValidateEditableCustomerKeysAllowsPeerMCP`——allowlist 测试放这里,与被测函数同文件)

- [ ] **Step 1: 写失败测试(两文件追加;`boolPtr` 助手若无则本文件内补)**

```go
func TestConfigValidate_MeshRejectsBadPeer(t *testing.T) {
	cfg := &Config{Customer: &CustomerConfig{Mesh: &MeshConfig{
		Peers: []MeshPeer{{DeviceID: "", RelayAddr: "1.2.3.4"}}, // 空 device_id
	}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mesh.peers") {
		t.Fatalf("want mesh.peers error, got %v", err)
	}
}

func TestConfigValidate_MeshHopsClamped(t *testing.T) {
	cfg := &Config{Customer: &CustomerConfig{Mesh: &MeshConfig{MaxHops: 99}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("want error for max_hops > 8")
	}
}

func TestConfigValidate_MeshFallbackRequiresAddr(t *testing.T) {
	cfg := &Config{Customer: &CustomerConfig{Mesh: &MeshConfig{
		Fallback: &MeshFallbackConfig{Enable: boolPtr(true)},
	}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mesh.fallback") {
		t.Fatalf("want mesh.fallback addr error, got %v", err)
	}
}

func TestValidateEditableCustomerKeysAllowsMesh(t *testing.T) {
	// 放 customer_config_service_test.go,照抄 :738 TestValidateEditableCustomerKeysAllowsPeerMCP 模式,断言 "mesh" 在 allowlist 内
}

func TestConfigValidate_MeshRejectsSelfReference(t *testing.T) {
	// spec §3.4/§7 层0:目标节点=自己,配置校验期直接拒绝
	selfID := "dev-self"
	cfg := &Config{Customer: &CustomerConfig{Mesh: &MeshConfig{
		Peers:    []MeshPeer{{DeviceID: selfID}},
		Fallback: &MeshFallbackConfig{Enable: boolPtr(true), Addr: "192.168.1.20:9443"},
	}}}
	if err := cfg.ValidateMesh(selfID); err == nil || !strings.Contains(err.Error(), "self") {
		t.Fatalf("want self-reference error, got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败** — `go test ./processor/config/ -run TestConfigValidate_Mesh -v` → FAIL(类型不存在)

- [ ] **Step 3: 实现**

types.go 追加(与 PeerMCPConfig 同风格,JSON snake_case):

```go
// MeshConfig 节点转发网(design docs/mesh-forwarding-network-design.md)。
type MeshConfig struct {
	Enable        *bool                `json:"enable,omitempty"`         // mesh 出口总开关(nil=true)
	ForwardEnable *bool                `json:"forward_enable,omitempty"` // 允许被其他节点转发(design §8,默认关 nil=false)
	MaxHops       int                  `json:"max_hops,omitempty"`       // 跳数上限,0=默认5,clamp [1,8]
	Peers         []MeshPeer           `json:"peers,omitempty"`          // 可转发目标节点
	Fallback      *MeshFallbackConfig  `json:"fallback,omitempty"`       // 离线兜底(design §9.4,兜底式)
}

type MeshPeer struct {
	DeviceID   string   `json:"device_id"`
	LANAddrs   []string `json:"lan_addrs,omitempty"`   // 候选 LAN 地址 host:port(beacon 动态补充)
	Relay      *bool    `json:"relay,omitempty"`       // 允许走中继载体(nil=true)
}

type MeshFallbackConfig struct {
	Enable  *bool    `json:"enable,omitempty"`
	Classes []string `json:"classes,omitempty"` // self/ai/custom 子集(design §9.4)
	Addr    string   `json:"addr,omitempty"`    // host:port,enable=true 时必填
}

func (c *MeshConfig) EffectiveEnabled() bool { return c == nil || c.Enable == nil || *c.Enable }
func (c *MeshConfig) EffectiveForwardEnabled() bool {
	return c != nil && c.ForwardEnable != nil && *c.ForwardEnable // 默认关(design §8)
}
func (c *MeshConfig) EffectiveMaxHops() int {
	if c == nil || c.MaxHops <= 0 { return meshproto.DefaultHops }
	if c.MaxHops > meshproto.MaxHopsCap { return meshproto.MaxHopsCap }
	return c.MaxHops
}
```

(`CustomerConfig` 加字段 `Mesh *MeshConfig \`json:"mesh,omitempty"\``;`ForwardEnable` 的 gating 检查点在 Task 6 relay 接入侧用 `EffectiveForwardEnabled()`。)

Validate 追加(`Config.Validate` customer 段,~:1031 后):peers 每项 device_id 非空、lan_addrs 用 `net.SplitHostPort` 校验;`MaxHops` 超界报错;`Fallback.Enable=true` 时 `Addr` 必填且格式校验、`Classes` 仅允许 self/ai/custom 子集。

**自指拒绝(spec §3.4/§7 层0 后端半边)**:新增 `(c *Config) ValidateMesh(selfDeviceID string) error`——`Peers[].DeviceID == self`、`Fallback` 目标 == self 一律报错。selfDeviceID 取自 tunnel manager 既有 deviceID 来源(实现时确认具体 getter 名,提交路径 customer_config_service 可达),`Config.Validate()` 内部调用 `ValidateMesh(currentSelfID)`,deviceID 未注册(未登录首启)时跳过自指检查不报错。

**三处 allowlist 同步**(仓库铁律「改一处必改三处」,见 customer_config_service.go:408 注释):types.go:1132 未知键 switch 加 `"mesh"`;types.go:1095 错误文案串加 `mesh`;customer_config_service.go:411-413 allowlist 加 `"mesh"`。

- [ ] **Step 4: 跑测试确认通过** — `go test ./processor/config/ ./app/http/services/ -run 'Mesh' -v` → PASS

- [ ] **Step 5: 提交** — `git commit -m "新增:customer.mesh 配置——peers/兜底/跳数/被转发门禁+三处 allowlist 同步"`

---

### Task 4: 路由编译与决策 — toNode 目标

**Files:**
- Modify: `processor/routing/snapshot.go`(:14-19 SnapshotAction、:48-54 SnapshotRule、:280-354 compile、:414-418 resolveProxyRuleTarget)
- Modify: `processor/routing/decision_engine.go`(:15-20 RouteDecision、:44 DecideRouteFromSnapshot、:90 resolveSnapshotAction、:129 isSnapshotActionAvailable)
- Modify: `processor/config/routing_schema.go`(**必改**:`:158-176` Validate 的 target 白名单与 `:200` normalizeRuleTarget 需接受 `toNode:` 前缀形态,否则 compile 出口两处 `canonical.Validate()` 调用会以 "unknown target" 失败)
- Modify: `processor/routing/snapshot_test.go`、`processor/routing/decision_engine_test.go`

**设计:** `SnapshotRule.Target` 采用 `toNode:<deviceID>` 前缀形态(不动 `SnapshotAction` 枚举语义,新增前缀常量);`RouteDecision` 直接透传字符串 `"toNode:<deviceID>"`,handler 侧解析前缀取节点。能力检查:mesh 使能 + 该 peer 存在于 `cfg.Customer.Mesh.Peers`。**实施期修正(0300244 评审定案):≠self 检查移出编译闸**——config 层拿不到 selfID(反向依赖成环),自指由保存期 `ValidateMesh` 拒绝 + 运行期 path vector 兜底,分层防御分工已在代码注释写明。**另:DNS 规则(Type:"dns")不参与连接级决策**(决策引擎恒 false),代转发目标由 Task 8 从快照直接扫规则取首条启用项。**失败策略多段尾(`mesh:x,direct/deny/fallback:y`)解析移至 Task 7**(Task 4 止血:mesh: 段后还有段 → 静默跳过不编译,防静默错编译)。

**DNS 规则(spec §6 层1/§9.3 规则驱动,定死):** 原始条目支持 `dns,mesh:dev-b` 形态 → 编译为 `{Type:"dns", Condition:"all", Target:"toNode:dev-b"}`。DNS 代转发目标**由快照中首条启用的 dns toNode 规则决定**(多 peer 无歧义:首条命中),`meshDNSForwardTarget()` 从 `CompileRuntimeSnapshotFromRuntimeInputs` 结果派生(deviceID, ok)——**不引入布尔开关字段**,与阶段 5 规则卡片的「DNS 全部→DNS 代转发」一一对应。

- [ ] **Step 1: 写失败测试(snapshot_test.go 追加,照抄 :357 GeoIP 测试的 wantRule 表风格)**

```go
func TestRuntimeSnapshot_CompileMeshForwardRule(t *testing.T) {
	cfg := &config.Config{Customer: &config.CustomerConfig{
		ProxyRules: []string{"domains,internal.corp,mesh:dev-b"},
		Mesh: &config.MeshConfig{Peers: []config.MeshPeer{{DeviceID: "dev-b"}}},
	}}
	compiled, err := routing.CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{AliangEnabled: true})
	if err != nil { t.Fatalf("compile: %v", err) }
	// 断言存在 rule{Type:"domain", Condition:"internal.corp", Target:"toNode:dev-b"}
	// 且 DecideRouteFromSnapshot 对该 domain 返回 "toNode:dev-b"
}

func TestRuntimeSnapshot_MeshTargetUnknownPeerRejected(t *testing.T) {
	// mesh:dev-xyz 但 Peers 无 dev-xyz → 该条目编译期跳过(与既有 :313-316 静默跳过一致)或报错——取「编译期跳过」,断言规则不存在
}

func TestDecideRouteFromSnapshot_MeshCapabilityUnavailable(t *testing.T) {
	// 目标 toNode:dev-b 存在但 cfg.Mesh.Peers 缺失 → 不命中(能力不可用),落到 default direct
}
```

- [ ] **Step 2: 跑测试确认失败** — `go test ./processor/routing/ -run Mesh -v` → FAIL

- [ ] **Step 3: 实现**

- `snapshot.go`:新增 `SnapshotTargetMeshPrefix = "toNode:"`;规则源语法扩展:`ClassifyEntry` 之外在 compile 层支持 `domains,<pattern>,mesh:<deviceID>` / `ip-cidr,<cidr>,mesh:<deviceID>` / `dns,mesh:<deviceID>` 形态(`resolveProxyRuleTarget` 同级新函数 `resolveProxyRuleTargetFromRaw(raw string) (cond string, kind, target string, ok bool)`,解析尾部 action 词时识别 `mesh:` 前缀——注意 `match/entry.go:39-43` legacyActionTail 会把未知尾部当噪音剥掉,**编译入口要在 ClassifyEntry 之前自行剥 mesh 尾**);生成 `Target: "toNode:dev-b"`。**红线记录:`match/entry.go:37-43` 注明该尾部集合与前端 `RulesSettings.vue` 必须同步维护——mesh 尾语法属此红线范围,阶段 5 前端计划必须覆盖(已记入本计划 §13 追加项,见 Task 14)。**
- `decision_engine.go`:`resolveSnapshotAction`/`isSnapshotActionAvailable` 对 `toNode:` 前缀:能力 = `cfg.Customer.Mesh.EffectiveEnabled()` 且 deviceID ∈ Peers(编译时已知,可在 SnapshotRule 上带 `Available bool` 或复用既有 capability seam——取最小改动:`isSnapshotActionAvailable` 加前缀分支查 config)。`DecideRouteFromSnapshot` 透传字符串。

- [ ] **Step 4: 跑测试确认通过** — `go test ./processor/routing/ -v` → PASS(含既有测试不回归)

- [ ] **Step 5: 提交** — `git commit -m "新增:路由快照 toNode 目标——mesh: 转发规则编译+能力检查+决策透传"`

---

### Task 5: mesh 出口 — outbound/proxy/mesh

**Files:**
- Create: `outbound/proxy/mesh/mesh.go`
- Create: `outbound/proxy/mesh/mesh_test.go`

**设计:** 实现 `proxy.Proxy`(`outbound/proxy/interfaces.go:13-22`:`DialContext/DialUDP/Addr/Proto`)。`DialContext`:按 peer 的载体优先级拨(LANAddrs 顺序试,全败且 Relay=true 且类别允许 → piko 端点),写帧头,返回 net.Conn。**中继载体拨号用 piko client dial(`<B>.mesh` 端点)**——piko dial API 验证见 Task 0 spike。

- [ ] **Step 0(前置 spike,半日):验证 piko 拨号面**
  - 在 `app/tunnel/` 写一次性探针测试:设备 A 的 piko client 能否 `client.Dialer` 拨另一设备已 Listen 的端点(`<B>.mesh` 形态);确认平台端点白名单缺失时的失败形态(软失败)。产出:确认/修正 Task 5 中继拨号代码与 Task 9 端点注册方式。**此 spike 结论回填本计划。**

- [ ] **Step 1: 写失败测试**

```go
package mesh

// loopback 假 relay:net.Listen 收连接→读头→回显,模拟 B 侧
func TestDialContextLANCarrierWritesHeader(t *testing.T) {
	// 起 loopback listener 当 B 的 relay;NewPeer(PeerTarget{DeviceID:"dev-b", LANAddrs:[addr]})
	// DialContext(ctx, metadata{HostName:"example.com", DstPort:443, ConnID:"c1"})
	// 断言:conn 首行 JSON 头 From/Class/Target/Via 正确;余下字节双向可写
}

func TestDialContextCarrierFallback(t *testing.T) {
	// LANAddrs 指向不可达地址 + Relay=false → 报错(错误回源,不吞)
}

func TestDialContextRelayCarrierDeniedForCustomClass(t *testing.T) {
	// Class=custom 且仅剩中继载体 → 报错(design §4 引擎层硬约束)
}
```

- [ ] **Step 2: 跑测试确认失败** — `go test ./outbound/proxy/mesh/ -v` → FAIL

- [ ] **Step 3: 实现(核心骨架)**

```go
package mesh

type PeerTarget struct {
	DeviceID string
	LANAddrs []string
	Relay    bool          // 允许中继载体
	PikoDial PikoDialFunc  // 注入式:拨 <DeviceID>.mesh 端点(测试注 fake;生产注 app/tunnel 封装)
	MaxHops  int
	SelfID   string        // 本机 device_id(链头写 Via)
}

type PikoDialFunc func(ctx context.Context, endpointID string) (net.Conn, error)

// DialContext 实现 proxy.Proxy(design §3.4 mesh 出口)
func (p *Peer) DialContext(ctx context.Context, md *metadata.Metadata) (net.Conn, error) {
	class := meshclassFromMetadata(md) // AI 流量=ai(56432 回环),自身=egress 调用点显式带,其余=custom
	target := meshTargetFromMetadata(md) // HostName:port(裸域名按 443/80 兜底,IP 直用)
	hdr := meshproto.Header{From: p.SelfID, Class: class, Target: target}
	for _, addr := range p.LANAddrs {
		c, err := dialLAN(ctx, addr)
		if err == nil {
			return writeHeaderAndReturn(c, &hdr)
		}
	}
	if !p.Relay || !meshproto.ClassCarrierAllowed(class, meshproto.CarrierRelay) {
		return nil, fmt.Errorf("mesh: peer %s unreachable (lan failed, relay denied)", p.DeviceID)
	}
	c, err := p.PikoDial(ctx, p.DeviceID + meshRelayEndpointSuffix) // ".mesh"
	if err != nil { return nil, err }
	return writeHeaderAndReturn(c, &hdr)
}
```

`metadata.Class` 传递:`M.Metadata` 无类别字段——**不改公共 Metadata**,在 mesh 包用 context value(与 `aliang/proxy.go` 的 `aliangContextConnIDKey` 同模式,proxy.go:126-128)+ egress 侧显式指定;56432 回环判 AI 复用 `shouldForceAliangRoute` 同款判据。

- [ ] **Step 4: 跑测试确认通过** — PASS

- [ ] **Step 5: 提交** — `git commit -m "新增:mesh 出口——LAN 优先双载体拨出+帧头写入+类别载体准入"`

---

### Task 6: B 侧接收 — mesh_inbound + 常驻 relay 监听口

**Files:**
- Create: `processor/tcp/mesh_inbound.go`、`mesh_inbound_test.go`
- Create: `app/mesh/relay/relay.go`、`relay_test.go`
- Modify: `app/tunnel/manager.go`(:328-345 第二端点模式旁加 `.mesh` 端点,软失败)
- Modify: `processor/config` 无新字段(门禁用 Task 3 的 `EffectiveForwardEnabled`)

**B 侧注入语义(design §3.3,定死):** 解析头→`AppendHop` 防环→门禁(`EffectiveForwardEnabled` + from ∈ 本机 Peers 配置的转发方 ACL)→构 `M.Metadata{HostName/DstIP/DstPort, Route:"RouteMesh"}`→喂 processor/tcp 既有入口(`inbound/tun/tunnel/tcp.go:30-57` 的 `getTCPHandler().Handle` 同款路径,SNI 嗅探/B 自己的规则/MITM 全继承)。DNS 目标(`dns:`)由 relay 直接代解析回包(Task 8)。

**ACL 语义(阶段 5 UI 需知):** 转发方 ACL 复用 `Mesh.Peers`——即**对称组网假设**:A 要把流量转发给 B,A 必须在 B 的 `Mesh.Peers` 里列有 A(或 B 的 peers 含 A 的 device_id)。仪表盘的 peers 列表因此是双向关系,画布连线两侧节点都需配置对方。

- [ ] **Step 1: 写失败测试**

```go
func TestMeshInboundRejectsLoop(t *testing.T) // Via 含本机 ID → 连接关闭+错误行回写
func TestMeshInboundRejectsGateClosed(t *testing.T) // ForwardEnable=false → 拒绝
func TestMeshInboundRejectsUnknownForwarder(t *testing.T) // from 不在 ACL → 拒绝
func TestMeshInboundRejectsCustomOnRelayCarrier(t *testing.T) // 中继端点收 custom → 拒绝
func TestMeshInboundInjectsTLSConnIntoPipeline(t *testing.T) // loopback: 起 relay,客户端发 TLS ClientHello(SNI=a.test),B 侧 pipeline 断言 DetermineRouteWithContext 收到 HostName=a.test
```

- [ ] **Step 2: 确认失败 → Step 3: 实现 → Step 4: 确认通过**(TDD 循环同前)

关键实现锚点:relay 常驻 listener(区别于 peer_lan 一次性口,`agent_peer_lan.go:311-320` 的「收一连接即关」行为**不复制**);`.mesh` piko 端点 serve 照抄 `manager.go:328-345`(`upstream.Listen` 失败仅日志+降级);relay accept 后交 `mesh_inbound.HandleMeshConn(conn, carrier)`。

- [ ] **Step 5: 提交** — `git commit -m "新增:B 侧 mesh 接收——防环/门禁 ACL/类别校验/注入 processor 管线"`

---

### Task 7: 链头拨出 — dialByRoute 分支 + 失败策略/兜底

**Files:**
- Modify: `processor/tcp/interfaces.go`(:155-172 加 `RouteToNode`,String() 同步)
- Modify: `processor/tcp/handler.go`(:706 dialByRoute 加 case;新增 `getMeshProxyForExecution` + `applyMeshHeadPolicy`,参照 `getToSocksProxyForExecution` :1243-1288 结构)
- Modify: `processor/tcp/tls.go`(:330-337 decision→ProxyRoute 映射加 `toNode:` 前缀解析)

**链头语义(design §2 行11/13 定死):** `dialByRoute` mesh 失败 → 错误回源语义下**在链头就地执行**:先落兜底(类别勾选且兜底使能),再走规则失败策略(回落 direct / 拒绝 / 转指定节点)。中间跳(Task 6)只回写错误行,不执行任何策略。

**Via/Hops 续传(f85643b 评审定案,本任务强制验收项):** B 侧注入的连接若规则再命中 toNode(B→C 中间跳),出口**必须携带入站帧头的 Via/Hops 续传**,不得写全新帧头——否则每跳 Via 重置 [self]/Hops 重置 0,跨节点环(A→B→C→B)的 path vector 与跳数帽**同时失效**。实现路径:mesh_inbound 注入时把 AppendHop 后的帧头(经 context 或 Metadata 扩展位)传至 dialByRoute;outbound/proxy/mesh 的 Peer 需支持「继承既有路径」构造。**验收测试必含:A→B→C→B 环路被 path vector/跳数帽拦截的集成用例**(并入 Task 13)。三处注释指针(mesh_inbound.go:108、mesh.go:46、mesh.go:82)已统一指向本任务。

- [ ] **Step 1: 写失败测试**(`handler_mesh_test.go`)

```go
func TestDialByRouteMeshNodeUnreachableFallsBackDirect(t *testing.T) // 策略=fallback_direct:mesh 拨号失败→direct 成功
func TestDialByRouteMeshFailurePolicyDeny(t *testing.T)             // 策略=deny:mesh 失败→连接拒绝,不落 direct
func TestDialByRouteMeshFallbackNodeSecond(t *testing.T)            // 策略=fallback_node:先兜底节点(勾选类别)再失败策略;未勾选类别直接走失败策略
func TestDialByRouteMeshRelayOnlyForAI(t *testing.T)                // custom 类别仅 LAN 载体:LAN 失败即失败,不试中继
```

- [ ] **Step 2-4: TDD 循环** — 失败策略字段:复用规则尾部语法 `mesh:dev-b` 之外再支持 `mesh:dev-b,direct` / `mesh:dev-b,deny` / `mesh:dev-b,fallback:dev-c`(缺省=direct)。**实施期修正(0300244 评审定案):多段尾解析从 Task 4 移至本任务**——Task 4 已止血(mesh: 段后还有段 → 静默跳过),本任务把 splitMeshTail 扩为多段尾语义并落地策略解析,顺带评估把 snapshot_mesh.go 拆分出 snapshot.go(评审 M3)。

- [ ] **Step 5: 提交** — `git commit -m "新增:dialByRoute mesh 分支——链头兜底/失败策略执行+中继载体类别闸"`

---

### Task 8: DNS 层1 — TUN UDP/53 mesh 代转发 + B 侧代解析

**Files:**
- Modify: `inbound/tun/tunnel/udp.go`(:20-61 handleUDPConn;:54-57 现为仅日志的 DNS 包裹点,扩展为转发点)
- Create: `inbound/tun/tunnel/dns_forward.go`、`dns_forward_test.go`
- Modify: `processor/tcp/mesh_inbound.go`(relay 侧 `dns:` 目标处理:miekg/dns 解析查询→B 本机解析器代答→单消息回写)

**设计:** 每次 DNS 查询独占一条 mesh 连接(target=`dns:`,单问单答即关)——查询量级下开销可忽略。**实施期修正(81231cb/bd17105):线格式为 `[2B 大端长度][载荷]` 双向帧化**(原文本「无长度分帧」系笔误级简化——B 侧需定界拒绝零长帧、A 侧读响应不依赖 EOF 时序,帧化更稳;两侧实现与测试互锁钉死契约,收敛时机定在 Task 9/13 触碰 meshproto 时合入)。**归类(design §2 行12):TUN DNS 一律 custom 类 → 仅 LAN 载体;自身/AI 不经此路径(egress 显式持域名走域名直转)。** **门控(spec §6 规则驱动):** `meshDNSForwardTarget() (deviceID string, ok bool)` 从路由快照派生——存在启用的 `{Type:"dns", Target:"toNode:<id>"}` 规则且 mesh 使能且该 peer 有 LAN 地址即激活,目标取规则里的 deviceID;无 dns 规则 = 不转发(现状直连)。Task 4 已定此契约。**NXDOMAIN 透传(评审精化):** 上游负应答(net.DNSError.IsNotFound)→ NXDOMAIN 回 A(客户端可负缓存),仅真实失败 → SERVFAIL——防离线广告域名重试风暴。

- [ ] **Step 1: 写失败测试**

```go
func TestDNSForwardPacketConnRelaysQueryViaMesh(t *testing.T) // loopback 假 B:收 dns: 连接→回拼好的 DNS 响应→断言 PacketConn.ReadFrom 拿到
func TestDNSForwardFailsClosedWhenNoLANPeer(t *testing.T)     // 无 LAN peer(禁中继)→ 超时/SERVFAIL,不直连公网
func TestMeshInboundDNSTargetResolvesLocally(t *testing.T)    // B 侧:miekg 拆查询→本地解析→回包
```

- [ ] **Step 2-4: TDD 循环** — `handleUDPConn` :54-57 处:`if metadata.DstPort == 53 && meshDNSForwardTarget() 命中` 则 `pc = newDNSForwardPacketConn(...)`(取代 dnsLoggingPacketConn;日志行为保留在转发 conn 内)。**fail-open/fail-closed 边界(定死):** 无已配置/已学习的 LAN 地址 → 门未激活 → 现状直连透传(fail-open);门已激活但拨号时 LAN 不可达(custom 类禁中继)→ SERVFAIL,**绝不回落公网 DNS**(fail-closed)。实现注意:激活路径必须**跳过** `udp.go:33-38` 先行执行的 `t.Dialer().DialUDP(metadata)`(否则公网 UDP socket 在包裹替换前已泄漏),测试断言钉死「无公网 socket」。目标节点由 Task 4 的快照派生函数给出。

- [ ] **Step 5: 提交** — `git commit -m "新增:TUN DNS mesh 代转发——custom 类仅 LAN+单问单答+B 侧本地代解析"`

---

### Task 9: 中继载体接入 — piko `.mesh` 端点

**Files:**
- Modify: `app/tunnel/manager.go`(Task 6 已加端点;本 task 完成 A 侧 piko dial 封装 + token 携带 + 端点白名单协调记录)
- Modify: `outbound/proxy/mesh/mesh.go`(PikoDialFunc 生产实现接线)

**内容:**
- [ ] **Step 1:** `app/tunnel` 新增 `DialPeerMesh(ctx, deviceID) (net.Conn, error)`:复用 tunnel 配置的 URL+token 走 piko client dial(具体 API 以 Task 5 spike 结论为准)
- [ ] **Step 2:** 测试:fake piko server(loopback)下 A→B 全双工回环
- [ ] **Step 3:** 平台侧端点白名单 `<deviceID>.mesh` 的放行需求写入 `docs/mesh-forwarding-network-design.md` §13 开放项追加行(平台部署协调项,非本仓代码)
- [ ] **Step 4: 提交** — `git commit -m "新增:中继载体 A 侧 piko 拨号封装+白名单协调记录"`

---

### Task 10: LAN beacon — 离线发现

**Files:**
- Create: `app/mesh/lanbeacon/beacon.go`、`beacon_test.go`
- Modify: `app/http/routes/routes.go` + handler(GET `/api/mesh/lan_peers` 返回当前 beacon 收到的候选,供仪表盘离线模式点选)

**设计:** UDP 广播(端口选固定高位,如 56440,常量+可配);payload JSON `{device_id, mesh_port, cert_fingerprint, ts}`,3s 周期广播;监听侧去重(map+TTL 15s)。**实施期范围修正(56fd7d1 后 controller 裁决):本任务只做 beacon 发现数据面——`cert_fingerprint` 字段保留但本期空值(LAN 载体现为明文+门禁 ACL,Task 6 已落地);relay 自签证书+TLS 升级+指纹钉扎(A 侧 dialLAN 同步)单列为「发布前加固任务」**(spec §8 发布门:对外发布前必须落地;混合版本舰队兼容:A 侧 TLS-first + 明文回落探测),不与本任务捆绑——避免 B 单方升 TLS 打断 A 明文拨号。

- [ ] **Step 1: 写失败测试** — `TestBeaconBroadcastAndReceive`(loopback 双实例互见)、`TestBeaconDedupesWithinTTL`、`TestBeaconIgnoresForeignPayload`
- [ ] **Step 2-4: TDD 循环**
- [ ] **Step 5: 提交** — `git commit -m "新增:LAN beacon 离线发现——UDP 广播+去重+候选查询端点"`

---

### Task 11: 自身流量统一出口 — processor/egress

**Files:**
- Create: `processor/egress/egress.go`、`egress_test.go`
- Modify: `processor/auth/scan_login.go:69,118`、`token_activate.go:95,350,487`、`user_center.go:154`(裸 `http.Client{Timeout}` → `egress.PlatformClient(timeout)`)
- Modify: `app/http/services/software_config_service.go:31,:40`、`software_update_service.go:84`(同款替换;`:40` 是 WithStore nil-fallback 分支的同族 client,勿漏)

**设计:** `egress.PlatformClient(timeout) *http.Client` — Transport.DialContext:mesh 激活(离线兜底含 self 类)且 LAN peer 可达 → 走 mesh 出口(class=self,域名直转,末跳解析,**不本地 DNS**);否则默认直连(Go ProxyFromEnvironment 行为不变)。**在线时不改变任何现有行为**(mesh 仅离线激活,design §9.4 兜底式)。

- [ ] **Step 1: 写失败测试** — `TestPlatformClientDirectWhenOnline`(默认路径 Transport 行为不变)、`TestPlatformClientRoutesViaMeshWhenFallbackActive`(注入假 mesh 状态 + loopback 假 B,断言请求经 B 且 Host 头为平台域名)、`TestPlatformClientFallsBackOnError`(mesh 失败→直连重试一次,失败策略=回落 direct 语义)
- [ ] **Step 2-4: TDD 循环;调用点替换逐文件编译验证**
- [ ] **Step 5: 提交** — `git commit -m "新增:自身流量统一出口——在线直连/离线 mesh 域名直转+登录用量更新调用点接入"`

---

### Task 12: AI 流量 mesh 转发 — 离线强制改道

**Files:**
- Modify: `processor/tcp/route_override.go`(shouldForceAliangRoute 旁加 mesh 改道判定)
- Modify: `processor/tcp/handler.go:478-486`(强制分支:离线且 AI 兜底勾选 → `RouteToNode` 指向兜底/AI 规则节点;在线行为零变化)
- Modify: `app/http/services/agent_ai_agentcore.go:313`(`ensureAIUpstreamReachable` 探针在离线模式下探测 mesh peer 而非公网,避免离线误报 upstream 不可达)

- [ ] **Step 1: 写失败测试** — `TestForceAliangRouteRedirectsToNodeWhenOffline`、`TestForceAliangRouteUnchangedWhenOnline`、`TestAIUpstreamProbeSkippedWhenOfflineMeshActive`
- [ ] **Step 2-4: TDD 循环**(「离线」判定 = 平台隧道不可达状态复用 + 兜底勾选 ai)
- [ ] **Step 5: 提交** — `git commit -m "新增:AI 流量离线经节点转发——强制路由改道+探针降级"`

---

### Task 13: 集成测试 — 双节点 loopback 全链

**Files:**
- Create: `test/mesh_integration_test.go`(仓库 `test/` 目录,既有 `test/proxy_test.go`、`test/shadowsocks_test.go` 同地)

- [ ] **Step 1: 写集成测试**

```go
func TestMeshTwoNodeLANEndToEnd(t *testing.T)      // A(56432 入站→规则 mesh:dev-b)→B(loopback relay)→echo 目标;断言内容透传+目标侧看到 A 的原始 TLS 流;**含真管线 SNI 断言**(f85643b 评审回填:注入连接过真 handler 的 SNI 嗅探链路当前零测试覆盖,本用例补上)
func TestMeshCrossNodeLoopBlocked(t *testing.T)    // A→B→C→B 环路:被 path vector 或跳数帽拦截(Task 7 Via/Hops 续传的验收用例,强制)
func TestMeshThreeHopChain(t *testing.T)           // A→B→C:中间跳再命中 mesh 规则;path vector 递增
func TestMeshLoopRejected(t *testing.T)            // A↔B 互指:连接失败+错误回源,无 goroutine 泄漏(goleak 或连接计数)
func TestMeshRelayCarrierRejectsCustomClass(t *testing.T) // spec §11:custom 类别在中继载体被拒(集成级复验 Task 5/6 的单元断言)
func TestMeshDNSForwardEndToEnd(t *testing.T)      // TUN 侧伪造 UDP/53 → B 代解析回包
func TestMeshOfflineFallbackActivates(t *testing.T) // 兜底式:正常路径可用时不落兜底;切断正常路径后落兜底
func TestMeshOfflineAIRedirectTwoHop(t *testing.T)  // A 离线 AI 改道→B,B 再离线触发 B 自己改道+Via 续传/ping-pong 环拒(829bd11 评审回填:Task 12 后此链路零直接覆盖)
```

- [ ] **Step 2: 跑通** — `go test ./test/ -run TestMesh -v` → PASS
- [ ] **Step 3: 全仓回归** — `go build ./... && go test ./...`(已知预存 flake 见 [[preexisting-test-flakes-subprocess-timeout]]:[[preexisting-race-flakes-transfer-2026-10]],单包复跑即绿,不算回归)
- [ ] **Step 4: 提交** — `git commit -m "新增:mesh 双节点/三跳/防环/DNS/兜底集成测试"`

---

### Task 14: 门禁收尾 — dashboard 端点 + 生产装配接线 + 文档

**Files:**
- Modify: `app/http/routes/routes.go`、`app/http/handlers/`(GET `/api/mesh/status`:本机 device_id、forward_enable 现值、peers、beacon 候选;写路径走既有 `/api/config/customer` 的 `customer.mesh`,**不做**独立 POST,避免 peer_mcp 两段式复杂度)
- Modify: **生产装配接线(6c9058b 评审回填)**——`egress.SetPeerSource`(从 customer.mesh.Peers[0] 装配 Peer:LANAddrs/SelfID,无 LAN peer 返回 nil)、`egress.SetOfflineSignal` **与 `tcp.SetMeshOfflineSignal`**(829bd11 评审回填:两缝用**同一** manager 隧道状态 getter 同位接线,同源同真假契约见 mesh_offline.go;漏接 tcp 缝=AI 改道永久静默休眠)——均在 app/tunnel validateConfig 与 SetMeshSelfIDSource/SetMeshPikoDialSource 同位;beacon 的生产 Start/Stop 装配(总开关开→Start+SetDefault,关→Stop+SetDefault(nil))
- Modify: `docs/mesh-forwarding-network-design.md` §13(追加记录:平台白名单协调项、`.mesh` 端点软失败行为、piko_proxy_url 帧键、**legacyActionTail 尾部集合与前端 RulesSettings.vue 的同步红线——mesh 尾语法已入集**)

- [ ] **Step 1: handler 测试**(照抄 `agent_peer_gates_handler_test.go` 风格)

- [ ] **Step 2-4: TDD 循环 + `go build ./... && go test ./...` 全绿**
- [ ] **Step 5: 提交** — `git commit -m "新增:mesh 状态查询端点+平台白名单协调记录"`

---

## 执行顺序与依赖

Task 1→2(meshproto)→3(config)→4(routing)→5(mesh 出口,其 Step 0 spike 先行)→6(B 侧)→7(链头)→8(DNS)→9(中继)→10(beacon)→11(egress)→12(AI)→13(集成)→14(收尾)。3/4/5 可在 1-2 完成后并行;6 依赖 1-3;7 依赖 4-6;11/12 依赖 7。

**追加任务 10b(发布前加固,56fd7d1 后 controller 裁决):relay LAN 载体 TLS 化**——常驻自签证书(peer_lan 生成逻辑常驻单例)+ relay listener TLS + A 侧 dialLAN TLS 指纹钉扎(TLS-first+明文回落探测,混合舰队兼容)+ beacon 指纹字段填充 + 既有明文路径兼容窗口。触发条件:对外发布前必做(§8 发布门);内部舰队版本可先明文运行。Task 10 的 beacon 已预留指纹字段。

## 已知风险与预案

0. **(0300244 评审追踪项 M6)** ValidateMesh 在 selfID 为空(未注册首启)时跳过 → 注册前保存的自指 peer 仍会编译通过。Task 5/6 落地 path vector 时**必须覆盖「selfID 为空的链头写 Via」边界**,或注册完成后补跑 ValidateMesh 治愈存量配置——执行到 Task 5/6 时二选一落实,不得跳过。

1. **Task 5 spike(piko dial 他人端点)是最大未知**——若平台白名单/拨号权限不成立,中继载体一期降级为「仅 LAN」,spec §5 的软失败路径兜底,不阻塞 LAN 主场景;
2. mesh 注入点若与 `processor/tcp` 的会话租约/session gate(`inbound/http/server.go:144` 同款)冲突,注入走 TUN 入口等价路径并补会话租约获取;
3. 仓库测试铁律:新测试先 `-list` 确认真实执行(防 GOOS 后缀静默跳过,见 [[go-implicit-goos-suffix-test-trap]]);`-race` 预存两枚 flake 基线勿洗白([[preexisting-race-flakes-transfer-2026-10]])。
