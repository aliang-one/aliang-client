# mesh SSH 链真机冒烟清单(2026-11-12)

对应 SPEC `docs/superpowers/specs/2026-11-10-mesh-as-ssh-proxy-chain-design.md` §15.3(真机冒烟)与 §14.2(升级后必需人工动作)。本清单给出**两台设备(记为 A=发起方/出口消费方,B=对端/出口提供方)**从同网段起步的逐步操作与判定。

前置阅读:《组网(mesh)收敛为 SSH 代理链——升级说明(2026-11-12)》(本目录 `2026-11-12-mesh-ssh-chain.md`)。

## 通用排查工具(所有步骤共用)

- `GET /api/mesh/status`:本机 `device_id`、peers 可达性、**`effective` 字段**(编译后真实生效值)、peers[].host_key 回读。**判定口诀:规则在但 `effective` 里没有 = 密钥没填齐**(§14.2 第 2/3/6 步漏项),不会报错、不会弹窗。
- B 侧 debug 日志(需开 debug 级):`SSH ingress mesh-exit injected conn_id=… from=<A-deviceID> target=<host>:<port>` 是注入成功的直接证据;`SSH ingress mesh-exit rejected (…)` 是注入被拒。
- A 侧连接日志:命中 mesh 规则的连接带 `Route=RouteMesh`;出口链路委托带 `Route=RouteMeshDelegate`,据此确认命中面与委托面。

## 准备:双机升级 + 七步密钥动作

1. 两台机器**全节点同步升级**到同一版本(新旧不互通:只升一半时 LAN 侧静默断连、piko 侧 SSH 握手失败)。升级完成后 `/api/mesh/status` 中对端仍不可达 + 对端已是新版 = 只升了一半。
2. 逐台执行升级说明的「升级七步」(§14.2):开 `ssh_ingress` → 本机 mesh 身份密钥对(`customer.mesh.private_key` + 公钥带 device_id 尾注释进对端 `ssh_ingress.mesh_authorized_keys`)→ 互填对端 SSH 入口公钥到 `peers[].host_key`(来源=对端仪表盘「SSH 入口」卡片指纹)→ `lan_addrs` 端口确认 **56432** → 跨 NAT 对端 `relay=true` + 平台放行 → `fallback.addr` 改填对端 `IP:56432` + 新增 `fallback.host_key` → 前端含密钥输入位(否则手改配置文件)。
3. 验收基线:两台各自 `/api/mesh/status` 中对端可达、`effective` 包含预期规则。**任一机器不过基线,不要进入冒烟**——以下每步的失败都可能只是密钥没填齐。

---

## 冒烟 1:同网段 LAN 出口(SPEC §15.3 主场景)

**配置(A 侧)**:规则加 `x.com,mesh:<B-deviceID>`;A、B 同网段,B 的 `lan_addrs` 填 B 的 LAN IP:56432。

**操作**:A 侧发起一次指向 x.com 的 AI 流量与一次自身流量(浏览器访问 x.com)。

**预期现象**:
- A 侧连接日志:目标 x.com 的连接 `Route=RouteMesh`,规则命中;
- B 侧 debug 日志:出现 `SSH ingress mesh-exit injected … from=<A-deviceID> target=x.com…`;
- B 侧仪表盘连接记录:可见来自 mesh 注入的连接(Route=RouteMesh);
- AI 流量与自身流量**两条都**经 B 出口(SPEC §15.3:验证 AI 流量与自身流量经对端出口)。

**若失败的排查锚点**:
1. A 侧日志无 Route=RouteMesh → 规则没进 `effective`:`/api/mesh/status` 核对 `effective`,没有 = host_key/private_key 缺项或 `mesh:` 段形态不合法(整条静默跳过);
2. A 侧命中但 B 侧无 injected 日志 → 传输层未建:B 核对 `ssh_ingress.enable=true` 且 56432 监听;A 侧 `lan_addrs` 是否填的 B LAN IP(不是 localhost/专用口);B 侧 `mesh_authorized_keys` 是否含 A 的公钥(带 device_id 尾注释);
3. B 侧出现 `mesh-exit rejected` → 凭证形态不匹配:该公钥是否被 bare 形态消费(尾注释丢失会掉进 bare);
4. host key 冒充路径:B 换过 SSH host key 而 A 的 `host_key` 未同步 → A 拒连,更新 A 侧 `peers[].host_key`。

## 冒烟 2:形态隔离(mesh 凭证开 direct-tcpip 被拒)

**配置**:B 侧不动配置(仍保留 A 的 mesh 公钥)。

**操作**:用 A 的 **mesh 身份私钥**当普通 SSH 客户端凭证,从 A(或任一机器)对 B 的 56432 发起 SSH 连接并请求 **direct-tcpip** 通道(标准 `ssh -L`/`-W` 语义)。

**预期现象**:
- B 拒绝该通道:通道请求在 Accept 前被 `Reject(Prohibited, "channel not allowed for this credential form")`,A 侧 SSH 客户端表现为通道打开失败/连接不可用;
- B 侧 debug 日志:形态观测留下 form=mesh / deviceID=<A> 的审计痕迹(先观测后拒绝的次序,不会无痕静默);
- B 侧**不产生**任何经 direct-tcpip 的代理流量。

**若失败的排查锚点**:
1. 通道居然打开 = 形态隔离失效,立即停用并回查 `inbound/ssh/server.go serveChannels` 矩阵与版本是否为本次发布内容;
2. 连 SSH 认证都进不去 → 不是本项失败,是密钥/authorized_keys 问题,回升级七步第 2 步;
3. 想看拒绝证据但日志没开 debug → 提级后重放(拒绝日志是 Debug 级)。

