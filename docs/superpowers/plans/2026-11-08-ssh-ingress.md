# SSH 入口实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 节点内置 x/crypto sshd 的 SSH 入口,双形态:标准 ssh 客户端裸流(-D/-L)与 mesh 帧外壳(节点互连加密载体),双密钥准入,总开关默认关。

**Architecture:** `inbound/ssh` 新包(生命周期/host key/双密钥认证/限速/安全收敛 channel 白名单/双形态注入);配置挂 `customer.ssh_ingress`(尾部追加);mesh 帧元数据经 `IngressFrameMeta` context 携带(消费=dialByRoute 续传,合并后集成契约);host key 指纹 API 双注册。

**Tech Stack:** Go + `golang.org/x/crypto/ssh`(v0.48 direct);测试用真 x/crypto 客户端连内置 sshd(镜像出口侧的 sshtest 先例);前端 Vue3 + vitest。

**Spec:** `docs/superpowers/specs/2026-11-08-ssh-ingress-design.md`(两轮审查通过)

**工作目录:** worktree `.worktrees/ssh-ingress`(分支 `feat/ssh-ingress`)。提交信息全中文「新增:/修复:/加固:」+双行 footer(🤖 Generated with [Claude Code](https://claude.com/claude-code) + Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>)。

**⚠️ 与组网分支(feat/mesh-forwarding,并行开发中)的冲突纪律(spec §8):**
- **禁止改动**:`processor/tcp/mesh_inbound.go`、`app/mesh/`、`outbound/proxy/mesh/`(本分支不存在,创建即制造冲突);`app/tunnel/manager.go` 存在于本分支但属 mesh 分支改动领地,同样不碰;
- mesh 帧格式/解析在本分支**独立实现**(新文件 `inbound/ssh/meshframe.go`,文件头注释声明「与 mesh 分支 meshproto 同源,合并后统一」),**不 import 不猜测 mesh 代码**;
- config 改动**只追加在尾部**:CustomerConfig 字段加在 PeerMCP 之后、白名单条目加在各列表末尾;
- 注入胶水在 inbound/ssh 内,不新增 processor/tcp 文件。

**验证基线(已验):** `go build ./inbound/... ./processor/tcp/ ./outbound/proxy/ssh/...` 与 `go test ./outbound/proxy/ssh/... ./inbound/http/ -count=1` 全绿。

---

### Task 1: 配置层 customer.ssh_ingress

**Files:**
- Modify: `processor/config/types.go`(CustomerConfig 尾部加字段;PeerMCPConfig 三态模式旁加新类型+Validate;Effective accessor;可编辑面白名单;unknown-key switch)
- Modify: `processor/config/types_test.go`
- Modify: `app/http/services/customer_config_service.go`(editable 列表+forbidden 文案两处)
- Test: 上述测试文件

- [ ] **Step 1: 写失败测试**(types_test.go 追加;先读既有 PeerMCP 测试构造惯例)

```go
func TestSSHIngressValidate(t *testing.T) {
	// enable+port 0 → 报 server port 类错误;port 70000 → 拒
	// enable 且两列表全空 → 「requires at least one authorized key」
	// 单条公钥非合法 authorized keys 格式(如 "not-a-key")→ ParseAuthorizedKey 拦
	// 同一公钥(逐字节同)出现在两列表 → 「listed in both」歧义拦截
	// 合法形态(ed25519 authorized keys 行)→ 过
}

func TestCustomerConfigSSHIngressEditable(t *testing.T) {
	// validateCustomerEditableSurface:customer.ssh_ingress 合法载荷过;未知键仍拒(回归)
	// Config.Validate→switch 接线:enable+缺凭证报错(经 Validate,非静默跳过)
}

func TestEffectiveSSHIngress(t *testing.T) {
	// nil/未启用 → nil;启用+合法载荷 → 归一化副本(port 默认 56441;公钥 trim;空行剔除)
	// [合并前变更] 默认口已改 56442 避让 mesh LAN TCP 56441(见 spec §7 端口裁定),本文其余 56441 为历史记录
}
```

- [ ] **Step 2: 红 → 实现(五处,全部尾部/末尾追加)**

1. `CustomerConfig` 结构 PeerMCP 字段之后加:
```go
	// SSHIngress 是 SSH 入口服务面(customer.ssh_ingress)。nil 时服务关闭——
	// 默认关,升级零回退。与组网分支的 MeshConfig 为相邻行合并关系(双方尾部追加)。
	SSHIngress *SSHIngressConfig `json:"ssh_ingress,omitempty"`
```
2. 新类型(放 PeerMCPConfig 及其方法之后):
```go
// SSHIngressConfig 控制 SSH 入口服务面(customer.ssh_ingress)。
// 双密钥准入:authorized_keys=人用凭证(裸流形态);mesh_authorized_keys=
// 节点互连凭证(mesh 帧形态)。禁密码。端口默认 56441。
type SSHIngressConfig struct {
	Enable             *bool   `json:"enable,omitempty"` // 三态:nil=false
	Port               uint16  `json:"port,omitempty"`
	AuthorizedKeys     []string `json:"authorized_keys,omitempty"`
	MeshAuthorizedKeys []string `json:"mesh_authorized_keys,omitempty"`
}

func (c *SSHIngressConfig) IsEnabled() bool // nil=false;PeerMCP 同款

func (c *SSHIngressConfig) Validate() error {
	// 未启用直接过;启用则:port ∈ 1..65535(0=默认 56441 由 Effective 归一,0 视为未配);
	// 两列表合计非空;逐条 gossh.ParseAuthorizedKey 校验([host:port] 可选后缀容忍,
	// 参照 authorized_keys 行格式);逐字节重复键跨列表 → 歧义拦截
}
```
3. `EffectiveSSHIngress()`(模式:未启用/nil→nil;归一化副本:port 0→56441;公钥 trim+剔空行;深拷贝切片)。
4. 可编辑面:`validateCustomerEditableSurface` 与 `Config.Validate` 的 switch(case *SSHIngressConfig→Validate)——**注意本分支无 customer.mesh,switch 只加本类型**。
5. **allowlist 同步(仓库铁律:两 switch+三文案=五处)**:`customerUnknownKeyErrors`(types.go:1166)文案字段表、`extractCustomerUnknownFields` 的键 switch(types.go:1198)、customer_config_service.go:383 forbidden 文案、:412-415 的 case switch **与 :415 default 错误文案**(紧邻 case 列表下方,同字段表,漏了=误导性拒绝提示)——全加 `ssh_ingress`。
6. import:`golang.org/x/crypto/ssh`。

- [ ] **Step 3: 绿 + 零回归**

Run: `go test ./processor/config/ -count=1` 全绿;`go test ./app/http/services/ -run TestForbidden -count=1`(若有相关测试)。

- [ ] **Step 4: 提交** — `新增:customer.ssh_ingress 配置层——双密钥准入字段/Validate/可编辑面四处同步`

---

### Task 2: inbound/ssh 骨架——生命周期/host key/安全收敛/限速

**Files:**
- Create: `inbound/ssh/server.go`(生命周期+host key+accept 循环)
- Create: `inbound/ssh/config.go`(运行期配置结构+LocalNodeID 注入点)
- Create: `inbound/ssh/ratelimit.go`(每 IP 失败限速)
- Test: `inbound/ssh/server_test.go`

- [ ] **Step 1: 写失败测试**(真 x/crypto 客户端连内置 sshd;host key 路径注入 t.TempDir)

```go
func TestSSHIngressStartStopIsRunning(t *testing.T) // 三态;重复 Start 幂等;Stop 后 Is=false
func TestSSHIngressHostKeyStableAcrossRestarts(t *testing.T) // 指纹重启不变(文件复载)
func TestSSHIngressHostKeyRegeneratedOnCorrupt(t *testing.T) // 写坏文件→Start 重生成+指纹变化
func TestSSHIngressRejectsNonDirectTCPIPChannels(t *testing.T) // 逐类型:session/shell 请求、forwarded-tcpip、auth-agent@openssh.com、X11 全 Reject;direct-tcpip Accept(用 testdata 客户端发起)
func TestSSHIngressRateLimitsAuthFailures(t *testing.T) // 同 IP 连续 N 次未知公钥失败→进入限速窗,窗口内连入即拒(ratelimit 拒绝文案可区分)
func TestSSHIngressDisabledRefuses(t *testing.T) // 未 Start(=开关关)端口连接层拒绝——由 reconcile 不启动保证,本测试断言 IsRunning=false 时拨号被拒
func TestSSHIngressPasswordAuthNotRegistered(t *testing.T) // 客户端尝试 password/keyboard-interactive 认证→被拒(spec §6「密码不注册」的实证断言,非仅构造性)
```

- [ ] **Step 2: 红 → 实现**

`config.go`:
```go
// RunConfig 是服务运行期配置(由配置层 EffectiveSSHIngress 翻转而来)。
type RunConfig struct {
	Port         uint16
	AuthorizedKeys []gossh.PublicKey   // 形态①(解析自 authorized_keys)
	MeshKeys     []gossh.PublicKey     // 形态②
	// LocalNodeID 返回本机节点标识(防环 AppendHop 用)。由装配层注入
	// (AgentService.currentDeviceID);测试注入闭包。空函数=形态②整体禁用(防御)。
	LocalNodeID  func() string
	// ForwardGate 返回(转发总开关, 允许的转发方列表)。**合并集成缝**:
	// customer.mesh 的 EffectiveForwardEnabled+Peers ACL 随 mesh 分支落地,
	// 本分支注入 nil=全放行(凭证列表即 ACL,防御已含);合并后装配层
	// 改注入 mesh 的门禁函数,测试可注入闭包。
	ForwardGate func() (bool, []string)
}

type Server struct { /* listener, config, hostKeySigner, mu, running, limiter */ }
func NewServer(rc RunConfig, hostKeyPath string) *Server
func (s *Server) Start() error / Stop() error / IsRunning() bool   // 幂等,照 inbound/http 三件套
```
`server.go`:
- host key:加载 `hostKeyPath` PEM;miss/损坏→生成 ed25519 并原子写(临时+rename,0o600);`HostKeyFingerprint() (fp, algo string)` 导出供 API;
- `gossh.ServerConfig`:NoClientAuth=false;**不设 PasswordCallback**(禁密码);PublicKeyCallback=Task 3 接线点(本任务先接 通配 allow-all 的测试桩?——**不**,本任务先让 PublicKeyCallback 恒拒,Task 3 替换;测试里非 direct-tcpip 用例配测试专用公钥路径在 Task 3 后补跑)——**顺序修正:Task 2 与 Task 3 在同包强耦合,合并为一个交付单元,channel 白名单测试在 Task 3 完成后跑**;
- ServerConfig.AddHostKey(hostkey);每连接:限速检查→`gossh.NewServerConn`;
- channel 循环:仅 `direct-tcpip` Accept,其余(含 global request 的 shell 相关)Reject;**本任务 direct-tcpip Accept 后暂只 drain+close**(注入在 Task 4/5 接);
- global request:keepalive@openssh.com 应答,其余 Reply(false)。
`ratelimit.go`:map[ip]{fails, lastFail};成功认证清零;窗口按 `1s<<min(fails,5)` 封顶 30s;`Allow(ip) bool`/`Fail(ip)`。

- [ ] **Step 3: 绿(本任务可绿用例:生命周期/host key 三态/限速/DisabledRefuses;channel 白名单用例标记 skip 注释指向 Task 3)**

Run: `go test ./inbound/ssh/ -count=1` 通过(允许 skip 两例)

- [ ] **Step 4: 提交** — `新增:inbound/ssh 骨架——生命周期/host key 原子落盘/安全收敛白名单/认证限速`

---

### Task 3: 双密钥认证+凭证→形态

**Files:**
- Modify: `inbound/ssh/server.go`(PublicKeyCallback 接线+Permissions.Extensions["ingress_form"])
- Modify: `inbound/ssh/config.go`
- Test: `inbound/ssh/server_test.go`(补 skip 用例)

- [ ] **Step 1: 红测试**

```go
func TestAuthHumanKeyMarksBareForm(t *testing.T)      // 人用公钥连入→direct-tcpip Accept(此前 skip 用例转绿)
func TestAuthMeshKeyMarksMeshForm(t *testing.T)       // mesh 公钥连入→direct-tcpip Accept(帧处理 Task 5 接,本断言=Accept)
func TestAuthUnknownKeyRejectedAndRateLimited(t *testing.T)
func TestAuthDualListedKeyMeshWinsAtRuntime(t *testing.T) // 双列表同键(绕过 Validate 直构造 RunConfig)→mesh 形态保守优先
```

- [ ] **Step 2: 实现**

PublicKeyCallback(meta, key):key ∈ MeshKeys→`Permissions.Extensions["ingress_form"]="mesh"`;∈ AuthorizedKeys→`"bare"`;否则 nil+limiter.Fail(remote ip)。通道处理经 `conn.Permissions().Extensions["ingress_form"]` 读形态。双列表同键:运行期 mesh 优先(Validate 拦截是第一道,此为防御)。

- [ ] **Step 3: 全绿**(含 Task 2 的 skip 用例转正)→ **Step 4: 提交** — `新增:SSH 入口双密钥认证——公钥即形态区分器,禁密码+限速`

---

### Task 4: 裸流注入(形态①)

**Files:**
- Modify: `inbound/ssh/channel.go`(新建:direct-tcpip payload 解析+注入)
- Modify: `inbound/ssh/server.go`(accept 后按形态分流)
- Test: `inbound/ssh/inject_test.go`

- [ ] **Step 1: 红测试**(注入目标=测试桩 TCPConnHandler;参照 mesh 计划 Task 6 的 `InjectsTLSConnIntoPipeline` 风格)

```go
func TestBareStreamInjectsIntoPipeline(t *testing.T) {
	// 注册测试桩 handler(tcphandler.SetHandler 或既有注入点);客户端 -L 语义发
	// direct-tcpip(target=回显地址);桩断言 Metadata{HostName==target host,
	// DstPort, SrcIP==127.0.0.1};端到端:桩回写数据,客户端收到
}
func TestBareStreamConnIDAndCtxCarry(t *testing.T) // ConnID 前缀 sshing-;ctx 为可取消 context
```

- [ ] **Step 2: 实现 channel.go**

```go
type directTCPIPMsg struct{ Addr string; Port uint32; OriginAddr string; OriginPort uint32 }
// 解 payload→M.Metadata{Network: M.TCP(Network 是 uint8 iota,不要写字符串"tcp"),
// ConnID:"sshing-"+seq, SrcIP/SrcPort(来自 ssh 连接 RemoteAddr), HostName:Addr,
// DstPort:Port;DstIP 不设——inbound/http CONNECT 对不可解析域名同样留空,管线隧道期重试}
// →tcphandler.GetHandler().Handle(ctx, channel, metadata)
// (getTCPHandler 是 inbound/tun/tunnel 包内未导出 helper,外部用导出的 tcphandler.GetHandler)
// M = inbound/tun/metadata(processor/tcp/types.go 另有同名 Metadata,无 ConnID,勿混)
```
`gossh.Channel` 已实现 net.Conn(含 CloseWrite);**注意 deadline no-op 已知限制**(出口 spec 已知限制同源),注释交叉引用。

- [ ] **Step 3: 绿 → Step 4: 提交** — `新增:SSH 入口裸流注入——direct-tcpip 解析构 Metadata 喂 processor 管线`

---

### Task 5: mesh 帧形态(②)——帧解析/防环/门禁/IngressFrameMeta

**Files:**
- Create: `inbound/ssh/meshframe.go`(帧常量/解析/AppendHop——**独立实现,文件头注释声明与 mesh 分支 meshproto 同源,合并后统一**)
- Create: `inbound/ssh/mesh_inject.go`(形态② 注入)
- Modify: `inbound/ssh/channel.go`(形态分流)
- Test: `inbound/ssh/mesh_inject_test.go`

- [ ] **Step 1: 红测试**

```go
func TestMeshFrameParse(t *testing.T) // JSON 头行解析:from/class(self|ai|custom)/target(host:port|裸域名|dns:)/via[]/hops
func TestMeshFormAppendsHopAndInjects(t *testing.T) // 帧凭证连入→首行帧→AppendHop(本机 ID)→注入;桩断言目标/ctx 上 IngressFrameMeta{Class,Via(含本机),Hops+1}
func TestMeshFormRejectsLoop(t *testing.T)            // Via 含本机→拒绝+错误行回写
func TestMeshFormRejectsHopsCap(t *testing.T)         // hops>=8(常量 MeshHopsHardCap,注释:合并对齐 mesh)→拒绝
func TestMeshFormRejectsDNSTarget(t *testing.T)       // dns: 目标→错误行「dns target not supported on ssh carrier」
func TestMeshFormGateClosedWhenDisabled(t *testing.T) // LocalNodeID 未注入(=装配未完成,防御)→拒;Enable=false 场景在 reconcile 层拦,此处测纵深
func TestMeshFormForwardGate(t *testing.T) // ForwardGate 注入闭包:false→拒(文案区分 forward-disabled);true+列表→过(列表语义=合并后接 mesh ACL,本分支凭证列表即 ACL,此处测钩子通路)
func TestMeshFormBadHeaderLineDisconnects(t *testing.T) // 非 JSON/缺字段→静默断开
```

- [ ] **Step 2: 实现 meshframe.go + mesh_inject.go**

```go
// meshframe.go — 帧头 JSON(与 mesh 分支 meshproto 同源;合并后统一):
// {"from":"<device_id>","class":"self|ai|custom","target":"host:port|domain|dns:domain","via":["..."],"hops":N}
const MeshHopsHardCap = 8 // 注释:合并时与 mesh 对齐(设计默认 5/上限 8)
type MeshFrameHeader struct{ From, Class, Target string; Via []string; Hops int }
func ParseMeshFrameHeader(r *bufio.Reader) (*MeshFrameHeader, error)  // 首行 JSON;超长行(64KB)拒绝
func (h *MeshFrameHeader) AppendHop(id string)
// mesh_inject.go — 形态②:Parse→dns: 拒→ForwardGate 检查(false 拒,
// 文案区分 forward-disabled / not-allowed)→AppendHop→防环→hops 帽→
// IngressFrameMeta{Class,Via,Hops} context.WithValue(导出键
// ContextKeyIngressFrameMeta;注释:合并后迁共享低层包,见 spec 集成期注记)
// →Metadata{HostName=Target(裸域名保留,不解析)}→Handle
```
错误回写:拒帧时向 channel 写一行 JSON 错误(`{"err":"loop"}` 等)后关闭——与 mesh B 侧「错误回写」语义一致。

- [ ] **Step 3: 全绿 → Step 4: 提交** — `新增:SSH 入口 mesh 帧形态——帧解析/防环/跳数帽/dns拒/IngressFrameMeta context 接缝`

---

### Task 6: host key 指纹 API(双注册)

**Files:**
- Create: `app/http/handlers/ssh_ingress_handler.go` + `ssh_ingress_handler_test.go`
- Modify: `app/http/routes/routes.go`(Handlers 字段+newHandlers+register 一行)
- Modify: `app/agentruntime/server.go`(registerAgentRoutes 一行)
- Modify: `app/http/services/customer_config_service.go`(配置提交 reconcile,见 Task 7)

- [ ] **Step 1: 红测试**(handler 收 host key 路径注入,生产口径 FromCache=GetCacheSubdir("ssh")/ingress_host_key)

```go
func TestSSHIngressHostkeyHandler(t *testing.T) // GET→{fingerprint,algorithm,generated_at};文件缺失→触发生成再返回;POST→405(http.Error,规避 ErrorCodeToHTTPStatus→500 坑,注释引用 ssh_known_hosts_handler 先例)
```

- [ ] **Step 2: 实现+双注册**

`register("/api/proxy/ssh-ingress/hostkey", h.SSHIngress.HandleHostkey, http.MethodGet)`;agentruntime 同名 mux.HandleFunc;**parity 已覆盖 /api/proxy**(出口任务扩围),跑 parity 确认。

- [ ] **Step 3: 绿 → Step 4: 提交** — `新增:SSH 入口 host key 指纹 API——GET 生成兜底,双注册`

---

### Task 7: 运行面接线——配置提交 reconcile 启停

**Files:**
- Modify: `app/http/services/customer_config_service.go`(配置提交协调路径加 SSH 入口 reconcile)
- Test: `app/http/services/ssh_ingress_reconcile_test.go`

- [ ] **Step 1: 红测试**

```go
func TestSSHIngressReconcileStartsAndStops(t *testing.T) // 配置 enable→IsRunning=true 且端口监听;disable→false;端口变更→重启;配置错误→保持停+日志
```

- [ ] **Step 2: 实现**

`inbound/ssh.Reconcile(rc *RunConfig, hostKeyPath string, localNodeID func() string)`(幂等:期望态 vs 运行态比对,端口/凭证变更即重启);customer_config_service 的提交协调路径(现 refresh customer proxy registry 附近,:170 区)调 reconcile;LocalNodeID 注入=`GetSharedAgentService().currentDeviceID`。reconcile 失败仅日志不阻断配置提交(服务面非关键路径)。

- [ ] **Step 3: 绿 → Step 4: 提交** — `新增:SSH 入口运行面接线——配置提交 reconcile 启停与端口变更重启`

---

### Task 8: 前端卡片+i18n

**Files:**
- Create: `app/website/src/components/settings/SshIngressSettings.vue`
- Modify: `app/website/src/components/SettingsPage.vue`(代理族挂载新卡片)
- Modify: `app/website/src/i18n/zh.js`、`en.js`
- Test: `app/website/src/components/settings/SshIngressSettings.test.js`

- [ ] **Step 1: vitest 红**(挂载式,参照 RulesSettings.test.js 范式):开关/端口/指纹展示(fetch mock /api/proxy/ssh-ingress/hostkey 读 data 信封+复制按钮)/人用公钥列表增删(载荷校验)/mesh 公钥只读+说明/enable 时无任何公钥→保存拦截文案
- [ ] **Step 2: 实现**(提交载荷 `{ssh_ingress:{enable,port,authorized_keys,mesh_authorized_keys}}`;SettingsPage 归一化透传+类型置零同族处理;i18n zh/en 同步,口径:「」、你、全角标点、上游代理)
- [ ] **Step 3: vitest 全绿 + `npm run build` → Step 4: 提交** — `新增:仪表盘 SSH 入口卡片——开关/指纹/双公钥管理/i18n`

---

### Task 9: 全量回归收尾

- [ ] `go build ./... && go vet ./inbound/... ./processor/... ./app/http/... ./app/agentruntime/...`
- [ ] `go test ./inbound/ssh/... ./outbound/proxy/ssh/... ./processor/config/ ./app/http/handlers/ ./app/http/routes/... ./app/agentruntime/ -race -count=1`
- [ ] `go test ./app/http/services/ -count=1`
- [ ] `cd app/website && npx vitest run && npm run build`
- [ ] 失败定性(预存 flake 对照 memory:services race flake/handlers notification flake/agent_ai 子进程超时)
- [ ] 冒烟清单(写给用户):仪表盘开 SSH 入口→保存→ssh -D 连入→流量经节点出口;指纹核对;mesh 凭证连入(待组网合并后端到端);**重启自愈**:重启 core(daemon/start/tray 三条启动路径任一)后已启用入口自动恢复监听(56441 或配置口),无需再保存一次配置;**入口所有权归 core 进程**——user-agent(agentruntime)不跑入口(其 mux 无 /api/config/customer 且非代理进程,注入会走未配置的出口 registry),双进程抢绑只会分脑;**合并后集成清单**:ForwardGate 接 mesh EffectiveForwardEnabled、dialByRoute 消费 IngressFrameMeta 续传、dns: 代解析启用
- [ ] 提交(若有散落)

## 任务依赖

T1 → T2 → T3 → T4 → T5 串行(包内递进);T6 独立可并行;T7 依赖 T2(Reconcile)与 T1;T8 依赖 T6(API);T9 最后。
