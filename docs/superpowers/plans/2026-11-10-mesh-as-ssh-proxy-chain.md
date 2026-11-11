# 组网收敛(mesh 作为 SSH 代理链出口)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 mesh 从自建帧协议收敛为「SSH 自管 channel(`mesh-exit`)+ 规则语法出口目标」——身份改公钥、防环改注入标记、载体降为 Transport、钉扎 fail-closed。

**Architecture:** 单跳 A→B。A 侧新增 `meshproxy.ExitClient`(Transport 选路 → SSH 握手 → `OpenChannel("mesh-exit", ExtraData payload)`);B 侧 `inbound/ssh` 的 `serveChannels` 按 form×channel 矩阵放行 `mesh-exit`,把流注入既有 processor 管线(`Route: RouteMesh` + `MeshInjected: true` 防环)。旧帧协议路径(`meshproto` 帧、`HandleMeshConn`、`relay.go`、`sniff.KindMesh`)整体删除。

**Tech Stack:** Go(`golang.org/x/crypto/ssh`)、miekg/dns(DNS 代转发搬运)、Vue 3 前端(字段透传 + 三个输入位)、Playwright e2e。

**Spec(唯一事实来源):** `docs/superpowers/specs/2026-11-10-mesh-as-ssh-proxy-chain-design.md`(下称 SPEC)。**行号会漂移,实施以符号名为准**(SPEC 文首约定)。本计划引用格式 `SPEC §x.y`。

**计划对 SPEC 的四处落地决策**(SPEC 未完全钉死,实施按此执行,不另开讨论):

1. **`Peer`/`PeerTarget` 类型整体删除**。`getMeshProxyForExecution` 直接返回缓存于 `TCPConnectionHandler` 的 `*meshproxy.ExitClient`(SPEC §5.1「正确落点」)。ancillary 路径(egress 兜底 peer、TUN DNS 代转发)经 `meshproxy.Dialer` 接口(`DialContext(ctx, md)` 签名,`ExitClient` 天然实现)消费,各自按调用时刻配置构造 `ExitClient`——这两条路径触发频率低(离线兜底/DNS 查询批),构造期解析私钥开销可接受,SPEC §9 已记账 DNS 路径生产接线本就未完成(Task 9 清单)。
2. **B 侧拒绝改用 SSH channel 层 Reject**(无文本拒绝行):form×channel 越界、payload 非法、ExtraData 超限、ForwardGate 关闭都在 `Accept()` **之前** `newCh.Reject(reason, msg)`,A 侧 `OpenChannel` 直接收到失败原因;旧 `WriteMeshRejectLine`/`writeMeshRejectLine` 随 TCP 载体删除。防环拒绝(SPEC §6.2)是 A 侧本地错误,不走线。
3. **`meshOfflineRedirectForAI` 增参**为 `(md *M.Metadata) (addr string, ok bool)`(SPEC §6.2 第 3 条守卫落点;调用方在 `processor/tcp/handler.go` AI 改道分支,符号 `meshOfflineRedirectForAI` 唯一)。
4. **`proto.Mesh` 随 `Peer` 退役**:SPEC §13.1 要求「`Protocol: proto.Mesh` 在 DialContext 重写后确认仍成立」;核实结论 = `mesh.go:75` 的 `Protocol: proto.Mesh` 挂在被删的 `Peer.Base` 上,全仓无非测试消费方,**不再成立 → 退役**,`mesh_assembly_test.go:173` 断言同步改(Task E1 Step 7)。

---

## 文件结构总览(改动地图)

**新建(Go):**
- `outbound/proxy/mesh/spec.go` — `PeerSpec` + `CacheKey` + `SignerFromPEM`
- `outbound/proxy/mesh/transport.go` — `Transport` 接口 + `LANTransport` + `PikoTransport` + `dialLAN`
- `outbound/proxy/mesh/payload.go` — `hostPortFromMetadata` + `exitPayload` + `exitMeta` + 导出缝 `ExitPayloadForMetadata`/`ErrUnbuildableTarget`
- `outbound/proxy/mesh/hostkey.go` — `pinnedHostKey`(钉扎独立成文件,SPEC §10.4 两种解析分支不许合并)
- `outbound/proxy/mesh/sshexit.go` — `ExitClient` + `exitConn` + `DialMeshAddr` + `NewExitClient` + `Dialer` 接口
- `outbound/proxy/mesh/sshexit_test.go` / `payload_test.go` / `transport_test.go`
- `inbound/ssh/mesh_dns.go` — DNS 代转发函数(自 `processor/tcp/mesh_inbound.go` 搬运导出化口径的同包本地版)
- `inbound/ssh/mesh_dns_test.go`
- `processor/tcp/mesh_guard_test.go` — 三处注入守卫测试

**删除(Go):** `inbound/ssh/meshframe.go`、`app/mesh/relay/`(整目录)、`app/tunnel/mesh_dispatch.go`

**重写/大改:** `outbound/proxy/mesh/mesh.go`(瘦身为类别/ctx 助手)、`inbound/ssh/mesh_inject.go`、`processor/tcp/mesh_inbound.go`(只留 `RouteMesh`)、`processor/tcp/handler.go`(mesh 区段)、`app/tunnel/manager.go`(accept 循环)、`inbound/sniff/sniff.go`、`inbound/http/dispatch.go`

**前端:** `app/website/src/components/settings/MeshSettings.js`、`MeshSettings.vue`、`app/website/src/components/SettingsPage.vue`、i18n `zh.js`/`en.js`、`app/website/tests/e2e/mesh-settings.spec.js`

---

## Task 0: worktree 与分支

- [ ] **Step 0.1: 建 worktree(兄弟目录)**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-mesh-sshchain -b fix/mesh-ssh-proxy-chain master
cd ../alianggate-mesh-sshchain
```

- [ ] **Step 0.2: 基线验证**

```bash
go build ./... && go test ./processor/config/... ./outbound/proxy/mesh/... ./inbound/ssh/... 2>&1 | tail -20
```

Expected: 全绿(基线若有预存红,记录在案、不洗白)。

---

# Phase A:配置层(processor/config,零依赖先行)

### Task A1: mesh 新字段 + Validate 必填/规整

**Files:**
- Modify: `processor/config/types.go`(`MeshConfig`/`MeshPeer`/`MeshFallbackConfig` 三个 struct + `Validate`)
- Test: `processor/config/mesh_validate_test.go`(新文件)

- [ ] **Step 1: 写失败测试**

```go
// processor/config/mesh_validate_test.go
package config

import (
	"strings"
	"testing"
)

func TestMeshValidateHostKeyRequired(t *testing.T) {
	c := &MeshConfig{Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "   "}}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "host_key required") {
		t.Fatalf("want host_key required, got %v", err)
	}
}

func TestMeshValidateHostKeyTrimmed(t *testing.T) {
	c := &MeshConfig{Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: " SHA256:abc "}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.Peers[0].HostKey != "SHA256:abc" {
		t.Fatalf("host_key not trimmed in place: %q", c.Peers[0].HostKey)
	}
}

func TestMeshValidatePrivateKeyRequired(t *testing.T) {
	c := &MeshConfig{Enable: meshBoolPtr(true), Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:abc"}}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "private_key required") {
		t.Fatalf("want private_key required, got %v", err)
	}
	c.PrivateKey = " "
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "private_key required") {
		t.Fatalf("trim-only blank must still fail with private_key required, got %v", err)
	}
}

