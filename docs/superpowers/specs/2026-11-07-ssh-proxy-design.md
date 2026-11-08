# SSH 代理协议设计(customer.proxy 新增 type=ssh)

日期:2026-11-07
分支:feat/ssh-proxy-outbound
状态:设计已获用户批准,待实施

## 背景与目标

客户自定义上游出口(`customer.proxy`)当前仅支持 `http` 与 `socks5` 两种协议。
本设计新增 `ssh` 协议:通过 SSH 服务器(密码或私钥认证)建立加密隧道,
流量经 SSH `direct-tcpip` 通道转发到目标地址——等价于 `ssh -D` 的 TCP 代理能力,
但由内核原生实现(不需要本地再起 socks 中转)。

### 已拍板的关键决策

| 决策项 | 结论 | 理由 |
|---|---|---|
| 认证方式 | 密码 + 私钥(含 passphrase)都支持 | 很多 VPS/跳板机默认禁密码登录,只支持密码覆盖不全 |
| host key 校验 | TOFU(首次信任) | 首连保存指纹,之后每次校验;指纹变更拒连。安全性与易用性平衡最好 |
| 连接复用 | 链 + 池(见 §3) | 每条流量零额外握手开销;池化摊带宽 |
| UDP 转发 | **不做,永久排除** | SSH 协议(RFC 4253 + OpenSSH)只有 TCP channel,协议层面不存在 UDP 通道,全球无实现;需要 UDP 出口是 socks5/hy2 的职责 |
| 跳板跳 | v1 实现 | 用户拍板全量纳入 |
| known_hosts 管理 UI | v1 实现 | TOFU 指纹冲突后的唯一自愈入口,强配套 |
| ssh:// URL 内嵌认证 | v1 实现(仅用户名+密码) | 私钥(PEM 数百行)无法也不应塞进 URL |

## §1 数据流(接入点不变)

```
仪表盘 RulesSettings(customer.proxy type=ssh)
  → PUT/POST /api/config/customer(routes.go:216,无 PATCH)→ 配置提交协调器
  → Registry.RefreshCustomerProxy() 注册名为 "ssh" 的 outbound
  → proxy_rules 按 "ssh" 名字选路 → SSH outbound 出流量
```

与 socks5/http 完全同构:SSH 是**原生 outbound**(实现 `proxy.Dialer` 直连目标)。
配置里 `egress.toSocks.upstream.type` 的「toSocks」是历史命名,实际语义是
「客户自定义上游出口」,枚举扩为 `[socks http ssh]` 即可。

## §2 Go 内核改动清单(11 处触点)

**⚠️ 本节是全量触点清单(spec 审查校准过)——少改任何一处都会造成
「配置存不进」或「流量静默回落直连」的隐性故障,实施计划必须逐条覆盖。**

### ① `outbound/proxy/proto/proto.go`

枚举**追加在末尾**(插中间会改变现有协议数值,影响兼容):
新增 `SSH` 常量,`String()` 返回 `"ssh"`。

### ② 新包 `outbound/proxy/ssh`(三个文件)

**`ssh.go` — outbound 主体**

```go
type SSH struct {
    *proxy.Base            // Address + proto.SSH
    cfg      Config        // user/password/privateKey/passphrase/poolSize/jumps
    hosts    *KnownHosts   // TOFU 指纹库
    mu       sync.Mutex
    pool     []*chain      // N 条独立链
}

func New(cfg Config, hostsDir string) (*SSH, error)  // 不连网,首次拨号懒建
```

- `DialContext`:从池中选「活跃 channel 数最少」的链 → 在该链 client 上
  `OpenChannel("direct-tcpip", {Addr, Port, "127.0.0.1", 0})`,channel 实现
  `net.Conn`。打开失败且检测到 client 已死时:摘除该链、换链重试一次。
- `DialUDP`:返回 `errors.ErrUnsupported`(协议限制,与全行业一致)。
- `Close()`:关全部链;registry 换代理时经现有 `closeProxyIfSupported` 自动调用。

**`chain.go` — 单链构建与自愈**

```
BuildChain() → *ssh.Client
  TCP 连 hop1 → SSH 握手(auth + TOFU)
  → 其上开 direct-tcpip 到 hop2 的 sshd 端口 → ssh.NewClientConn 覆盖其上
  → … → 最终 client(零跳 = 直连最终服务器)
```