## 冒烟 3:DNS 代转发(`dns,mesh:<peer>`)

**配置(A 侧)**:规则加 `dns,mesh:<B-deviceID>`(尾段语法与失败策略同族,如 `dns,mesh:dev-b,fallback:dev-c`)。

**✅ 已接线(2026-11-12)**:TUN UDP/53 的门控按规则驱动——首条启用 `dns,mesh:<id>` 规则 + peer 载体可用(LAN 地址存在)即激活,A 侧查询经 mesh 由 B 侧代解析;**relay-only peer(无 LAN 地址)的 dns 规则不激活**(TUN DNS 恒 custom 类、Piko 中继拒 custom——此时 fail-open 走本机 DNS,绝不 SERVFAIL 风暴);门未激活/编译失败同样 fail-open。

**操作**:A 侧发起域名解析(访问任一新域名),观察 A、B 两侧。

**预期现象**:
- 规则解析成功:`/api/mesh/status` 的 `effective` 含该 dns 条目;
- B 侧出现 `SSH ingress mesh-exit injected … target=dns…` 日志,A 侧域名解析结果正常返回;
- 查询经 mesh 但 B 侧解析失败 → A 侧收到 SERVFAIL(fail-closed,不静默回落公网 DNS)。

**若失败的排查锚点**:
1. `effective` 里没有该条 → `dns` 目标段形态不合法(参照 routing 测试矩阵:`dns,mesh:<id>` 大小写前缀不敏感、deviceID 保留大小写、段内空白容忍),检查逗号段拼写;
2. 整条规则被静默跳过 → 多段尾策略语法错误(mesh: 之后只允许恰一个失败策略段,多余段整条作废),或 peer 未钉扎/缺节点私钥(编译期能力闸静默跳过——查保存时是否报错);
3. peer 只有 piko 中继(无 LAN 地址)→ 门不激活属预期(fail-open),A 侧照常解析;
4. B 侧有 injected 但解析失败 → B 侧 `resolveDNSQuestion` 类型覆盖面(仅 A/AAAA 族),查 B debug 日志错误行。

## 冒烟 4:跨 NAT(piko relay)

**配置**:把 B 挪到不同网段/不同 NAT(或模拟:A 与 B 之间 LAN 不可达)。B 侧确认 `peers[].relay=true`、平台已放行 `<deviceID>.mesh` 且已下发 `piko_proxy_url`。

**操作**:重复冒烟 1 的 A 侧流量。

**预期现象**:
- A 侧日志:LAN 不可达时降级 piko,`Route=RouteMesh` 仍成立(SPEC:LAN 优先、LAN 全失败才降级 piko);
- B 侧:piko 载体连入后同样出现 mesh-exit injected 日志;
- 若 A、B 间 LAN 实际可达:**应走 LAN 不走 piko**(降级判定成立才算通过);要单测 piko,先人为断开 LAN 路径。

**若失败的排查锚点**:
1. 完全不通 → 平台侧未放行 `<deviceID>.mesh` / `piko_proxy_url` 未下发(B 侧 `/api/mesh/status` 与 agent 日志核对);
2. B 侧 WARN 记账「piko 连入但 ingress 未运行」 → B 的 `ssh_ingress` 未开或代理停了(统一口形态下 mesh 入站随代理运行态启停);
3. 降级后偶发失败 → piko 载体首包握手超时(A 侧 deadline 保护),重试应恢复;持续失败查平台 relay 通道。

## 冒烟 5:出口链路(开关驱动,常驻委托——不再断平台验证)

**配置(A 侧)**:组网卡「出口链路」卡开关打开、下一跳选 **B**(B 须已在 peers 且已钉扎 host_key;地址与指纹从 peers 条目继承,不再单独填)。可选:配置一条规则 `x.com,mesh:<B>` 验证规则路径零变化。

**操作**:直接发起 AI 对话(x.com 规则流量与 AI 流各来一次),**A 不需要断平台**——开关=无条件委托,在线也走 B。

**预期现象**:
- A 侧 AI 流日志:`Route=RouteMeshDelegate`(委托给 B 出 B 的隧道);规则流量照旧 `Route=RouteMesh`(开关不影响规则路径);
- B 侧:mesh-exit injected 日志照常出现,AI 流在 B 就地处理(RouteToALiang 走 B 的隧道);若 B 也开了开关且下一跳是 C,则 B 日志再出现一次 `Route=MeshDelegate`(链式推进),C 侧落地;
- A 关掉开关:AI 流回归 `Route=RouteToALiang`(A 自己的隧道),零残留。

**若失败的排查锚点**:
1. 委托必不通 → B 的 `host_key` 缺失/过期(空=fail-closed,开关开了也不生效;A 组网卡下一跳下拉会标「未绑对端指纹」),取 B「SSH 入口」卡片指纹更新 peers 条目;
2. 开关开了但 AI 流仍走本机隧道 → 检查 `fallback.enable` 是否真的落盘(`/api/config/customer` 回读 mesh.fallback.next_hop 非空);
3. 链上第二跳不动 → 中间节点未开开关(链在那台就地终止,属预期);或 `max_hops` 已到上限(经停链长,默认 5)。

---

## 收尾

全部五步通过后:两台 `/api/mesh/status` 对端可达、`effective` 与预期一致,记录两侧版本号与本清单执行日期。任何一步失败,按该步排查锚点定位后重跑该步(不做全量静默重跑)。