func TestMeshValidateFallbackHostKeyRequiredWhenAddrSet(t *testing.T) {
	on := true
	c := &MeshConfig{Fallback: &MeshFallbackConfig{
		Enable: &on, Addr: "192.168.1.20:56432", HostKey: "",
	}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "fallback.host_key required") {
		t.Fatalf("want fallback.host_key required, got %v", err)
	}
	c.Fallback.HostKey = "SHA256:x"
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestMeshValidatePassphraseTrimmed(t *testing.T) {
	c := &MeshConfig{PrivateKey: "k", KeyPassphrase: " p "}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.KeyPassphrase != "p" {
		t.Fatalf("passphrase not trimmed: %q", c.KeyPassphrase)
	}
}

// meshBoolPtr 避免与 types_test.go 既有的 boolPtr 重名(同包编译冲突)。
func meshBoolPtr(b bool) *bool { return &b }
```

注意:私有 key 为纯空白(`" "`)时,`PrivateKey` 经 TrimSpace 变空 → **仍报 `private_key required`**(上面第 3 个测试的第二次断言已按此语义写好,TrimSpace 规整先于必填判定)。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./processor/config/ -run TestMeshValidate -v
```

Expected: FAIL(字段不存在,编译错误)。

- [ ] **Step 3: 实现 struct 字段**

`processor/config/types.go` 改三个 struct(SPEC §6.1/§10.4/§10.5 的字段注释逐字采用):

```go
type MeshConfig struct {
	Enable        *bool               `json:"enable,omitempty"`
	ForwardEnable *bool               `json:"forward_enable,omitempty"`
	MaxHops       int                 `json:"max_hops,omitempty"` // 一期不消费,仅为二期链长上限占位(SPEC §17)
	Peers         []MeshPeer          `json:"peers,omitempty"`
	Fallback      *MeshFallbackConfig `json:"fallback,omitempty"`
	// PrivateKey 本节点的 mesh 身份私钥(PEM,OpenSSH 或 PKCS#8)。
	// 公钥分发到各对端的 ssh_ingress.mesh_authorized_keys(尾注释=本机 device_id)。
	// 空 = 本节点不能作为转发发起方(仍可作为被转发方)。
	PrivateKey    string `json:"private_key,omitempty"`
	// KeyPassphrase 私钥口令。Validate 会 TrimSpace:首尾真有空格的 passphrase
	// 会被静默改掉导致解不开钥(与 proxy.key_passphrase 既有口径一致,接受)。
	KeyPassphrase string `json:"key_passphrase,omitempty"`
}

type MeshPeer struct {
	DeviceID string   `json:"device_id"`
	LANAddrs []string `json:"lan_addrs,omitempty"`
	Relay    *bool    `json:"relay,omitempty"`
	// HostKey 对端 ssh_ingress 的 host key 钉扎(authorized_keys 行或 SHA256: 指纹)。
	// 空 = 该 peer 不可用(fail-closed,不做 TOFU,SPEC §10.4)。
	HostKey string `json:"host_key,omitempty"`
}

type MeshFallbackConfig struct {
	Enable  *bool    `json:"enable,omitempty"`
	Classes []string `json:"classes,omitempty"`
	Addr    string   `json:"addr,omitempty"`
	// HostKey 兜底节点 SSH host key 钉扎;enable=true 且 addr 非空时必填(fail-closed)。
	HostKey string `json:"host_key,omitempty"`
}
```

- [ ] **Step 4: 实现 Validate 增量**(在 `MeshConfig.Validate` 现有 peer 循环内与 fallback 分支内插入;`TrimSpace` 就地规整先于必填判定,SPEC §6.1)

peer 循环内追加:

```go
peer.HostKey = strings.TrimSpace(peer.HostKey)
if peer.HostKey == "" {
	return fmt.Errorf("customer.mesh.peers[%d].host_key required (peer host key must be pinned, no TOFU)", i)
}
```

fallback 分支内(在既有 `Addr` 校验之后)追加:

```go
c.Fallback.HostKey = strings.TrimSpace(c.Fallback.HostKey)
if c.Fallback.HostKey == "" {
	return fmt.Errorf("customer.mesh.fallback.host_key required when fallback enabled (no TOFU)")
}
```

函数尾部(既有 return nil 之前)追加:

```go
c.PrivateKey = strings.TrimSpace(c.PrivateKey)
c.KeyPassphrase = strings.TrimSpace(c.KeyPassphrase)
if len(c.Peers) > 0 && c.PrivateKey == "" {
	return fmt.Errorf("customer.mesh.private_key required when peers configured (node identity key)")
}
```

- [ ] **Step 5: 跑测试确认通过**

```bash
go test ./processor/config/ -run TestMeshValidate -v
```

Expected: PASS。再跑全包 `go test ./processor/config/...` 确认无回归。

- [ ] **Step 6: Commit**

```bash
git add processor/config/types.go processor/config/mesh_validate_test.go
git commit -m "新增:mesh 配置新字段——节点私钥/对端钉扎/兜底钉扎 + 保存期必填与规整"
```

### Task A2: `ValidateMeshDependency` + `MeshEffectivelyEnabled` + 判据切换

**Files:**
- Modify: `processor/config/types.go`
- Modify: `processor/config/mesh_fallback_gate_test.go`(判据切换后既有断言更新)
- Test: `processor/config/mesh_dependency_test.go`(新)

- [ ] **Step 1: 写失败测试**

```go
// processor/config/mesh_dependency_test.go
package config

import "testing"

func meshCfgFor(on bool) *Config {
	sshOn := on
	return &Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: &sshOn, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "h"}}},
		SSHIngress: &SSHIngressConfig{Enable: &sshOn},
	}}
}

func TestMeshEffectivelyEnabledFollowsSSHIngress(t *testing.T) {
	if !meshCfgFor(true).MeshEffectivelyEnabled() {
		t.Fatal("both on → effective")
	}
	if meshCfgFor(false).MeshEffectivelyEnabled() {
		t.Fatal("ssh off → degraded")
	}
	var nilCfg *Config
	if nilCfg.MeshEffectivelyEnabled() {
		t.Fatal("nil config → false")
	}
}

func TestValidateMeshDependencyFourCombos(t *testing.T) {
	on, off := true, false
	mk := func(mesh, ssh *bool) *Config {
		return &Config{Customer: &CustomerConfig{
			Mesh:       &MeshConfig{Enable: mesh, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "d", HostKey: "h"}}},
			SSHIngress: &SSHIngressConfig{Enable: ssh},
		}}
	}
	if err := mk(&on, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh+ssh: %v", err)
	}
	if err := mk(&off, &off).ValidateMeshDependency(); err != nil {
		t.Fatalf("both off: %v", err)
	}
	if err := mk(&off, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh off: %v", err)
	}
	if err := mk(&on, &off).ValidateMeshDependency(); err == nil {
		t.Fatal("mesh on + ssh off must be rejected at save time")
	}
}

func TestMeshFallbackHostKey(t *testing.T) {
	on := true
	cfg := &Config{Customer: &CustomerConfig{Mesh: &MeshConfig{
		Fallback: &MeshFallbackConfig{Enable: &on, Addr: "1.2.3.4:56432", HostKey: " SHA256:x "},
	}}}
	if got := MeshFallbackHostKey(cfg); got != "SHA256:x" {
		t.Fatalf("want trimmed SHA256:x, got %q", got)
	}
	if got := MeshFallbackHostKey(nil); got != "" {
		t.Fatalf("nil cfg → empty, got %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./processor/config/ -run "MeshEffectively|MeshDependency|MeshFallbackHostKey" -v
```

Expected: FAIL(符号不存在)。

- [ ] **Step 3: 实现(SPEC §7.2/§10.3 代码逐字采用)**

`processor/config/types.go` 追加三个函数(SPEC §10.3 的 `MeshEffectivelyEnabled` 代码块、§7.2 的 `MeshFallbackHostKey` 代码块原样;`ValidateMeshDependency` 仿 `ValidateMesh` 接收者形态):

```go
// MeshEffectivelyEnabled mesh 出口的生效开关 = 用户开关 ∧ SSH 入口可用。
// 依赖降级(SPEC §10.3):SSH 入口未启用时 mesh 无出口载体,恒 false——
// 存量配置(mesh 开、ssh 未配)升级后 mesh 静默降级为关,而不是启动失败。
// 保存期由 ValidateMeshDependency 硬拒,两条路径不重复报错。
func (c *Config) MeshEffectivelyEnabled() bool {
	if c == nil || c.Customer == nil {
		return false
	}
	if !c.Customer.Mesh.EffectiveEnabled() {
		return false
	}
	return c.Customer.SSHIngress.IsEnabled()
}

// ValidateMeshDependency 保存期依赖硬拒(SPEC §10.3):开 mesh 必须开 SSH
// 入口。启动期不调用本函数(Config.Validate 也不调)——启动降级走
// MeshEffectivelyEnabled,绝不阻断 boot。
func (c *Config) ValidateMeshDependency() error {
	if c == nil || c.Customer == nil || c.Customer.Mesh == nil {
		return nil
	}
	if c.Customer.Mesh.EffectiveEnabled() && !c.Customer.SSHIngress.IsEnabled() {
		return fmt.Errorf("组网依赖 SSH 入口(ssh_ingress.enable):开启 customer.mesh.enable 前必须先开启 SSH 入口")
	}
	return nil
}

// MeshFallbackHostKey 返回兜底节点的 SSH host key 钉扎(未配置=空串=fail-closed)。
func MeshFallbackHostKey(cfg *Config) string {
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil || cfg.Customer.Mesh.Fallback == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Customer.Mesh.Fallback.HostKey)
}
```

- [ ] **Step 4: 切换 `MeshFallbackActiveForClass` 判据**(SPEC §10.3 表第 1 行)

`processor/config/types.go` `MeshFallbackActiveForClass` 内 `if !mesh.EffectiveEnabled()` → `if !cfg.MeshEffectivelyEnabled()`(函数注释同步改一行:「总开关检查 = MeshEffectivelyEnabled(含 SSH 入口依赖降级,SPEC §10.3)」)。

- [ ] **Step 5: 跑包测试,更新 `mesh_fallback_gate_test.go` 既有断言**

```bash
go test ./processor/config/... ./processor/egress/... ./processor/tcp/... ./processor/routing/... 2>&1 | tail -30
```

`mesh_fallback_gate_test.go` 里 ssh_ingress 未置位导致 fail 的用例:给测试构造的 `Config` 补 `SSHIngress: &SSHIngressConfig{Enable: boolPtr(true)}`。**逐条更新断言语义而非删除**;更新后在文件顶部加一行注释:「判据含 SSH 入口依赖(SPEC §10.3),用例须显式开 ssh_ingress」。

- [ ] **Step 6: 全绿后 Commit**

```bash
git add processor/config/
git commit -m "新增:mesh↔SSH入口依赖门——MeshEffectivelyEnabled 降级 + 保存期硬拒 + 兜底钉扎助手"
```

### Task A3: snapshot_mesh.go 编译期能力判定升级(SPEC §13.2,评审补)

**Files:**
- Modify: `processor/routing/snapshot_mesh.go`(`meshForwardAvailable`、`meshDNSForwardTargetFromSnapshot`)
- Test: `processor/routing/snapshot_mesh_test.go`(既有,追加用例)

- [ ] **Step 1: 写失败测试**(三组)

```go
// ① meshForwardAvailable:SSH 依赖降级穿透到编译期(SPEC §10.3)
func TestMeshForwardAvailableSSHDependency(t *testing.T) {
	on, off := true, false
	mk := func(ssh *bool, privKey, hostKey string) *config.Config {
		return &config.Config{Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{
				Enable: &on, PrivateKey: privKey,
				Peers: []config.MeshPeer{{DeviceID: "dev-b", HostKey: hostKey}},
			},
			SSHIngress: &config.SSHIngressConfig{Enable: ssh},
		}}
	}
	if !meshForwardAvailable(mk(&on, "k", "h"), "dev-b") {
		t.Fatal("all set → available")
	}
	if meshForwardAvailable(mk(&off, "k", "h"), "dev-b") {
		t.Fatal("ssh off → degraded unavailable")
	}
	if meshForwardAvailable(mk(&on, "", "h"), "dev-b") {
		t.Fatal("no identity key → unavailable")
	}
	if meshForwardAvailable(mk(&on, "k", ""), "dev-b") {
		t.Fatal("unpinned peer → unavailable (fail-closed)")
	}
}

// ② meshDNSForwardTargetFromSnapshot:relay-only peer 放行(SPEC §13.2 ③)
func TestMeshDNSForwardTargetRelayOnlyPeer(t *testing.T) {
	// peer: DeviceID=dev-b, LANAddrs 空, Relay=true(跨 NAT 形态)
	// → 期望 ok=true(交 Transport 选 piko),不再被 len(LANAddrs)==0 静默关闭
}
```

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现**

`meshForwardAvailable` 改(SPEC §13.2 ②):

```go
func meshForwardAvailable(cfg *config.Config, deviceID string) bool {
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return false
	}
	mesh := cfg.Customer.Mesh
	if !cfg.MeshEffectivelyEnabled() { // SSH 依赖降级一处生效(SPEC §10.3/§13.2 ①)
		return false
	}
	if strings.TrimSpace(mesh.PrivateKey) == "" {
		return false // 无身份钥 = 本机不能作发起方,能力不成立(SPEC §13.2 ②)
	}
	for _, peer := range mesh.Peers {
		if strings.TrimSpace(peer.DeviceID) == deviceID {
			if strings.TrimSpace(peer.HostKey) == "" {
				return false // 未钉扎 peer 能力不成立(fail-closed,SPEC §13.2 ②)
			}
			return true
		}
	}
	return false
}
```

(函数注释同步:补「含 SSH 入口依赖降级与钉扎/身份能力检查(SPEC §13.2)」一行。)

`meshDNSForwardTargetFromSnapshot` 删除 `len(peer.LANAddrs) == 0 → ok=false` 分支,注释改写:「relay-only peer(跨 NAT)交 Transport 选 piko(SPEC §13.2 ③);无 LAN 地址不再静默关闭 DNS 代转发」。

- [ ] **Step 4: 测试通过 + `go test ./processor/routing/...` 全绿 → Commit**

```bash
git add processor/routing/
git commit -m "修复:mesh 编译期能力判定——SSH依赖降级+钉扎/身份必查+relay-only DNS代转发放行(SPEC §13.2)"
```

### Task A4: 规则目标 fallback 段纳入 host_key 能力校验

**Files:**
- Modify: `processor/config/routing_schema.go`(mesh target 分支,符号 `ParseMeshRuleTarget` 的消费点)
- Test: `processor/config/routing_schema_test.go`(既有文件追加用例)

- [ ] **Step 1: 写失败测试**(追加到既有 routing_schema 测试;若文件名不同,`grep -rn "ParseMeshRuleTarget" processor/config/*_test.go` 定位)

用例:`routing.rules[].target = "toNode:dev-b:fallback:dev-c"`,`customer.mesh.peers` 含 dev-b(带 HostKey)但 **不含 dev-c** → 校验**通过**(SPEC §6.1 表:fallback 目标不要求已列入 peers,运行期失败策略收口——这里校验的只是「若 id2 也在 peers 里,其 HostKey 不能为空」)。第二个用例:dev-c 也在 peers 里但 `HostKey: ""` → **拒绝**,错误含 `host_key`。

```go
func TestMeshRoutingSchemaFallbackID2HostKey(t *testing.T) {
	// … 构造 cfg:routing.rules 一条 toNode:dev-b:fallback:dev-c
	// 用例 1:peers 只有 dev-b(HostKey 有值)→ schema 校验过
	// 用例 2:peers 有 dev-b+dev-c,dev-c.HostKey=="" → 拒,文案含 host_key
}
```

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现**

在 mesh target 分支(`if deviceID, policy, isMesh := ParseMeshRuleTarget(target); isMesh {`)内、`continue` 之前加:

```go
// SPEC §6.1/§13.1:fallback 段的 id2 若也配置在 peers 里,其 host_key 必须已钉扎
// (fail-closed 与主目标同口径);不在 peers 里则运行期失败策略收口,不在此拦。
if id2, isFb := strings.CutPrefix(policy, MeshPolicyFallback+":"); isFb && id2 != "" {
	for j := range cfg.Customer.Mesh.Peers {
		p := &cfg.Customer.Mesh.Peers[j]
		if strings.TrimSpace(p.DeviceID) == id2 && strings.TrimSpace(p.HostKey) == "" {
			return newRoutingSchemaValidationError("invalid_target", targetField,
				fmt.Sprintf("mesh fallback peer %q is configured but has empty host_key (pin required)", id2))
		}
	}
}
```

(注意:该函数是否拿得到 `cfg.Customer.Mesh` 需核对其接收对象;函数签名里已有 `cfg`。若 peers 为 nil,循环自然跳过。)

- [ ] **Step 4: 测试通过 → Commit**

```bash
git add processor/config/routing_schema.go processor/config/routing_schema_test.go
git commit -m "新增:mesh 规则 fallback 段目标若在 peers 中则要求已钉扎 host_key"
```

### Task A5: 绑定面判据换 `SSHIngress`(SPEC §5.5)

**Files:**
- Modify: `processor/config/defaults.go`(`UnifiedIngressListenAddrFor`)
- Modify(注释同步): `app/http/services/customer_config_service.go`(「刻意的『模式无关』」段)、`app/http/services/run.go:44` 附近、`cmd/core.go`(ReconcileUnifiedIngressBinding 调用点附近注释)、`cmd/start.go`、`cmd/tray.go`——`grep -rn "mesh 总开关" cmd/ app/http/services/` 找齐全部提及「随 mesh 总开关变化」的注释
- Test: `processor/config/defaults_test.go`(既有,定位:`grep -rn "UnifiedIngressListenAddrFor" processor/config/*_test.go`)

- [ ] **Step 1: 改测试为 SSH 判据**(mesh 开+ssh 关 → 回环;ssh 开 → 0.0.0.0,与 mesh 无关)

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现**

```go
func UnifiedIngressListenAddrFor(cfg *Config) string {
	host := ServiceBindHost()
	if cfg != nil && cfg.Customer != nil && cfg.Customer.SSHIngress.IsEnabled() {
		host = "0.0.0.0" // LAN 可达面由 ssh_ingress 决定(SPEC §5.5;对称风险见发布说明)
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", DefaultHTTPProxyPort))
}
```

注释整段重写(引用 SPEC §5.5/§5.5.1)。四处调用点注释同步(功能不动,只改文字:提及「mesh 总开关」处改为「ssh_ingress 开关,SPEC §5.5」)。

- [ ] **Step 4: 测试通过 + `go build ./...` → Commit**

```bash
git add processor/config/defaults.go app/http/services/ cmd/
git commit -m "修复:56432 绑定面判据由 mesh 总开关换为 ssh_ingress 开关(SPEC §5.5,对称风险随发布说明)"
```

---

# Phase B:防环地基

### Task B1: `Metadata.MeshInjected` + 三处守卫 + engine 注释

**Files:**
- Modify: `inbound/tun/metadata/metadata.go`(struct 追加字段)
- Modify: `processor/tcp/handler.go`(`dialViaMesh` 顶部 + `dialMeshFallbackNode` 顶部)
- Modify: `processor/tcp/route_override.go`(`meshOfflineRedirectForAI` 增参 + 守卫;调用方 `handler.go` AI 改道分支传 `metadata`)
- Modify: `processor/rules/engine.go`(`RouteToNode/RouteMeshFallback` case 上方加前提注释,不改逻辑)
- Test: `processor/tcp/mesh_guard_test.go`(新)

- [ ] **Step 1: 写失败测试**

```go
// processor/tcp/mesh_guard_test.go
package tcp

import (
	"context"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

func TestDialViaMeshRejectsInjected(t *testing.T) {
	h := &TCPConnectionHandler{}
	md := &M.Metadata{Route: "toNode:dev-b", MeshInjected: true}
	_, err := h.dialViaMesh(context.Background(), md)
	if err == nil || !isMeshLoopGuardErr(err) {
		t.Fatalf("injected traffic must be rejected with loop guard error, got %v", err)
	}
}

func TestDialMeshFallbackNodeRejectsInjected(t *testing.T) {
	h := &TCPConnectionHandler{}
	md := &M.Metadata{MeshInjected: true}
	if _, err := h.dialMeshFallbackNode(context.Background(), md, "1.2.3.4:56432"); !isMeshLoopGuardErr(err) {
		t.Fatalf("fallback dial must reject injected traffic, got %v", err)
	}
}

func isMeshLoopGuardErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "loop guard")
}
```

(`strings` 与 `errors` 按需 import;守卫错误定义 `var errMeshInjectedLoop = errors.New("mesh: injected traffic must not re-enter mesh (loop guard)")` 于 handler.go mesh 区段。)

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./processor/tcp/ -run "RejectsInjected" -v
```

Expected: FAIL(MeshInjected 字段不存在)。

- [ ] **Step 3: 实现**

1. `inbound/tun/metadata/metadata.go` struct 末尾追加(SPEC §6.2 注释逐字):

```go
// MeshInjected 标记本连接由 mesh-exit 注入(公钥认证的对端转发而来)。
// 路由决策全程不写该字段(tls.go 决策只写 Route),故注入标记可存活到
// dialViaMesh。json:"-" 不入任何线格式。
MeshInjected bool `json:"-"`
```

2. `handler.go` mesh 区段追加 `var errMeshInjectedLoop = errors.New(...)`;`dialViaMesh` 函数体首行加 `if metadata.MeshInjected { return nil, errMeshInjectedLoop }`;`dialMeshFallbackNode` 首行同。
3. `route_override.go`:`meshOfflineRedirectForAI()` → `meshOfflineRedirectForAI(md *M.Metadata) (string, bool)`,函数体首行 `if md != nil && md.MeshInjected { return "", false }`;更新其调用点(实参传当前 metadata)。
4. `engine.go` 的 `case "RouteToNode", "RouteMeshFallback":` 上方加两行注释(SPEC §6.2 末):「RouteMesh 不在名单里的前提:mesh 注入路径直接赋值 HostName、不走 SetHostName,DNSInfo 恒 nil,根本到不了 StoreBinding。若将来给注入连接补 DNSInfo,必须同时加 RouteMesh 分支。」另在 `case` 列表里**不加** `RouteMesh`(SPEC 结论:不加)。

- [ ] **Step 4: 测试通过 + `go build ./... && go test ./processor/...` → Commit**

```bash
git add inbound/tun/metadata/metadata.go processor/tcp/ processor/rules/engine.go
git commit -m "新增:mesh 注入防环——Metadata.MeshInjected 标记 + 三处拨号入口守卫"
```

---

# Phase C:出口侧新代码(outbound/proxy/mesh)

> 本 Phase 全部为**新增文件**,旧 `Peer`/`PeerTarget` 仍在编译,Task E1 才做切换。每个 Task 结束 `go build ./... && go test ./outbound/proxy/mesh/...` 保持全绿。

### Task C1: `PeerSpec` + `SignerFromPEM` + `CacheKey`

**Files:**
- Create: `outbound/proxy/mesh/spec.go`
- Test: `outbound/proxy/mesh/spec_test.go`

- [ ] **Step 1: 失败测试**

```go
// outbound/proxy/mesh/spec_test.go
package mesh

import "testing"

func TestPeerSpecCacheKeyOrderInsensitive(t *testing.T) {
	a := PeerSpec{DeviceID: "dev-b", LANAddrs: []string{"1.1.1.1:56432", "2.2.2.2:56432"}, HostKey: "h", SSHUser: "mesh", Relay: true}
	b := PeerSpec{DeviceID: "dev-b", LANAddrs: []string{"2.2.2.2:56432", "1.1.1.1:56432"}, HostKey: "h", SSHUser: "mesh", Relay: true}
	if a.CacheKey("pk", "") != b.CacheKey("pk", "") {
		t.Fatal("same set, different order → same key")
	}
	if a.CacheKey("pk", "") == a.CacheKey("pk2", "") {
		t.Fatal("private key change must change key")
	}
	if a.CacheKey("pk", "") == a.CacheKey("pk", "pw") {
		t.Fatal("passphrase change must change key")
	}
	c := a
	c.HostKey = "h2"
	if a.CacheKey("pk", "") == c.CacheKey("pk", "") {
		t.Fatal("host key change must change key")
	}
}
```

- [ ] **Step 2: 确认失败** → **Step 3: 实现**(SPEC §7.0 注释逐字 + §10.4 键定义)

```go
// outbound/proxy/mesh/spec.go
package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// meshSSHUser 是 mesh 节点间 SSH 握手的固定用户名(无语义,仅为服务端
// 日志可读;准入只看公钥,不看用户)。
const meshSSHUser = "mesh"

// PeerSpec 一个 mesh 出口节点的全部拨号事实(配置期确定,运行期只读)。
//
// 注意:**没有 Class 字段**。流量类别是**逐连接**的,不是节点的属性——
// 同一个对端会同时承载 self/ai/custom 三类流量(classFromContext:优先
// ctx 的 WithClass,否则按 metadata 推断)。把它塞进 PeerSpec 会让「这个
// peer 属于哪一类」成为一个无解的问题。类别在 ExitClient.DialContext 内
// 按连接解析(SPEC §5.1)。
type PeerSpec struct {
	DeviceID string   // 对端 mesh 身份 ID(也是 SSH 握手的 host 标签)
	LANAddrs []string // 对端 SSH 入口地址(默认 IP:56432,SPEC §5.2)
	HostKey  string   // 对端 SSH host key 钉扎;空 = fail-closed(SPEC §10.4)
	SSHUser  string   // 固定 "mesh"
	Relay    bool     // true = 允许中继 Transport
}

// CacheKey 计算该 peer 在 ExitClient 缓存中的键(SPEC §10.4):全部影响
// 握手行为的配置值 + 私钥摘要。LANAddrs 排序后拼接(顺序无关)。
func (spec PeerSpec) CacheKey(privateKeyPEM, passphrase string) string {
	addrs := append([]string(nil), spec.LANAddrs...)
	sort.Strings(addrs)
	sum := sha256.Sum256([]byte(privateKeyPEM + "\x00" + passphrase))
	parts := []string{
		spec.DeviceID,
		strings.Join(addrs, "|"),
		spec.HostKey,
		strconv.FormatBool(spec.Relay),
		spec.SSHUser,
		hex.EncodeToString(sum[:]),
	}
	return strings.Join(parts, "\x1f")
}

// SignerFromPEM 解析本机 mesh 身份私钥(OpenSSH 或 PKCS#8 PEM,可带口令)。
func SignerFromPEM(privateKeyPEM, passphrase string) (gossh.Signer, error) {
	if strings.TrimSpace(privateKeyPEM) == "" {
		return nil, errors.New("mesh: private key not configured")
	}
	if passphrase == "" {
		return gossh.ParsePrivateKey([]byte(privateKeyPEM))
	}
	return gossh.ParsePrivateKeyWithPassphrase([]byte(privateKeyPEM), []byte(passphrase))
}
```

- [ ] **Step 4: 测试通过 → Commit**

```bash
git add outbound/proxy/mesh/spec.go outbound/proxy/mesh/spec_test.go
git commit -m "新增:mesh PeerSpec 与 ExitClient 缓存键(含私钥摘要)"
```

### Task C2: Transport 接口 + LAN/Piko 实现

**Files:**
- Create: `outbound/proxy/mesh/transport.go`
- Test: `outbound/proxy/mesh/transport_test.go`

- [ ] **Step 1: 失败测试**(SPEC §15.1 的 Admit 矩阵)

```go
// outbound/proxy/mesh/transport_test.go
package mesh

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTransportAdmitMatrix(t *testing.T) {
	peerWithAddr := PeerSpec{LANAddrs: []string{"1.2.3.4:56432"}}
	peerNoAddr := PeerSpec{}
	lan := LANTransport{}
	piko := PikoTransport{dial: func(context.Context, string) (net.Conn, error) { return nil, nil }}

	if !lan.Admit(ClassCustom, peerWithAddr) || !lan.Admit(ClassSelf, peerWithAddr) || !lan.Admit(ClassAI, peerWithAddr) {
		t.Fatal("lan admits all classes when addrs present")
	}
	if lan.Admit(ClassCustom, peerNoAddr) || lan.Admit(ClassAI, peerNoAddr) {
		t.Fatal("lan requires non-empty LANAddrs regardless of class")
	}
	if piko.Admit(ClassCustom, peerWithAddr) {
		t.Fatal("custom must not ride relay")
	}
	if !piko.Admit(ClassSelf, peerWithAddr) || !piko.Admit(ClassAI, peerWithAddr) {
		t.Fatal("self/ai may ride relay")
	}
}

func TestPikoTransportBuildsEndpointID(t *testing.T) {
	var got string
	piko := PikoTransport{dial: func(_ context.Context, endpointID string) (net.Conn, error) {
		got = endpointID
		return nil, nil
	}}
	_, _ = piko.DialSSHD(context.Background(), PeerSpec{DeviceID: "dev-b"})
	if got != "dev-b.mesh" {
		t.Fatalf("endpoint id = %q", got)
	}
}

// TestLanTransportDialsInOrder 钉住 LAN 顺序尝试:第一个地址失败落第二个。
func TestLanTransportDialsInOrder(t *testing.T) {
	// 127.0.0.1:1 必然 connection refused;第二个口起真监听。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	conn, derr := LANTransport{}.DialSSHD(context.Background(), PeerSpec{
		LANAddrs: []string{"127.0.0.1:1", net.JoinHostPort("127.0.0.1", port)},
	})
	if derr != nil {
		t.Fatalf("second addr should succeed: %v", derr)
	}
	_ = conn.Close()
	_ = time.AfterFunc(time.Second, func() {})
}
```

- [ ] **Step 2: 确认失败** → **Step 3: 实现**

```go
// outbound/proxy/mesh/transport.go
package mesh