- 链断线只重建该链(整链含跳板全部重连);全部链断时现建救急链。
- 同一链重建互斥(锁)+ 失败指数退避(封顶 30s),防止重建风暴打挂 sshd。

**`known_hosts.go` — TOFU 指纹库**

- 落盘 `cache.GetCacheSubdir("ssh")/known_hosts`,每行
  `host:port fingerprint algorithm added_at`,读写加锁、临时文件+rename 原子写。
- HostKeyCallback:查存储 → 无记录则写入放行;有记录不匹配则**拒连**,
  错误文案指明 host + 旧指纹,提示可能是服务器重装或中间人。
- 作用域:链上**每个节点**(跳板+终点)独立条目,key=`host:port`。

认证逻辑:密码(`ssh.Password`)与私钥(`ParsePrivateKey`,带 passphrase 时
先解密)同时配置则同时提供;私钥解析失败在 `New` 时即报错(fail-fast)。

### ③ `processor/config/types.go`

- `CustomerProxyConfig` 加字段:`private_key`(PEM 全文)、`key_passphrase`、
  `pool_size`(默认 3,钳位 1~16)、`jump_servers`(可选,按序穿透数组)。
- 新 `SSHProxyConfig{Server, ServerPort, Username, Password, PrivateKey,
  KeyPassphrase, PoolSize, JumpServers}`;跳板项
  `SSHJumpServer{Server, Port, Username, Password, PrivateKey, KeyPassphrase}`。
- `Validate()`:server/port 必填;密码与私钥至少其一;每跳独立校验
  (server/port/username 必填 + 认证至少其一);pool_size 钳位。
- `Config.Validate()` 的 customerProxy switch(types.go:987)加
  `case *SSHProxyConfig` 调其 `Validate()`——不加则 ssh 配置在提交时**静默跳过校验**。
- `validateCustomerEditableSurface`(types.go:1046)类型白名单
  `[http socks5]` → 加 `"ssh"`——不加则 type=ssh 的配置**根本保存不进**(特性死在门口)。
- **新增** `EffectiveSSHProxy()`;既有 `EffectiveCustomerProxy()`(types.go:1356)
  加 `case "ssh"` 分发到它。
- `validateCustomerProxyScheme` 加 `ssh`:URL 形态 scheme 必须为 `ssh`,
  用户名密码内嵌解析复用现有 `parseCustomerProxyServer`。

### ④ `outbound/registry.go`

- `RefreshCustomerProxy` 加 `case *SSHProxyConfig`:注册名为 `"ssh"` 的 outbound。
- `UnregisterCustomerProxies` 增加 `delete(r.proxies, "ssh")`。

### ⑤ `processor/config/routing_schema.go`

`egress.toSocks.upstream.type` 枚举 `[socks http]` → `[socks http ssh]`(两处校验)。

### ⑥ `processor/routing/snapshot.go`(resolveToSocksUpstreamType,:398)

类型 switch 加 `case "ssh": return "ssh", true`——不加则快照编译时
`ToSocks.Enabled=false`,所有客户代理流量**静默回落 direct**,测试全绿但线上是坏的。

### ⑦ `processor/tcp/handler.go`(两处)

- `currentCustomerProxyUpstreamType`(:1285):类型 switch 加
  `case "ssh": return toSocksUpstreamTypeSSH, true`(新常量 `= "ssh"`)。
- `getToSocksProxyForExecution` 的 upstreamType→proxyName switch(:1264):
  加 `case toSocksUpstreamTypeSSH: proxyName = "ssh"`——不加则 ssh 流量
  硬拒(`DenyReasonToSocksUnsupported`)。

### ⑧ `app/http/services/run.go`(bootstrapCanonicalRoutingSchema,:734)

类型白名单加 `"ssh"`,且 **upstreamType 必须显式映射为 `"ssh"`**
(现逻辑只对 http 覆写,ssh 若沿用默认值 `"socks"` 会与 registry 里的
`"ssh"` 条目错位,导致 `Get("socks")` 失败报 ToSocksUnavailable)。

### ⑨ known_hosts 管理 API(agent HTTP 服务)

遵循 routes.go + registerAgentRoutes 双注册约定,鉴权沿用代理配置端点同档;
handler 落在与 customer 配置同族的 service 文件(沿 routes.go:216 的
`/api/config/customer` 注册路径即可定位),新文件亦可:

- `GET /api/proxy/ssh/known-hosts` →
  `[{host, port, fingerprint, algorithm, added_at}]`