import (
	"context"
	"net"
	"time"

	"aliang.one/nursorgate/outbound/meshproto"
)

// Transport 提供一条到对端 sshd 的、尚未握手的 net.Conn。无状态,可跨拨号复用。
type Transport interface {
	Name() string                           // "lan" / "piko" / 未来 "udp"
	Admit(class string, peer PeerSpec) bool // 载体准入:custom 禁走公共中继;LAN 还需有地址
	DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error)
}

// lanDialTimeout 是 LAN 载体单地址拨号兜底超时;ctx 自带 deadline 时以 ctx
// 为准(更紧者生效)。
const lanDialTimeout = 5 * time.Second

// LANTransport LAN 直连载体:peer.LANAddrs 顺序尝试,全类别可达,但无地址
// 时 admit=false(空地址 peer 落到 errNoTransportAdmits,而不是报一个含
// 0 个地址的费解错误,SPEC §7.1)。
type LANTransport struct{}

func (LANTransport) Name() string { return "lan" }

func (LANTransport) Admit(_ string, peer PeerSpec) bool { return len(peer.LANAddrs) > 0 }

func (LANTransport) DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error) {
	var lastErr error
	for _, addr := range peer.LANAddrs {
		conn, err := dialLAN(ctx, addr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return nil, lastErr
}

// PikoTransport piko 中继载体:endpointID 由本 Transport 拼装
// (`<deviceID>.mesh`),注入的 dial func 只收拼好的 endpointID。类别准入:
// 仅 self/ai(custom 禁公共中继,SPEC §4.3/§7.1);relay 未接线时
// ExitClient 构造期就不会把本 Transport 放进切片(SPEC §7.3)。
type PikoTransport struct {
	dial PikoDialFunc
}

func (PikoTransport) Name() string { return "piko" }

func (PikoTransport) Admit(class string, _ PeerSpec) bool {
	return class == meshproto.ClassSelf || class == meshproto.ClassAI
}

func (t PikoTransport) DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error) {
	return t.dial(ctx, peer.DeviceID+meshRelayEndpointSuffix)
}

func dialLAN(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		d.Timeout = lanDialTimeout
	}
	return d.DialContext(ctx, "tcp", addr)
}
```

**与旧 mesh.go 的符号冲突处置(必须同 Task 完成)**:本 Task 的 transport.go 与旧 `mesh.go` 各有一份 `dialLAN`/`lanDialTimeout` 会同包重名——**Task C2 直接删除旧 `mesh.go` 的 `lanDialCandidates`/`dialLANFn`/`dialLAN`/`lanDialTimeout`**,并同步删除旧 `mesh_test.go` 里引用它们的用例(`TestDialContextLANCandidate*` 族——旧帧协议路径的测试随 Task E1/F1 大重构重写,此处先删使编译绿)。旧 `Peer.DialContext` 的 LAN 段改为临时内联(该函数 Task E1 整体删除,不值得维护):

```go
var lastErr error
for _, addr := range p.target.LANAddrs {
	var d net.Dialer
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		d.Timeout = 5 * time.Second
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		lastErr = err
		continue
	}
	if err := writeHeader(conn, line); err != nil {
		_ = conn.Close()
		lastErr = fmt.Errorf("mesh: write header to %s: %w", addr, err)
		continue
	}
	proxy.SetKeepAlive(conn)
	return conn, nil
}
```

- [ ] **Step 4: `go build ./... && go test ./outbound/proxy/mesh/...` 全绿 → Commit**

```bash
git add outbound/proxy/mesh/
git commit -m "新增:mesh Transport 抽象——LAN/Piko 双载体 + 类别准入矩阵"
```

### Task C3: `hostPortFromMetadata` + `exitPayload` + `pinnedHostKey`

**Files:**
- Create: `outbound/proxy/mesh/payload.go`、`outbound/proxy/mesh/hostkey.go`(钉扎独立成文件,SPEC §10.4 两种解析分支不许合并)
- Test: `outbound/proxy/mesh/payload_test.go`、`outbound/proxy/mesh/hostkey_test.go`

- [ ] **Step 1: 失败测试**(SPEC §15.1 四形态 + 钉扎矩阵)

```go
// payload_test.go 关键用例(骨架,补全断言)
func TestHostPortFromMetadataFourForms(t *testing.T) {
	cases := []struct {
		name       string
		hostName   string
		dstIP      netip.Addr
		dstPort    uint16
		wantHost   string
		wantPort   string
	}{
		{"bare domain", "x.com", netip.Addr{}, 443, "x.com", "443"},
		{"host with port (CONNECT)", "x.com:8443", netip.Addr{}, 8443, "x.com", "8443"},
		{"bare ipv4", "1.2.3.4", netip.MustParseAddr("1.2.3.4"), 0, "1.2.3.4", "443"},
		{"bare ipv6 no brackets", "::1", netip.MustParseAddr("::1"), 0, "::1", "443"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port, ok := hostPortFromMetadata(&M.Metadata{HostName: c.hostName, DstIP: c.dstIP, DstPort: c.dstPort})
			if !ok || host != c.wantHost || port != c.wantPort {
				t.Fatalf("got (%q,%q,%v) want (%q,%q,true)", host, port, ok, c.wantHost, c.wantPort)
			}
		})
	}
}

func TestExitPayloadVariants(t *testing.T) {
	if got := exitPayload(&M.Metadata{HostName: "x.com", DstPort: 443}); string(got) != `{"t":"tcp","h":"x.com","p":443}` {
		t.Fatalf("tcp payload = %s", got)
	}
	if got := exitPayload(&M.Metadata{HostName: "dns:"}); string(got) != `{"t":"dns"}` {
		t.Fatalf("dns payload = %s", got)
	}
	if got := exitPayload(nil); got != nil {
		t.Fatalf("nil md must yield nil payload, got %s", got)
	}
	if got := exitPayload(&M.Metadata{HostName: "   "}); got != nil {
		t.Fatalf("unbuildable target must yield nil, got %s", got)
	}
	if got := exitPayload(&M.Metadata{HostName: "x.com:99999", DstPort: 0}); got != nil {
		t.Fatalf("port overflow must yield nil, got %s", got)
	}
}
```

```go
// hostkey_test.go:两种输入 × {正确✓, 错误✗, 空 fail-closed} + 坏配置拒绝两次
func TestPinnedHostKeyMatrix(t *testing.T) { /* 表驱动,断言回包 error 有/无 */ }
func TestPinnedHostKeyBadConfigRejectsTwice(t *testing.T) {
	cb := pinnedHostKey("not-a-valid-key-or-fingerprint")
	bad := errors.New("x")
	if err := cb("h", nil, pub); err == nil || !strings.Contains(err.Error(), "bad host_key config") {
		t.Fatalf("first call: %v", err)
	}
	if err := cb("h", nil, pub); err == nil {
		t.Fatal("second call must still reject (closure cached per ExitClient)")
	}
	_ = bad
}
```

(`pub` 用 `testMakePub(t)` 生成 ed25519 测试钥公钥——包内已有测试钥生成先例的话复用,没有就在测试文件里生成。)

- [ ] **Step 2: 确认失败** → **Step 3: 实现**

`payload.go`——SPEC §5.1.1 两个函数**逐字采用**(`hostPortFromMetadata`/`exitPayload` + `exitMeta{T,H,P}`,`exitMeta.P` 为 `uint16` JSON 数字),补 import(`encoding/json`/`net`/`strconv`/`strings` + `M` + `meshproto`)。

`hostkey.go`——SPEC §10.4 的 `pinnedHostKey` **逐字采用**。

- [ ] **Step 4: 测试通过 → Commit**

```bash
git add outbound/proxy/mesh/payload.go outbound/proxy/mesh/hostkey.go outbound/proxy/mesh/*_test.go
git commit -m "新增:mesh exitPayload 四形态拆端口 + host key 钉扎(fail-closed 双格式)"
```

### Task C4: `ExitClient` + `exitConn` + `DialMeshAddr`

**Files:**
- Create: `outbound/proxy/mesh/sshexit.go`
- Test: `outbound/proxy/mesh/sshexit_test.go`

- [ ] **Step 1: 失败测试**(真实回环 sshd——参照包内既有 sshtest 先例,`grep -rn "NewServerConn" outbound/ --include="*_test.go"` 找既有测试 sshd 构造;没有则用 gossh 自建)

测试矩阵(SPEC §15.1 摘 mesh 出口相关):
1. **TestExitClientDialTCP**:本机起真 sshd(接受固定 mesh 公钥),`ExitClient.DialContext` → 服务端收到 `mesh-exit` channel + ExtraData 正确 → 服务端 Accept 回显 → 客户端 Read/Write 双向通。
2. **TestExitClientLanPreferredOverRelay**:relay=true 且 LAN 可达(本地假 sshd)时只拨 LAN——注入 fake `dial` 计数,piko 拨号计数必须为 0。
3. **TestExitClientLanFailFallsBackToRelay**:LAN 地址全 refused → 落 piko fake(计数 1),错误为 nil。
4. **TestExitClientNoTransportAdmits**:① 空 LANAddrs + Relay=false;② 空 LANAddrs + Relay=true + class=custom;③ 空 LANAddrs + Relay=true + class=ai + piko 未接线。三者都返回 `errNoTransportAdmits` 且 **fake dial 计数 0**。
5. **TestExitClientUnbuildableTarget**:`md` 空 HostName/DstIP → `errUnbuildableTarget`,零拨号。
6. **TestExitClientPikoNotInSliceWhenUnwired**:Relay=true 但 pikoDial=nil → transports 只有 LAN(admit 后不 panic)。
7. **TestExitClientHandshakeDeadline**:对端 accept TCP 后不发 SSH banner → 在 `meshExitHandshakeTimeout`(测试覆写为 300ms)内返回错误。
8. **TestDialMeshAddrRejectsEmptyHostKey**:hostKey="" → fail-closed 错误。
9. **TestExitClientCustomClassDialsLanOnly**:WithClass(ClassCustom) + Relay=true + LAN 可达 → 走 LAN(计数 0 piko)。

- [ ] **Step 2: 确认失败** → **Step 3: 实现**(SPEC §5.1 代码为主体,补构造器/conn 适配/兜底入口)

```go
// outbound/proxy/mesh/sshexit.go
package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"

	gossh "golang.org/x/crypto/ssh"
)

// ChannelMeshExit 是 mesh 出口在 SSH 上的自管 channel 名(B 侧 serveChannels
// 的 form×channel 矩阵同源常量,在线协议,SPEC §4)。
const ChannelMeshExit = "mesh-exit"

// meshExitMaxExtra 是 mesh-exit ExtraData 服务端上限(与 B 侧同值;正常
// payload 恒 < 100B)。
const meshExitMaxExtra = 512

// meshExitHandshakeTimeout 界定 ExitClient 拨号+握手窗口:gossh.NewClientConn
// 不吃 ClientConfig.Timeout(该字段只对 gossh.Dial 的 TCP 段生效,SPEC §5.1),
// 必须手动 SetDeadline。var 仅为测试注入短超时。
var meshExitHandshakeTimeout = 10 * time.Second

var (
	errUnbuildableTarget = errors.New("mesh: target unbuildable from metadata")
	errNoTransportAdmits = errors.New("mesh: no transport admits this peer/class")
)

// pickErr 返回第一个非 nil 的错误(全 nil 返回 nil)。
func pickErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ExitClient 到单一 mesh 出口节点的 SSH 出口客户端(SPEC §5.1 完整定义)。
// 生命周期:构造期解析私钥/钉扎/Transport 切片(一次性),DialContext 逐
// 连接解析类别并遍历 Transport。缓存策略由调用方决定(主路径缓存在
// processor/tcp 的 TCPConnectionHandler,SPEC §5.1「正确落点」)。
type ExitClient struct {
	spec       PeerSpec
	transports []Transport // 有序;LAN 优先,Piko 兜底(SPEC §7.3)
	identity   gossh.Signer
	hostKeyCB  gossh.HostKeyCallback
	timeout    time.Duration
}

// NewExitClient 解析私钥与钉扎并组装 transports。relay=true 且 pikoDial=nil
// 时不放 PikoTransport(等价中继未接线,落到 LAN-only 语义,SPEC §7.3)。
func NewExitClient(spec PeerSpec, privateKeyPEM, passphrase string, pikoDial PikoDialFunc) (*ExitClient, error) {
	signer, err := SignerFromPEM(privateKeyPEM, passphrase)
	if err != nil {
		return nil, fmt.Errorf("mesh: identity key: %w", err)
	}
	transports := []Transport{LANTransport{}}
	if spec.Relay && pikoDial != nil {
		transports = append(transports, PikoTransport{dial: pikoDial})
	}
	user := spec.SSHUser
	if user == "" {
		user = meshSSHUser
	}
	return &ExitClient{
		spec:       spec,
		transports: transports,
		identity:   signer,
		hostKeyCB:  pinnedHostKey(spec.HostKey),
		timeout:    meshExitHandshakeTimeout,
	}, nil
}

// DialContext 建一条 mesh-exit 连接(每条连接一次独立 SSH 握手,无池,
// SPEC §5.1)。类别逐连接解析;载体准入在拨号前判定(Admit 唯一调用点)。
func (c *ExitClient) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	class := classFromContext(ctx, md)

	payload := exitPayload(md)
	if payload == nil {
		return nil, errUnbuildableTarget
	}

	// ConnID 透传给 PikoTransport 的生产拨号(piko 侧逐连接日志,既有缝)。
	if md != nil && md.ConnID != "" {
		ctx = WithConnID(ctx, md.ConnID)
	}

	var raw net.Conn
	var lanErr, relayErr error
	var admitted bool
	for _, t := range c.transports {
		if !t.Admit(class, c.spec) {
			continue // 准入在拨号前,不消耗拨号预算
		}
		admitted = true
		conn, err := t.DialSSHD(ctx, c.spec)
		if err == nil {
			raw = conn
			break
		}
		if t.Name() == "piko" {
			relayErr = err
		} else {
			lanErr = err
		}
	}
	if raw == nil {
		if !admitted {
			return nil, errNoTransportAdmits
		}
		if relayErr != nil && lanErr != nil {
			return nil, fmt.Errorf("mesh: no SSH transport to %s (lan_last_err: %v, relay_last_err: %w)",
				c.spec.DeviceID, lanErr, relayErr)
		}
		return nil, fmt.Errorf("mesh: no SSH transport to %s: %w", c.spec.DeviceID, pickErr(lanErr, relayErr))
	}

	if err := raw.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = raw.Close()
		return nil, err
	}
	cc, chans, reqs, err := gossh.NewClientConn(raw, c.spec.DeviceID, &gossh.ClientConfig{
		User:            c.spec.SSHUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(c.identity)},
		HostKeyCallback: c.hostKeyCB,
	})
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mesh: ssh handshake to %s: %w", c.spec.DeviceID, err)
	}
	_ = raw.SetDeadline(time.Time{}) // 握手完成,清 deadline
	client := gossh.NewClient(cc, chans, reqs)

	ch, chReqs, err := client.OpenChannel(ChannelMeshExit, payload)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("mesh: open %s channel to %s: %w", ChannelMeshExit, c.spec.DeviceID, err)
	}
	go gossh.DiscardRequests(chReqs) // 必须消费,否则连接挂死(SPEC §5.1)

	return newExitConn(ch, client), nil
}

// exitConn 把 gossh.Channel 适配为 net.Conn。自写而非复用 inbound/ssh 的
// channelConn(未导出且其 deadline 为 no-op 先例不可跨包)。Close 连带关
// SSH client(每条 mesh-exit 连接独占一条 SSH 连接)。
type exitConn struct {
	ch         gossh.Channel
	client     *gossh.Client
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newExitConn(ch gossh.Channel, client *gossh.Client) *exitConn {
	c := &exitConn{ch: ch, client: client}
	if client != nil {
		c.remoteAddr = client.RemoteAddr()
		c.localAddr = client.LocalAddr()
	}
	return c
}

func (c *exitConn) Read(b []byte) (int, error)         { return c.ch.Read(b) }
func (c *exitConn) Write(b []byte) (int, error)        { return c.ch.Write(b) }
func (c *exitConn) Close() error {
	err := c.ch.Close()
	if cerr := c.client.Close(); err == nil {
		err = cerr
	}
	return err
}
func (c *exitConn) LocalAddr() net.Addr                { return c.localAddr }
func (c *exitConn) RemoteAddr() net.Addr               { return c.remoteAddr }
func (c *exitConn) SetDeadline(time.Time) error        { return nil } // SSH 通道无 deadline 语义(已知限制,SPEC §16)
func (c *exitConn) SetReadDeadline(time.Time) error    { return nil }
func (c *exitConn) SetWriteDeadline(time.Time) error   { return nil }

// DialMeshAddr 拨一个无 peer 记录的裸地址(离线兜底专用,SPEC §7.2)。
// addr 指向对端 SSH 入口 56432(不是已删除的 mesh 监听口);identity 为
// 本机 mesh 身份(B 的 mesh_authorized_keys 准入);hostKey 为兜底节点
// 钉扎,空 = fail-closed。LAN-only(单载体,不试中继)。
func DialMeshAddr(ctx context.Context, addr, hostKey string, identity gossh.Signer) (net.Conn, error) {
	if identity == nil {
		return nil, errors.New("mesh: fallback dial requires node identity key")
	}
	raw, err := dialLAN(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("mesh: fallback dial %s: %w", addr, err)
	}
	if err := raw.SetDeadline(time.Now().Add(meshExitHandshakeTimeout)); err != nil {
		_ = raw.Close()
		return nil, err
	}
	cc, chans, reqs, hErr := gossh.NewClientConn(raw, addr, &gossh.ClientConfig{
		User:            meshSSHUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(identity)},
		HostKeyCallback: pinnedHostKey(hostKey),
	})
	if hErr != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mesh: fallback ssh handshake to %s: %w", addr, hErr)
	}
	_ = raw.SetDeadline(time.Time{})
	client := gossh.NewClient(cc, chans, reqs)
	ch, chReqs, oErr := client.OpenChannel(ChannelMeshExit, payload)
	if oErr != nil {
		_ = client.Close()
		return nil, fmt.Errorf("mesh: fallback open %s channel to %s: %w", ChannelMeshExit, addr, oErr)
	}
	go gossh.DiscardRequests(chReqs)
	return newExitConn(ch, client), nil
}
```

**`DialMeshAddr` 收 payload 而非常量**:兜底拨出的目标由调用方 metadata 决定(SPEC §7.2 的 `dialMeshFallbackNode(ctx, md, addr)` 收 `md`),签名定为 `DialMeshAddr(ctx, addr, hostKey, identity, payload []byte)`;`payload == nil` 按不可构造拒绝(返回 `errUnbuildableTarget`),在 OpenChannel 之前判。

- [ ] **Step 4: 测试通过 → Commit**

```bash
git add outbound/proxy/mesh/sshexit.go outbound/proxy/mesh/sshexit_test.go
git commit -m "新增:mesh ExitClient——SSH mesh-exit 出口客户端(LAN优先/Piko兜底/钉扎/握手deadline)"
```

---

# Phase D:入站 SSH(mesh-exit 形态)

### Task D1: `MeshKey` 类型 + `ForwardGate` 收窄 + fail-closed(编译原子批)

> 本 Task 跨 `inbound/ssh` 与 `app/http/services` 两个包,**必须在一个 commit 内完成**(改 `MeshKeys` 元素类型与 `ForwardGate` 签名会同时打断两包编译)。

**Files:**
- Modify: `inbound/ssh/config.go`、`inbound/ssh/server.go`、`inbound/ssh/reconcile.go`
- Modify: `app/http/services/customer_config_service.go`
- Modify: `inbound/ssh/reconcile_test.go`、`inbound/ssh/server_test.go`、`app/http/services/ssh_ingress_reconcile_test.go`(SPEC §10.1/§10.2 连带表)

- [ ] **Step 1: 改 `inbound/ssh/config.go`**

```go
// MeshKey 是形态②(mesh)准入凭证:公钥 + authorized_keys 尾注释承载的
// device_id(SPEC §10.2;日志/审计的人类可读身份,空则回落指纹前 16 字符)。
type MeshKey struct {
	Key      gossh.PublicKey
	DeviceID string
}

// extMeshDeviceID 是认证 Permissions 中携带 mesh 对端身份的扩展键(进程内
// 数据,不上线——Extensions 由服务端产生与消费,SPEC §10.2)。
const extMeshDeviceID = "mesh_device_id"

type RunConfig struct {
	// … Port/AuthorizedKeys 不变 …
	// MeshKeys 是形态②凭证(mesh_authorized_keys)。**注意字段名沿用
	// MeshKeys,元素类型从 []gossh.PublicKey 变为 []MeshKey**(SPEC §10.2)。
	MeshKeys []MeshKey
	// LocalNodeID 返回本机节点标识(防环 AppendHop 用)。由装配层注入
	// (AgentService.currentDeviceID);测试注入闭包。nil=形态②整体禁用
	// (防御:装配未完成时不放行 mesh 帧)。
	LocalNodeID func() string
	// ForwardGate 返回转发总开关(customer.mesh.forward_enable 活视图)。
	// **nil = fail-closed(SPEC §10.1 决策 #14:装配层恒注入活闭包,nil
	// 语义从「全放行」翻转为「拒绝」)**。allowed []string 名单职能已删除
	// ——ACL 由 MeshKeys 公钥列表承担,两套 ACL 并存会制造失配。
	ForwardGate func() bool
}
```

- [ ] **Step 2: 改 `server.go` 的 `publicKeyCallback`/`keyInList`**

```go
// meshKeyFor 按公钥身份查 mesh 凭证,命中返回完整 MeshKey(DeviceID 随之
// 进入通道层,SPEC §10.2)。
func meshKeyFor(key gossh.PublicKey, list []MeshKey) (MeshKey, bool) {
	for _, mk := range list {
		if mk.Key != nil && bytes.Equal(key.Marshal(), mk.Key.Marshal()) {
			return mk, true
		}
	}
	return MeshKey{}, false
}
```

`publicKeyCallback` 的 mesh 分支:

```go
case func() bool { _, ok := meshKeyFor(key, s.rc.MeshKeys); return ok }():
	// ↑ switch 语义不允许这么写——用 if/else if 重构:
```

实际写法(把 switch 改 if/else if):

```go
func (s *Server) publicKeyCallback(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
	ip := remoteIP(meta.RemoteAddr())
	if mk, ok := meshKeyFor(key, s.rc.MeshKeys); ok {
		s.limiter.Reset(ip)
		return &gossh.Permissions{Extensions: map[string]string{
			extIngressForm:  FormMesh,
			extMeshDeviceID: meshDeviceIDLabel(mk),
		}}, nil
	}
	if keyInList(key, s.rc.AuthorizedKeys) {
		s.limiter.Reset(ip)
		return &gossh.Permissions{Extensions: map[string]string{extIngressForm: FormBare}}, nil
	}
	s.limiter.Fail(ip)
	logger.Debug(fmt.Sprintf("SSH ingress rejected unknown key %s for %q from %s",
		gossh.FingerprintSHA256(key), meta.User(), ip))
	return nil, fmt.Errorf("unknown public key for %q", meta.User())
}

// meshDeviceIDLabel 尾注释为空时回落指纹前 16 字符(SPEC §10.2)。
func meshDeviceIDLabel(mk MeshKey) string {
	if id := strings.TrimSpace(mk.DeviceID); id != "" {
		return id
	}
	fp := gossh.FingerprintSHA256(mk.Key)
	if len(fp) > 16 {
		return fp[:16]
	}
	return fp
}
```

(`keyInList` 保留原样,只服务裸钥列表。)

- [ ] **Step 3: 改 `reconcile.go`**

- `runIdentity.meshKeys` 类型改 `[]MeshKey`;
- `marshalKeyIdentity` 只服务 authorized(签名不变);新增 SPEC §10.2 的:

```go
// marshalMeshKeyIdentity 把 mesh 凭证归一为排序后的身份集合:Marshal 字节
// + "\x00" + DeviceID(注释改了但不改钥字节也要重启,SPEC §10.2)。
func marshalMeshKeyIdentity(keys []MeshKey) [][]byte {
	out := make([][]byte, 0, len(keys))
	for _, mk := range keys {
		b := mk.Key.Marshal()
		line := make([]byte, 0, len(b)+1+len(mk.DeviceID))
		line = append(line, b...)
		line = append(line, 0x00)
		line = append(line, mk.DeviceID...)
		out = append(out, line)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out
}
```

- `newRunIdentity` 的 `meshKeys: marshalMeshKeyIdentity(rc.MeshKeys)`。

- [ ] **Step 4: 改 `customer_config_service.go`**(SPEC §10.1 第 2 条 + §10.2 拆两路)

1. `buildSSHIngressRunConfig` 签名扩参:`func buildSSHIngressRunConfig(effective *config.SSHIngressConfig, mesh *config.MeshConfig) *sshin.RunConfig`;调用点(`ReconcileSSHIngressFromConfig` 内)传 `cfg.Customer.Mesh`。
2. 闭包(SPEC §10.1 代码逐字,捕获 `mesh` 参数不捕获全局):

```go
var gate func() bool
if mesh != nil {
	gate = func() bool { return mesh.EffectiveForwardEnabled() }
}
```

3. `MeshKeys: parseSSHIngressMeshKeys(effective.MeshAuthorizedKeys)`(新函数,保留 comment):

```go
// parseSSHIngressMeshKeys 逐行解析 mesh_authorized_keys,保留尾注释为
// device_id(SPEC §10.2)。坏行跳过+Warn。
func parseSSHIngressMeshKeys(lines []string) []sshin.MeshKey {
	keys := make([]sshin.MeshKey, 0, len(lines))
	for _, line := range lines {
		pub, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			logger.Warn(fmt.Sprintf("ssh_ingress.mesh_authorized_keys key skipped (unparseable): %v", err))
			continue
		}
		keys = append(keys, sshin.MeshKey{Key: pub, DeviceID: strings.TrimSpace(comment)})
	}
	return keys
}
```

4. 两处注释整段重写(SPEC §10.1 第 4 条):`ReconcileSSHIngressFromConfig` 的「ForwardGate 留 nil=全放行…」段、`buildSSHIngressRunConfig` 内 `ForwardGate: nil` 行改为 `ForwardGate: gate,` + 新注释「nil=fail-closed;装配层恒注入活闭包(SPEC §10.1 决策 #14)」。另 `grep -rn "全放行" inbound/ssh/ app/http/services/` 扫尾。

- [ ] **Step 5: 写 `MeshKey` 解析新测试**(SPEC §13.4 新增项,评审补——注释→DeviceID 是新身份叙事的承重墙)

在 `inbound/ssh/server_test.go`(或新建 `inbound/ssh/meshkey_test.go`)追加:

```go
// SPEC §13.4:MeshKey 解析四形态 + 指纹回落
func TestParseSSHIngressMeshKeysCommentForms(t *testing.T) {
	// services 包的 parseSSHIngressMeshKeys 在 app/http/services——本用例放
	// app/http/services/ssh_ingress_reconcile_test.go;inbound/ssh 侧测
	// meshKeyFor/meshDeviceIDLabel:
	// 1) 带尾注释 "ssh-ed25519 AAAA... dev-b" → DeviceID="dev-b"
	// 2) 不带注释 → DeviceID=""(运行时由 meshDeviceIDLabel 回落指纹)
	// 3) 尾注释含空格 "dev-b extra" → comment 原样,Gossh 返回整段;
	//    parseSSHIngressMeshKeys 的 TrimSpace 只去首尾——注释含内部空格时
	//    DeviceID 保留原样(实现按此写,测试钉住)
	// 4) 跨列表同钥:同一公钥同时出现在 authorized_keys 与 mesh_authorized_keys
	//    → 配置层 Validate 已拦(SPEC §10.3 既有),此处钉 meshKeyFor 命中 mesh 优先
}
func TestMeshDeviceIDLabelFallback(t *testing.T) {
	// DeviceID="" → 返回指纹前 16 字符;DeviceID 非空 → 原样(TrimSpace 后)
}
```

- [ ] **Step 6: 同步三处测试文件(SPEC §10.1/§10.2 连带表)**

- `inbound/ssh/reconcile_test.go:181,189`:`MeshKeys: []gossh.PublicKey{meshPub}` → `[]MeshKey{{Key: meshPub, DeviceID: "dev-b"}}`。
- `inbound/ssh/server_test.go:365,388,432`:同上;`:432` 双钥用例顺手断言两个不同 DeviceID 各自透传 `extMeshDeviceID`。
- `app/http/services/ssh_ingress_reconcile_test.go`:构造点全部 `[]gossh.PublicKey` → `[]sshin.MeshKey`;**删除 `:243-245` 的「ForwardGate 必须为 nil」断言**,改为断言 `ForwardGate != nil` 且返回期望布尔。
- `inbound/ssh/mesh_inject_test.go`:本文件随 Task D2 整体重写,本 Task 只需让**包编译通过**——若 `mesh_inject_test.go` 因 `ForwardGate func() (bool, []string)` 编译红,临时把其中 gate 用例的签名改为 `func() bool`(语义重写在 D2)。

- [ ] **Step 7: `go build ./... && go test ./inbound/ssh/... ./app/http/services/...` 全绿 → Commit**

```bash
git add inbound/ssh/ app/http/services/
git commit -m "新增:mesh 凭证身份化——MeshKey(公钥+device_id 尾注释)+ ForwardGate 收窄为总开关且 nil=fail-closed"
```

### Task D2: `serveChannels` 矩阵 + `mesh-exit` 注入 + DNS handler + `WithRemoteLabel`(旧帧路径删除)

**Files:**
- Modify: `inbound/ssh/server.go`(`serveChannels`)
- Modify: `inbound/ssh/channel.go`(新增 `WithRemoteLabel`)
- Rewrite: `inbound/ssh/mesh_inject.go`
- Create: `inbound/ssh/mesh_dns.go`、`inbound/ssh/mesh_dns_test.go`、`inbound/ssh/mesh_inject_test.go`(重写)
- Delete: `inbound/ssh/meshframe.go`(+ 其测试)

- [ ] **Step 1: 写失败测试(mesh_inject_test.go 重写版核心用例)**

1. **TestMeshExitMatrix**(form×channel 穷举,SPEC §15.1):bare×`mesh-exit` → Reject;mesh×`direct-tcpip` → Reject;未知 form×任意 → Reject(UnknownChannelType)。
2. **TestMeshExitExtraDataCap**:`len(extra) > 512` → Reject。
3. **TestMeshExitBadPayload**:非 JSON / `t` 未知 / 缺字段 → Reject。
4. **TestMeshExitForwardGateFailClosed**:gate=nil → Reject;gate 返回 false → Reject;true → 注入。
5. **TestMeshExitInjectsPipeline**(端到端小环):真 sshd + 真服务端,`ExitClient` 开 `mesh-exit` → B 侧 `GetHandler` 收到 Metadata `Route=RouteMesh`、`MeshInjected=true`(handler 用测试替身——`processor/tcp` 已有 handler 替身先例,`grep -rn "TCPConnHandler\|statsManager" processor/tcp/*_test.go` 定位;若单测注入难,落 Task H1 的集成测试,本用例移过去)。
6. **TestMeshDNSExitRoundTrip**:`{"t":"dns"}` → B 侧读帧→fake resolver→回帧→关。
7. **TestWithRemoteLabelRateLimitKey**:`remoteIP(WithRemoteLabel(conn,"piko-dev-b.mesh").RemoteAddr())` 返回 `"piko-dev-b.mesh"` 原样(**无冒号约定**,SPEC §8.2);`netAddrToIPPort` 对该 addr 返回零值(SrcIP 归零钉住)。
8. **TestMeshKeyDeviceIDPassthrough**:双 MeshKey 不同 DeviceID,各自连接的 `extMeshDeviceID` 正确。

- [ ] **Step 2: 确认失败** → **Step 3: 实现 `server.go serveChannels`**

```go
// serveChannels 是通道安全收敛矩阵(SPEC §4.2):公钥身份决定 form,form
// 决定可用 channel;所有拒绝在 Accept 前发出(Reject reason 会原样回给
// A 侧 OpenChannel)。bare→direct-tcpip 裸流注入;mesh→mesh-exit ExtraData
// 注入(§4);未知 form 全拒。
func (s *Server) serveChannels(ctx context.Context, chans <-chan gossh.NewChannel, perms *gossh.Permissions, remote net.Addr) {
	for newCh := range chans {
		form, meshDeviceID := "", ""
		if perms != nil {
			form = perms.Extensions[extIngressForm]
			meshDeviceID = perms.Extensions[extMeshDeviceID]
		}
		s.observeForm(form)

		var allowed string
		switch form {
		case FormBare:
			allowed = channelDirectTCPIP
		case FormMesh:
			allowed = channelMeshExit
		default:
			_ = newCh.Reject(gossh.UnknownChannelType, "unknown credential form")
			continue
		}
		if newCh.ChannelType() != allowed {
			_ = newCh.Reject(gossh.Prohibited, "channel not allowed for this credential form")
			continue
		}

		switch form {
		case FormBare:
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				logger.Debug(fmt.Sprintf("SSH ingress direct-tcpip accept failed: %v", err))
				continue
			}
			go gossh.DiscardRequests(chReqs)
			injectDirectTCPIP(ctx, ch, remote, newCh.ExtraData(), s.rc)
		case FormMesh:
			handleMeshExitChannel(ctx, newCh, newCh.ExtraData(), s.rc, meshDeviceID, remote)
		}
	}
}
```

(注意:`injectDirectTCPIP` 现签名含 `form` 参数——改为不再收 form(form 分流上移到本函数);同步改其调用与实现签名。`injectBareStream` 的 `netAddrToIPPort` 逻辑不动。)

- [ ] **Step 4: 重写 `mesh_inject.go`**

```go
// mesh_inject.go — 形态②(mesh-exit channel)注入路径:payload 走 SSH
// channel ExtraData(SPEC §4,无长度歧义、无首段预读坑——旧帧协议的
// meshChannelConn/preread 回放机制随帧协议删除),身份由公钥承载
// (Permissions.Extensions),注入本机入站管线(Route=RouteMesh 溯源 +
// MeshInjected=true 防环,SPEC §6.2)。
package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"

	gossh "golang.org/x/crypto/ssh"
)

// channelMeshExit 是 mesh 形态唯一放行的 channel 名(与出口侧
// meshproxy.ChannelMeshExit 同值在线协议,两处字面量由测试互锁)。
const channelMeshExit = "mesh-exit"

// meshExitMaxExtra 是 mesh-exit ExtraData 上限(与出口侧同值,测试互锁)。
const meshExitMaxExtra = 512

// meshExitTarget 是 mesh-exit ExtraData 的解码形态(t=tcp 带 h/p;t=dns 无
// 其它字段,SPEC §4.1)。
type meshExitTarget struct {
	T string `json:"t"`
	H string `json:"h"`
	P uint16 `json:"p"`
}

// handleMeshExitChannel 是 mesh form 的 channel 分派:ExtraData cap →
// payload 解析 → ForwardGate fail-closed 门禁(SPEC §10.1 决策 #14)全部
// 在 Accept 前完成(Reject reason 回给 A 侧 OpenChannel);Accept 后按 t
// 分流(dns→代解析不进管线;tcp→注入管线)。
func handleMeshExitChannel(ctx context.Context, newCh gossh.NewChannel, extra []byte, rc RunConfig, meshDeviceID string, connRemote net.Addr) {
	reject := func(msg string) {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit rejected (%s) from %s", msg, meshDeviceID))
		_ = newCh.Reject(gossh.Prohibited, msg)
	}
	if len(extra) > meshExitMaxExtra {
		reject("payload too large")
		return
	}
	var tgt meshExitTarget
	if err := json.Unmarshal(extra, &tgt); err != nil {
		reject("bad payload")
		return
	}
	switch tgt.T {
	case "tcp":
		if tgt.H == "" || tgt.P == 0 {
			reject("bad payload: h/p required for tcp target")
			return
		}
	case "dns":
		// 无其它字段(SPEC §4.1:域名只在帧内,B 侧自己解问)
	default:
		reject("unknown payload type")
		return
	}
	// ForwardGate:fail-closed(SPEC §10.1)——nil=装配层未接线=拒绝,
	// 而非旧「全放行」。
	if rc.ForwardGate == nil || !rc.ForwardGate() {
		reject("forward disabled (customer.mesh.forward_enable)")
		return
	}

	ch, chReqs, err := newCh.Accept()
	if err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit accept failed: %v", err))
		return
	}
	go gossh.DiscardRequests(chReqs)

	if tgt.T == "dns" {
		go serveMeshDNSExit(ch)
		return
	}
	go injectMeshExitStream(ctx, ch, connRemote, tgt, meshDeviceID)
}

// injectMeshExitStream 把 mesh-exit TCP 流注入本机入站管线(SPEC §4.1 注入
// 落点:直接复用 channelConn 字段字面量,无适配器;源地址取连接层)。
func injectMeshExitStream(ctx context.Context, ch gossh.Channel, connRemote net.Addr, tgt meshExitTarget, meshDeviceID string) {
	defer ch.Close()

	md := &M.Metadata{
		Network:      M.TCP,
		ConnID:       fmt.Sprintf("sshing-%d", atomic.AddUint64(&sshIngressConnCounter, 1)),
		HostName:     tgt.H,                    // ExtraData 里的 h(已拆端口)
		DstPort:      tgt.P,
		Route:        tcphandler.RouteMesh,     // 决策前统计口径(SPEC §4.1 附注)
		MeshInjected: true,                     // §6.2 环路守卫,不被决策覆写
	}
	md.SrcIP, md.SrcPort = netAddrToIPPort(connRemote)

	conn := &channelConn{
		Channel: ch,
		local:   &net.TCPAddr{IP: net.ParseIP(tgt.H), Port: int(tgt.P)}, // 域名→IP nil,仅日志展示
		remote:  connRemote,
	}
	logger.Debug(fmt.Sprintf("SSH ingress mesh-exit injected conn_id=%s from=%s target=%s:%d",
		md.ConnID, meshDeviceID, tgt.H, tgt.P))
	if err := tcphandler.GetHandler().Handle(ctx, conn, md); err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit conn_id=%s handle: %v", md.ConnID, err))
	}
}
```

删除项:旧 `injectMeshFrame`/`writeMeshRejectLine`/`forwarderAllowed`/`meshChannelConn`/`newMeshChannelConn`/`IngressFrameMeta`/`ingressFrameMetaKey`/`ContextKeyIngressFrameMeta`/全部 `meshReject*` 常量/`meshFrameDefaultPort`。

- [ ] **Step 5: 新建 `mesh_dns.go`**——从 `processor/tcp/mesh_inbound.go` **整段搬运**以下符号(SPEC §9,签名全部不含 `meshproto.Header`,保持未导出同包本地版):`LocalDNSResolver`、`meshDNSResolveTimeout`、`meshDNSAnswerTTL`、`resolveDNSQuery`、`dnsRcodeForResolveError`、`resolveDNSQuestion`、`dnsRRHeader`、`dnsPTRNameToAddr`、`splitSRVName`、`servfailDNSResponse`、`readDNSLengthFrame`、`writeDNSLengthFrame`,加一个包级 resolver 缝:

```go
// meshDNSResolver 是 mesh-exit dns 代解析所用解析器(net.Resolver 系统解析
// 器,SPEC §9 取舍注释随搬);var 仅为测试注入 fake。
var meshDNSResolver LocalDNSResolver = &net.Resolver{}

// serveMeshDNSExit 处理 {"t":"dns"} channel:读 [2B 长度][DNS 报文] 帧 →
// 解析 → 写 [2B 长度][响应] → 关(SPEC §9;不进入站管线,无 DNS 二跳)。
func serveMeshDNSExit(ch gossh.Channel) {
	defer ch.Close()
	resolver := meshDNSResolver
	if resolver == nil {
		resolver = &net.Resolver{}
	}
	_ = ch.SetDeadline(time.Now().Add(meshDNSResolveTimeout))
	query, err := readDNSLengthFrame(ch)
	if err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit dns: read frame: %v", err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), meshDNSResolveTimeout)
	defer cancel()
	if _, werr := writeDNSLengthFrame(ch, resolveDNSQuery(ctx, resolver, query)); werr != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit dns: write frame: %v", werr))
	}
}
```

- [ ] **Step 6: `channel.go` 新增 `WithRemoteLabel`**(SPEC §8.2;**label 禁含冒号**,由调用方与测试共同钉住)

```go
// WithRemoteLabel 给连接包一层 RemoteAddr 标签(piko 中继载体限速串桶修法,
// SPEC §8.2):yamux 流的 RemoteAddr 探测到底层 websocket 对端(= piko 服
// 务器 IP),所有中继 peer 会共用限速键。包装后 remoteIP(SplitHostPort
// 失败 → 原样返回)按 label 隔离。**label 绝不能含冒号**——"piko:dev-b"
// 会被 SplitHostPort 成功拆分(host="piko"),修法完全失效。
// 已知副作用(测试钉住):netAddrToIPPort 对 label 地址解析失败 → SrcIP/
// SrcPort 归零,peer 真实来源在统计/审计面不可知(SPEC §8.2/§16)。
type remoteLabelAddr struct{ label string }

func (a *remoteLabelAddr) Network() string { return "tcp" }
func (a *remoteLabelAddr) String() string  { return a.label }

type remoteLabelConn struct {
	net.Conn
	addr *remoteLabelAddr
}

func WithRemoteLabel(c net.Conn, label string) net.Conn {
	if c == nil {
		return nil
	}
	return &remoteLabelConn{Conn: c, addr: &remoteLabelAddr{label: label}}
}

func (c *remoteLabelConn) RemoteAddr() net.Addr { return c.addr }
```

- [ ] **Step 7: 删除 `meshframe.go` 及其测试**;`go build ./...`(此时 `processor/tcp/mesh_inbound.go` 的 DNS 函数仍在新旧两处并存——`inbound/ssh` 与 `processor/tcp` 是不同包,不冲突;`inbound/http/dispatch.go`、`app/tunnel/mesh_dispatch.go` 仍走旧路径,Task F1 才删)。

- [ ] **Step 8: `go build ./... && go test ./inbound/ssh/...` 全绿 → Commit**

```bash
git add inbound/ssh/
git commit -m "新增:mesh-exit channel 入站形态——form×channel 矩阵+ExtraData注入+DNS代转发+远端标签限速隔离"
```

---

# Phase E:切换(Task E1,编译原子大批)

### Task E1: processor/tcp 全量切换 + mesh.go 瘦身 + app/tunnel/egress/dns 消费方同步

> 单 commit:改 `getMeshProxyForExecution` 返回类型会同时打断 `dialViaMesh`/`egress.go`/`dns_forward.go`/`udp.go`/`tunnel.go`/`mesh_assembly.go`/`mesh_dial.go`,必须原子。

**Files:**
- Modify: `processor/tcp/handler.go`(`TCPConnectionHandler` 结构体字段 + mesh 区段重写)
- Modify: `outbound/proxy/mesh/mesh.go`(删除 `Peer`/`PeerTarget`/`NewPeer`/`Peer.DialContext`/`writeHeader`/`targetFromMetadata`/`InheritedHeader` 缝/`dialLANFn`;保留 `PikoDialFunc`/`meshRelayEndpointSuffix`/`WithClass`/`classFromContext`/`InferClass`/`WithConnID`/`ConnIDFromContext`/`looksLikeAITraffic`/`aiProxyPort`;包注释改写)
- Modify: `processor/egress/egress.go`(`peerSource` 缝改 `func() meshproxy.Dialer`;`PeerSourceForTest` 返回类型同步)
- Modify: `inbound/tun/tunnel/tunnel.go`(`meshDNSForwardFn func() meshproxy.Dialer`)、`dns_forward.go`(`SetMeshDNSForwardPeerSource` 签名 + `dnsForwardPacketConn.peer` 类型 + `exchange` 不变)、`udp.go`(`t.meshDNSForwardPeer()` 非 nil 判定不变)
- Modify: `app/tunnel/mesh_assembly.go`(`egressPeer` 改造)与 `app/tunnel/mesh_dial.go`(import 注释微调)
- Modify: `app/tunnel/manager.go`(评审补:E1 即完成缝改名/删除——`SetMeshPikoDialSource` → `SetMeshTransportDialSource`(`:335`)与 `SetMeshSelfIDSource` 调用删除(`:317`);F1 只负责 accept 循环与 relay 删除,两者不重叠)
- Modify: `outbound/proxy/mesh/payload.go`(评审补:`ExitPayloadForMetadata(md)` / `ErrUnbuildableTarget()` 两个导出缝落在本文件,随本 Task 增加)
- Modify: `app/tunnel/manager_test.go`(评审补:`:22-23` 调用 `SetMeshPikoDialSource`/`SetMeshSelfIDSource`,随改名/删除同步)、`app/tunnel/mesh_assembly_test.go`(`:173` 断言随决策 #4 同步)
- Modify: `outbound/proxy/mesh/mesh_test.go`(旧 Peer 用例删除/重写为 ExitClient 语义)、`app/tunnel/mesh_dial_test.go`(`:28-29` 缝引用同步)、`processor/tcp/handler_mesh_test.go`(重写为缓存键/守卫测试)

- [ ] **Step 1: handler.go 结构体字段 + 缓存(SPEC §5.1「正确落点」)**

```go
type TCPConnectionHandler struct {
	protocolDetector ProtocolDetector
	tlsHandler       TLSHandler
	relayManager     RelayManager
	statsManager     *statistic.Manager
	// meshExits 是 mesh ExitClient 缓存(SPEC §5.1/§10.4):挂在实例上而非
	// 包级变量——包级变量跨 ResetHandler(测试)存活会串味。键与失效见
	// meshproxy.PeerSpec.CacheKey;无 TTL,键不匹配即重建(唯一重建路径)。
	meshExitsMu sync.RWMutex
	meshExits   map[string]*meshproxy.ExitClient
}
```

`NewTCPConnectionHandler` 初始化 `meshExits: make(map[string]*meshproxy.ExitClient)`。

- [ ] **Step 2: mesh 区段重写**(以下为完整新实现,替换 `getMeshProxyForExecution`/`dialMeshFallbackNode`,并改造 `dialViaMesh`)

```go
// getMeshExitForExecution 从 customer.mesh.peers 装配并缓存 toNode:<deviceID>
// 的 ExitClient(SPEC §5.1/§7.3)。peer 不存在/未配置/缺身份 → 错误:编译期
// meshForwardAvailable 已把关(含 SSH 依赖与钉扎/身份检查),此处兜住决策→
// 拨出窗口内的配置漂移,由链头失败策略收口。
func (h *TCPConnectionHandler) getMeshExitForExecution(deviceID string) (*meshproxy.ExitClient, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil || !cfg.MeshEffectivelyEnabled() {
		return nil, fmt.Errorf("mesh: customer.mesh not configured or degraded (target %s)", deviceID)
	}
	meshCfg := cfg.Customer.Mesh
	if strings.TrimSpace(meshCfg.PrivateKey) == "" {
		return nil, fmt.Errorf("mesh: customer.mesh.private_key not configured")
	}
	for i := range meshCfg.Peers {
		entry := &meshCfg.Peers[i]
		if strings.TrimSpace(entry.DeviceID) != deviceID {
			continue
		}
		spec := meshproxy.PeerSpec{
			DeviceID: strings.TrimSpace(entry.DeviceID),
			LANAddrs: entry.LANAddrs,
			HostKey:  strings.TrimSpace(entry.HostKey),
			SSHUser:  "mesh",
			Relay:    entry.Relay == nil || *entry.Relay,
		}
		var pikoDial meshproxy.PikoDialFunc
		if spec.Relay {
			if dial := meshTransportDialFromSource(); dial != nil {
				pikoDial = dial
			}
		}
		key := spec.CacheKey(meshCfg.PrivateKey, meshCfg.KeyPassphrase)
		h.meshExitsMu.RLock()
		client, ok := h.meshExits[key]
		h.meshExitsMu.RUnlock()
		if ok {
			return client, nil
		}
		client, err := meshproxy.NewExitClient(spec, meshCfg.PrivateKey, meshCfg.KeyPassphrase, pikoDial)
		if err != nil {
			return nil, err
		}
		h.meshExitsMu.Lock()
		h.meshExits[key] = client // 键不匹配即重建(唯一失效路径,SPEC §10.4)
		h.meshExitsMu.Unlock()
		return client, nil
	}
	return nil, fmt.Errorf("mesh: peer %q not found in customer.mesh.peers", deviceID)
}
```

`dialViaMesh`:首行加守卫(已在 Task B1 落)、`peer, peerErr := h.getMeshProxyForExecution(deviceID)` → `client, peerErr := h.getMeshExitForExecution(deviceID)`、**删除** `InheritedHeaderFromContext` 中间跳分支(单跳模型,无中间跳;注入连接已被守卫拦截)、其余兜底/失败策略结构不变。

`dialMeshFallbackNode`(SPEC §7.2 + 决策 #3 守卫):

```go
// dialMeshFallbackNode 拨离线兜底节点:兜底地址现指对端 SSH 入口 56432
// (SPEC §7.2——旧值指向已删除的 mesh 监听口,发布说明必写),经
// DialMeshAddr 走 SSH(LAN-only 单载体)。本机身份 = customer.mesh.
// private_key(兜底路径拨号频率低,构造期解析私钥可接受,不缓存)。
func (h *TCPConnectionHandler) dialMeshFallbackNode(ctx context.Context, metadata *M.Metadata, addr string) (net.Conn, error) {
	if metadata.MeshInjected {
		return nil, errMeshInjectedLoop
	}
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return nil, fmt.Errorf("mesh: customer.mesh not configured (fallback %s)", addr)
	}
	meshCfg := cfg.Customer.Mesh
	signer, err := meshproxy.SignerFromPEM(meshCfg.PrivateKey, meshCfg.KeyPassphrase)
	if err != nil {
		return nil, fmt.Errorf("mesh: fallback identity key: %w", err)
	}
	payload := meshproxy.ExitPayloadForMetadata(metadata)
	if payload == nil {
		return nil, meshproxy.ErrUnbuildableTarget()
	}
	return meshproxy.DialMeshAddr(ctx, addr, config.MeshFallbackHostKey(cfg), signer, payload)
}
```

(配套:meshproxy 把 `errUnbuildableTarget`/`exitPayload` 导出为 `ErrUnbuildableTarget()`/`ExitPayloadForMetadata(md)`,供跨包复用;`errNoTransportAdmits` 保持包内。)

注入缝改名(SPEC §7.3):`SetMeshPikoDialSource` → `SetMeshTransportDialSource`、`meshPikoDialFromSource` → `meshTransportDialFromSource`(原子.Value 结构不变);`meshSelfIDSource` 缝:`PeerTarget.SelfID` 已死,**保留该缝但标注「一期无消费方,为日志/观测保留」**或直接删除——选**删除**(`SetMeshSelfIDSource`/`meshSelfIDFromSource`/`manager.go:317` 调用点一并删),YAGNI。

- [ ] **Step 3: egress.go 切换**

```go
var peerSource atomic.Value // func() meshproxy.Dialer

func SetPeerSource(fn func() meshproxy.Dialer) { /* 同构 */ }
func peerFromSource() meshproxy.Dialer { /* 同构 */ }
func PeerSourceForTest() meshproxy.Dialer { return peerFromSource() }
```

`platformDialContext` 内 `peer := peerFromSource()` 后的 `peer.DialContext(...)` 调用不变(接口同签名)。

- [ ] **Step 4: tunnel/dns/udp 切换**——`tunnel.go:62` `meshDNSForwardFn func() meshproxy.Dialer`;`dns_forward.go` `SetMeshDNSForwardPeerSource(fn func() meshproxy.Dialer)`、`meshDNSForwardPeer() meshproxy.Dialer`、`dnsForwardPacketConn` 的 `peer` 字段类型;`udp.go` 调用点签名无关、零改动(编译自然过)。

- [ ] **Step 5: mesh_assembly.go `egressPeer`**(SPEC §10.3:本函数换 `MeshEffectivelyEnabled`;beacon 的 `meshLANDesired` 保持 `EffectiveEnabled` 不动)

```go
// egressPeer 从当前全局配置装配 egress 兜底 peer(Peers[0]):SSH 依赖降级
// 由 MeshEffectivelyEnabled 承担(SPEC §10.3);私钥/钉扎缺失 → nil(该
// peer 能力不成立)。ExitClient 每次调用重建——离线兜底触发频率低,构造期
// 解析私钥开销可接受(SPEC 计划决策 #1)。
func (m *Manager) egressPeer() meshproxy.Dialer {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return nil
	}
	mesh := cfg.Customer.Mesh
	if !cfg.MeshEffectivelyEnabled() || len(mesh.Peers) == 0 {
		return nil
	}
	if strings.TrimSpace(mesh.PrivateKey) == "" {
		return nil
	}
	first := mesh.Peers[0]
	deviceID := strings.TrimSpace(first.DeviceID)
	if deviceID == "" {
		return nil
	}
	lanAddrs := make([]string, 0, len(first.LANAddrs))
	for _, addr := range first.LANAddrs {
		if addr = strings.TrimSpace(addr); addr != "" {
			lanAddrs = append(lanAddrs, addr)
		}
	}
	spec := meshproxy.PeerSpec{
		DeviceID: deviceID,
		LANAddrs: lanAddrs,
		HostKey:  strings.TrimSpace(first.HostKey),
		SSHUser:  "mesh",
		Relay:    first.Relay == nil || *first.Relay,
	}
	client, err := meshproxy.NewExitClient(spec, mesh.PrivateKey, mesh.KeyPassphrase, m.dialMeshEndpoint)
	if err != nil {
		log.Printf("[MESH-LAN] egress fallback peer %s unavailable: %v", deviceID, err)
		return nil
	}
	return client
}
```

`wireMeshAssemblySeams` 的 `egress.SetPeerSource(m.egressPeer)` 签名自动匹配。

- [ ] **Step 6: mesh.go 瘦身**——删除列(§决策)所列符号与 `proto`/`netip` 不再需要的 import;包注释改写为「mesh 出口侧:SSH 代理链出口的类别判定与 context 助手;ExitClient 见 sshexit.go」。**在本步定义 `Dialer` 接口**(决策 #1 的承载形态,五处签名消费它):

```go
// Dialer 是 mesh 出口拨号的最小接口(ExitClient 天然实现;egress 兜底与
// TUN DNS 代转发经此消费,计划决策 #1)。
type Dialer interface {
	DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error)
}
```

(接口与 E1 Step 5/6 各消费方同一 commit——五处签名同批切换,不留中间态。)

- [ ] **Step 7: `go build ./... && go test ./...`(全仓)**;修既有测试:`mesh_assembly_test.go:173` 的 `proto.Mesh` 断言改断言 `egressPeer` 返回 `*meshproxy.ExitClient`(或 nil)——这是计划决策 #4(SPEC 要求「确认仍成立」,核实结论=随 `Peer.Base` 退役);`mesh_fallback_gate_test.go` 若 egress 侧构造仍引用旧类型则同步。

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "重构:mesh 出口切换——ExitClient 缓存于 handler+PeerSpec 装配+消费方全量 Dialer 化(旧 Peer 帧路径退役)"
```