- `DELETE /api/proxy/ssh/known-hosts?host=X&port=Y` → 删单条
  (删终点不影响跳板条目)

## §3 前端(RulesSettings.vue + i18n)

- 类型下拉加 `<option value="ssh">SSH</option>`;默认 type 保持 `socks5`。
- `type==='ssh'` 时条件显示:
  - 用户名/密码框复用现有;
  - 私钥 textarea(placeholder 提示 PEM 全文粘贴);
  - passphrase 输入框(密码型);
  - pool_size 数字输入(默认 3);
  - 跳板列表编辑(可增删行,每行:server 文本框 + port 独立数字输入 +
    username + 密码或私钥,与主服务器字段形态一致);
  - 「服务器指纹」区块:指纹列表(host/port/指纹截断/添加时间)+ 单条删除 +
    「清空全部」,数据走上述 API。
- 表单校验:ssh 时密码与私钥至少填其一;每跳同理。
- i18n 按既有文案口径(「」、你、全角标点、术语统一)。

## §4 错误处理

| 场景 | 行为 |
|---|---|
| 指纹不匹配 | 拒连,文案含 host+旧指纹,提示重装/中间人可能;自愈=UI 删指纹重采 |
| 链断线 | 仅重建该链(含跳板),退避封顶 30s;其余链接流量,全断时现建救急链 |
| 认证失败 | 错误透传(含服务器返回的错误串) |
| sshd MaxSessions 满 | 错误透传,**不加重试路径**(§2 的换链重试仅限「client 已死」);缓解靠池的活跃数最少分发把负载摊开 |
| 私钥解析失败 | `New` 时 fail-fast,配置提交即报错 |
| 缺认证(密码私钥都空) | `Validate()` 配置提交时拦截,运行时不会出现 |

## §5 测试

可测性关键:`x/crypto/ssh` 自带 server 侧 API,测试内同进程起 loopback
内存 sshd(listener + `ssh.NewServerConn`),零外部依赖测全链路。

- ssh 包:零跳/单跳/双跳全链路;跳板认证失败;中途断链重建;池分发均衡性;
  单链断线不拖垮其他链;重建退避;TOFU 三态(首连写入/复连放行/换指纹拒连);
  密码与私钥认证;DialUDP 不支持;known_hosts 存储原子写
- config:EffectiveSSHProxy 分支;ssh case 路由;scheme 校验;Validate 拦截
  (缺认证/跳板缺认证/pool_size 越界)
- registry:ssh 分支注册与 Unregister 清理
- routing_schema:新枚举通过/旧值仍拒
- **运行时选路集成测试(spec 审查补充,防止「测试绿但流量回落 direct」)**:
  - snapshot 编译:type=ssh 时 `ToSocks.Enabled=true` 且 upstream=`"ssh"`
  - handler 选路:type=ssh 时 `getToSocksProxyForExecution` 解析到
    registry 的 `"ssh"` 条目(而非拒连或回落 direct)
- known_hosts API:列表/删除/未配置 ssh 时的语义
- 前端 vitest:条件字段显隐、校验、指纹区块操作

## 明确不做(YAGNI)

- UDP 转发(协议硬限制,永久排除)
- known_hosts 指纹预填/导入 known_hosts 文件
- SSH keepalive 可配置(先用 x/crypto 默认;实测有问题再加)
- compression/加密套件选择(交给协议默认)

## 已知限制(spec 审查立档,2026-11-08)

- **SSH 通道无 deadline 支持**:x/crypto 的 ssh.Channel 不支持
  SetReadDeadline/SetWriteDeadline(trackedChannel 只能 no-op)。relayStream
  依赖 SetReadDeadline 做的「半关闭后不无限等」保护对 SSH 出口**静默失效**
  (对端僵死且 TCP 不断时,拷贝协程可挂)——socks5/http/vless 返回真 TCP
  conn 有界,本出口是新增的不一致风险面。协议固有限制,与跳板 hop≥1 握手
  无 deadline 同源。候选缓解:通道级空闲看门狗/keepalive(与 AI 卡死硬顶
  工作族相关),留待实测有案例后立项。

## 影响面与兼容

- `proto` 枚举追加尾部,现有协议数值不变。
- `customer.proxy` 新字段全部 optional,存量 http/socks5 配置零感知。
- 路由 schema 枚举只扩不缩,存量 schema 校验不受影响。
- `golang.org/x/crypto` 从 indirect 升 direct(v0.48.0 已在依赖图)。