---

# Phase F:B 侧旧路径删除

### Task F1: relay/mesh_dispatch/sniff/dispatch/mesh_inbound/meshproto + manager accept 循环

**Files:**
- Delete: `app/mesh/relay/`(整目录)、`app/tunnel/mesh_dispatch.go`、**`test/mesh_integration_test.go`(评审补:整篇按帧协议写,引用 `relay.New`/`meshproto.Parse`/`CarrierLAN`/`CarrierRelay`/`MeshInboundDeps`,本 Task 删这些符号后它必编译红——先删,Task H1 按本计划场景清单重建,旧帧协议实现细节不保留)**
- Modify: `app/http/routes/routes.go`(核对项,SPEC §13.1 行:确认 `/api/mesh/status`、`/api/mesh/lan_peers` 两条路由注册仍指向 mesh_handler 的既有方法——`effective` 字段落在 F1 Step 5 的 handler 改动里,路由注册本身预计零改动;核对结论记入提交说明)
- Modify: `app/tunnel/manager.go`(`runConfig.meshRelay` 字段删、`validateConfig` 的 `relay.New`/`SetMeshSelfIDSource` 删、`runTunnel` 的 mesh 块换 §8 accept 循环)
- Modify: `cmd/core.go:186`(删 `WireUnifiedIngressMeshDispatch` 调用)
- Modify: `inbound/sniff/sniff.go`(删 `KindMesh` + `'{'` 分支 + iota 重排 + `String()`)
- Modify: `inbound/http/dispatch.go`(删 `KindMesh` 分支与 `MeshConnHandler` 变量 + 文件头注释)
- Modify: `inbound/http/server.go`(日志文案「嗅探 SSH/socks5/mesh/HTTP」→ 三协议)
- Modify: `processor/tcp/mesh_inbound.go`(瘦身为只含 `RouteMesh` 常量;DNS 函数已搬走;`HandleMeshConn`/`MeshInboundDeps`/`ProductionDeps` 消费面/`meshMetadataFromTarget`/`WriteMeshRejectLine`/`writeMeshRejectLine`/`WrappedConn` 回放段删——**先 `grep -rn "WriteMeshRejectLine\|MeshInboundDeps\|HandleMeshConn\|meshproto\." --include="*.go" | grep -v _test` 确认全部消费方已死再删**)
- Modify: `outbound/meshproto/header.go`(删 `Header`/`Encode`/`Parse`/`AppendHop`/`ClassCarrierAllowed`/`CarrierLAN`/`CarrierRelay`/`MaxHeaderBytes`/`hasControlByte`;保 `Class*`/`DefaultHops`/`MaxHopsCap`/`DNSTarget`/`IsDNSTarget`/`SplitTarget`;包注释改「mesh 语义常量包,SPEC §12」)
- Modify: `app/tunnel/bridge.go`(`MeshEndpointID` 注释)、`app/tunnel/piko_logger.go`(`reportMeshRelayDisabled` 注释,SPEC §13.1 两处只改注释行)
- Modify: `app/http/handlers/mesh_handler.go`(`meshStatusResponse` 增 `Effective bool json:"effective"`,`HandleStatus` 里 `cfg.MeshEffectivelyEnabled()` 填充;`Peers` 透传含 `host_key` 保留——前端渲染输入位需要,SPEC §17 记账)
- Test: 删/改 `processor/tcp/mesh_inbound_test.go`、`processor/tcp/handler_mesh_test.go`(守卫/缓存测试已在新文件)、`inbound/sniff/sniff_test.go:84`(String 断言去 mesh)、`app/tunnel/manager` 相关测试(`runConfig.meshRelay` 引用清零)、dispatch 测试(`grep -rn "KindMesh\|MeshConnHandler" --include="*_test.go"` 找齐)

- [ ] **Step 1: sniff/dispatch 删除**(iota:`KindUnknown=0, KindSSH, KindSocks, KindHTTP`;`String()` 去 mesh;classify 去 `'{'`;dispatch 去 KindMesh 分支与 `MeshConnHandler` 变量;文件头注释改三协议;`inbound/http/server.go` 日志文案同步)

- [ ] **Step 2: manager.go accept 循环**(SPEC §8 代码为主体,落 `runTunnel`)

```go
// B-side mesh relay endpoint:每条 yamux 流 = 一条 SSH 入口连接,喂
// sshin.HandleIngressConn(每连接必须起 goroutine——ServeConn 阻塞至会话
// 结束,串行 accept 会把 B 侧退化为单并发,SPEC §8.1)。
if config.meshRelayEnabled { // 由 validateConfig 按需置位,或直接去掉该 gate
	meshListener, meshErr := upstream.Listen(ctx, MeshEndpointID(config.deviceID))
	log.Printf("[AGENT-TUNNEL] mesh listen %s: err=%v addr=%v", MeshEndpointID(config.deviceID), meshErr, func() any {
		if meshErr == nil {
			return meshListener.Addr().String()
		}
		return ""
	}())
	if meshErr != nil {
		reportMeshRelayDisabled(config.onState, fmt.Sprintf("mesh relay unavailable: %v", meshErr))
	} else {
		endpoint := MeshEndpointID(config.deviceID)
		go func() {
			<-ctx.Done()
			_ = meshListener.Shutdown()
		}()
		go func() {
			for {
				conn, err := meshListener.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					c = sshin.WithRemoteLabel(c, "piko-"+endpoint) // 无冒号(SPEC §8.2)
					if !sshin.HandleIngressConn(c) {
						log.Printf("[MESH-RELAY] ingress not running, dropped conn %s", c.RemoteAddr())
					}
				}(conn)
			}
		}()
	}
}
```

(`config.meshRelayEnabled`:原 `runConfig.meshRelay` 字段改为 bool,`validateConfig` 里 `relay.New` 行删除、改为不再构造;或直接删字段+无条件尝试 Listen——**采用无条件尝试**,与现网「无条件 Listen、逐连接门禁」注释一致。)
`validateConfig` 同步删:`meshRelay := relay.New(...)`、`SetMeshSelfIDSource` 调用、`tcp.SetMeshPikoDialSource(func() meshproxy.PikoDialFunc { return m.dialMeshEndpoint })` → `tcp.SetMeshTransportDialSource(func() meshproxy.PikoDialFunc { return m.dialMeshEndpoint })`;`import relay`/`import meshproto`(manager 里的 `meshproto.CarrierRelay` 随 Serve 删而清)。

- [ ] **Step 3: 删 `app/mesh/relay/`、`app/tunnel/mesh_dispatch.go`、`cmd/core.go:186` 调用、`test/mesh_integration_test.go`**(H1 重建)

- [ ] **Step 4: meshproto 剪枝 + mesh_inbound.go 瘦身**(只留 `RouteMesh`;注释同步「mesh 语义常量包」)

- [ ] **Step 5: mesh_handler.go 加 `Effective` 字段**

- [ ] **Step 6: bridge.go/piko_logger.go 注释改写**(SPEC §13.1 两行)

- [ ] **Step 7: `go build ./... && go test ./...` 全绿(修所有引用断裂)→ Commit**

```bash
git add -A
git commit -m "重构:删除 mesh 帧协议入站路径——relay/HandleMeshConn/KindMesh/sniff 分支退役,piko 中继改喂 SSH 入口"
```

### Task F2: processor/config 依赖收尾 + defaults 注释扫尾

**Files:**
- Modify: `processor/config/types.go`(`MeshConfig` 注释区扫尾:`max_hops` 一期占位注已落 Task A1)
- Modify: `cmd/config.go` 确认 `ApplyConfig` 启动路径**没有**调 `ValidateMeshDependency`(SPEC §10.3:启动降级走 `MeshEffectivelyEnabled`,绝不阻断 boot)——加一行注释到 `Config.ValidateMesh` 或 `MeshEffectivelyEnabled` 上方声明该分工。

- [ ] **Step 1: 核对 + 注释** → **Step 2: `go build ./...` → Commit(可与 F1 合并,若有独立改动)**

---

# Phase G:前端(SPEC §13.3,全部必改)

### Task G1: 归一化透传 + 保存载荷(MeshSettings.js/.vue)

**Files:**
- Modify: `app/website/src/components/settings/MeshSettings.js:48-54,71-75,83-89`
- Modify: `app/website/src/components/settings/MeshSettings.vue:770-783`(save 的 fallback 字面量)
- Test: `app/website`(vitest)——`grep -rn "normalizeMesh" app/website/tests/ app/website/src/**/*.spec.*` 找既有单测文件追加

- [ ] **Step 1: 先写失败测试**:`normalizeMeshPeers` 透传 `host_key`(空串不落键)、`normalizeMeshFallback` 透传 `host_key`、`normalizeMeshConfig` 透传 `private_key`/`key_passphrase`。

- [ ] **Step 2: 确认失败** → **Step 3: 实现**(三个 normalize 函数各加一行透传,空串不落键;`MeshSettings.vue` save 的 `fallback` 字面量改为 `fallback: { ...(this.config?.mesh?.fallback || {}), enable: …, addr: …, classes: … }`)

- [ ] **Step 4: `cd app/website && npm test -- --run` 全绿 → Commit**

```bash
git add app/website/src/components/settings/
git commit -m "修复:组网配置归一化透传 host_key/private_key/key_passphrase,保存载荷不再抹掉密钥字段"
```

### Task G2: 清空路径(SettingsPage.vue)

**Files:**
- Modify: `app/website/src/components/SettingsPage.vue:503-534`(`buildCustomerConfigPatch`)

- [ ] **Step 1: 失败测试**(patch 构造:mesh.private_key 从有值改空串 → patch.mesh.private_key === '')→ **Step 2: 实现**(SPEC §13.3.3 代码逐字,插在 `applyAgentCoreLLMClearing` 之前;含 fallback.host_key 分支)→ **Step 3: 测试过 → Commit**

```bash
git commit -m "修复:组网凭据清空不可达——private_key/key_passphrase/fallback.host_key 空串显式进 patch"
```

### Task G3: UI 输入位 + i18n + e2e 修断言

**Files:**
- Modify: `app/website/src/components/settings/MeshSettings.vue`(三个输入位:peer 行 `host_key`、兜底行 `host_key`、节点身份 `private_key`+`key_passphrase`;验收=填→保存→刷新仍在;清空→保存→刷新为空)
- Modify: `app/website/src/i18n/zh.js` 与 `en.js`(`mesh_peerHostKey`/`mesh_fallbackHostKey`/`mesh_privateKey` + placeholder/hint 各两条,zh 文案见 SPEC §13.3.6;**en.js 漏了会回落显示 key 名**)
- Modify: `app/website/tests/e2e/mesh-settings.spec.js:184-188`(`toEqual` 整对象比对必失——改为逐字段断言或给期望对象补 `host_key: ''`;`:193-195` toBeUndefined 系列复核)

- [ ] **Step 1: i18n 三键 × 两语言 + placeholder/hint** → **Step 2: 输入位**(样式对齐既有 peer 行输入;私钥框用 textarea,`autocomplete="off"`)→ **Step 3: e2e 断言更新** → **Step 4: `npm run test:e2e -- mesh-settings` 通过(vitest + e2e 全跑一遍)→ Commit**

```bash
git add app/website/
git commit -m "新增:组网页面三个密钥输入位(host_key×2/节点私钥)+ i18n + e2e 断言更新"
```

---

### Task G4: 依赖降级与身份不一致的两条仪表盘提示(SPEC §10.3,评审补)

**Files:**
- Modify: `app/website/src/components/settings/MeshSettings.vue`(提示位 ×2)
- Modify: `app/website/src/i18n/zh.js`、`en.js`(文案 ×4)
- Test: `app/website`(vitest,提示条件渲染断言)

- [ ] **Step 1: 失败测试**(两个条件渲染:① `/api/config/customer` 里 `mesh.enable` 有效开启但 `ssh_ingress.enable` 非 true → 卡片内出现「依赖 SSH 入口」警告条,含「一并开启」按钮;② 每个 peer 的 `device_id` 与 `ssh_ingress.mesh_authorized_keys` 尾注释比对不一致 → 该 peer 行旁出现警告级(非阻断)提示)

- [ ] **Step 2: 确认失败** → **Step 3: 实现**

1. 依赖提示:计算属性 `sshIngressOff = (config?.ssh_ingress?.enable !== true) && (mesh 已启用)`(mesh 有效启用 = `enable !== false`,对齐后端三态);警告条文案:`mesh_sshDependencyWarning`。**「一并开启」按钮只做一层确认后写入 `config.ssh_ingress.enable = true` 随整体保存提交,不单独发请求、不静默改写**(SPEC §10.3 原文「不静默改写用户配置」——按钮触发确认弹窗,文案 `mesh_sshEnableTogether`)。
2. 身份不一致提示:前端可从 `config.ssh_ingress.mesh_authorized_keys` 解析每行尾注释(`parseAuthorizedKeyComment` 小助手:取行内最后一个空白分隔段,`ssh-ed25519`/`AAAA` 开头段跳过),与 `peers[].device_id` 逐个比对;不一致 → peer 行下警告级文案 `mesh_deviceIdMismatchHint`(「与本机准入表的尾注释不一致,仅提示不阻断」)。解析失败/无注释 → 不提示(对端可省注释,SPEC §10.3「不强制一致」)。

- [ ] **Step 4: i18n 四键(zh/en 成对)**:`mesh_sshDependencyWarning`「组网依赖 SSH 入口,当前未启用,出口不会生效」、`mesh_sshEnableTogether`「一并开启 SSH 入口」、`mesh_deviceIdMismatchHint`「device_id 与本机准入表尾注释不一致(仅提示)」、`mesh_sshDependencyTitle`「依赖提示」。en 对应直译。

- [ ] **Step 5: vitest 全绿 → Commit**

```bash
git add app/website/
git commit -m "新增:组网页依赖降级与身份不一致两条仪表盘提示(SPEC §10.3)"
```

# Phase H:集成测试与文档

### Task H1: `test/mesh_integration_test.go` 重写(两节点本地回环,SPEC §15.2)

**Files:**
- Create(重建,F1 已删旧版): `test/mesh_integration_test.go`
- 参考:既有 `inbound/ssh/server_test.go` 的 sshd 测试构造(直接复用其 helper,`grep -n "func newTestServer\|gossh.NewServerConn" inbound/ssh/server_test.go | head`)

场景清单(逐个 `t.Run`):
1. **单跳端到端**:A `ExitClient`(真 LAN transport 回环)→ B 真 `inbound/ssh`(Reconcile 注入专用口)→ `mesh-exit` ExtraData 注入 → B 侧 handler 替身收到 Metadata(Route=RouteMesh/MeshInjected=true)→ 回显字节校验。
2. **形态隔离**:mesh 凭证开 `direct-tcpip` → OpenChannel 被拒;bare 凭证开 `mesh-exit` → 被拒。
3. **环路阻断**:带 `MeshInjected` 的连接在 B 侧命中 mesh 规则 → `dialViaMesh` 拒(单元层已测,集成层钉一次)。
4. **host key 冒充**:B 换自签钥冒充 → A 拒(host key mismatch)。
5. **握手挂死**:对端 accept 不回 banner → A 在 deadline 内失败。
6. **并发**:两个 peer 同时经 piko fake 连入(两 goroutine 并发 OpenChannel)不串行。
7. **DNS 代转发**:`dns:` payload 往返(fake resolver)。
8. **失败语义**:对端不可达 → 错误回源(ExitClient 返回错误,不回落 direct)。
9. **piko 中继 ingress 未运行**:`HandleIngressConn` 返回 false 的 WARN 路径(manager accept 循环测试替身)。

- [ ] **Step 1: 逐场景写测试(先 1/2/4/5 核心,后其余)** → **Step 2: 全部通过** → **Step 3: Commit**

```bash
git add test/mesh_integration_test.go
git commit -m "测试:mesh SSH 链两节点回环集成——单跳/形态隔离/冒充/挂死/并发/DNS/失败语义"
```

### Task H2: 发布说明 + SPEC 状态

**Files:**
- Create: `docs/release-notes/2026-11-11-mesh-ssh-chain.md`(SPEC §14.1 全文 + §14.2 七步人工动作,逐字搬)
- Modify: `docs/superpowers/specs/2026-11-10-mesh-as-ssh-proxy-chain-design.md` 头部状态行 → 「已实施(见 plans/2026-11-10-mesh-as-ssh-proxy-chain.md)」

- [ ] **Step 1: 写发布说明(SPEC §14.1 原文,含两条对称暴露面警告)** → **Step 2: SPEC 状态行更新** → **Step 3: Commit**

```bash
git add docs/
git commit -m "文档:mesh 收敛发布说明(不互通警告/密钥人工动作/绑定面暴露面)与 spec 状态更新"
```

### Task H3: 全量回归

- [ ] **Step 1: `go build ./...`**
- [ ] **Step 2: `go test ./...`(全仓;预存 flake 按 [[preexisting-test-flakes-subprocess-timeout]] 口径:auth 基线与 agent_ai 子进程超时族偶败属基线,单包复跑即绿;**不得复跑洗白新红**)**
- [ ] **Step 3: `cd app/website && npm test -- --run && npm run test:e2e`**
- [ ] **Step 4: `grep -rn "HandleMeshConn\|meshproto.Header\|KindMesh\|MeshConnHandler\|PeerTarget\|WriteMeshRejectLine\|ClassCarrierAllowed\|CarrierLAN\|SetMeshPikoDialSource\|SetMeshSelfIDSource" --include="*.go" . | grep -v _test` → 期望零命中(死符号清零)**
- [ ] **Step 5: 手工冒烟清单(真机,由用户执行;写入报告)**:两台设备同网段 → 开 SSH 入口 + 双向密钥互填(含 host_key 钉扎)+ `x.com,mesh:<peer>` 规则 → 验证 AI/自身流量经对端出口(SPEC §15.3)

---

## 执行注意事项(全 Task 通用)

1. **每个 commit 前必须 `go build ./...` + 受影响包 `go test`;不得提交红**。全仓预存红按记忆口径识别(基线 flake 单包复跑;auth 基线 5/10 不能复跑洗白)。
2. **提交信息全中文**「新增:/修复:/重构:/测试:/文档:」标题;禁止 force-push master。
3. **行号漂移**:SPEC 与本计划所有行号仅作定位提示,以 `grep -n "符号名"` 为准。
4. **TDD 纪律**:每个 Task 先红后绿;新符号先有消费失败测试再实现。
5. **SPEC 互锁字面量**:`channelMeshExit`/`meshExitMaxExtra`/`"t":"dns"` 线格式在 A/B 两侧各有一份,由测试互锁钉住(集成测试断言两侧同值)。
6. **e2e 已知 master 预存**:`chat-composer-style` 为预存失败([[i18n-copy-readability-merged]]),不算本批回归。
