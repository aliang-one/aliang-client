# 设备间文件互传(同账号)设计

- 日期:2026-10-25
- 状态:已获用户批准的设计稿(待实现)
- 范围:alianggate(设备端)`transfer.*` 协议与收发 handler + PhoneServer(服务端)transfer 编排;产品入口=手机 App / 设备仪表盘 / AI 会话三处(入口 UI 细节随各端自行出)
- 不改动:既有 `file.upload`(手机从设备下载)链路、AI 附件下载器现有行为、aliang-official-website(门户/计费,与本功能无关,已核实非控制面)
- 调查依据:2026-10-24 九路并行代码调查(6 路只读测绘 + 3 路对抗复核),关键论断均带 file:line 证据

## 1. 背景与动机

用户目标:**同一个账号下的多台设备之间,可以互相传输文件和文件目录**(目录先压缩传输再解压)。这个基础能力先落地,再包装到三个产品入口上;内网直连(同内网不走公网)是明确功能点;中继节点/自定义网络路线若有开源方案再规划,暂不引入。

文件规模预期:**几百 MB ~ 2GB**。这决定了:断点续传(上传分块 + 下载 Range)必须进第一阶段设计;LAN 直连优先级相应提前(2GB 走公网 COS 中转的流量成本与耗时会真实可见)。

## 2. 现状盘点(调查结论)

### 2.1 已有的两条文件腿,方向恰好互补

| 方向 | 现状 | 对互传的意义 |
|---|---|---|
| 设备→云端 | `file.upload`(`app/http/services/agent_file_upload.go`):平台经 WS 下发 COS 预签名 PUT URL,设备流式直传 COS,server 字节零经手。带进度节流(5%/1s,`:150-167`)、取消(`:224-233`)、max_bytes 复核(`:116-131`,0=不限)、错误脱敏剥预签名 URL(`:90-97`)。单设备单飞 1 槽(`:66-75`) | **互传发送侧内核直接复用,几乎零改动**;但它是 server→agent 推送编排,设备不能主动发起 |
| 云端→设备 | 仅 AI 附件下载器(`app/http/services/agent_ai_attachment_download.go`):https-only + host 白名单(空则拒,`:70-73`)+ 单文件 20MiB 硬顶(`:37`)+ 2min 超时(`:38`)+ 每跳重定向复检(`:277-289`)。安全边界集中在 `writeAttachmentPayload`(`:249-309`),**输出是 io.Writer 抽象** | 内核可整体复用;但落盘只两种变体(临时目录随 run 删 / TUI 缓存哈希改名+7 天 TTL),都不适配互传,需第三种落盘变体;20MiB/2min 硬编码须参数化 |

### 2.2 关键架构事实(对抗复核确认)

1. **WS 搬不动文件字节**:纯 JSON 文本帧(`agent_remote_ws.go:417-422` ReadJSON/WriteJSON)、写超时 10s、读窗 90s(`:389-410`)、`file.read` 事实载荷上限 512KiB(`agent_detail.go:25`)。互传必须「URL 引用 + 两端各自 HTTP 拉」,COS 中转正是现有形状。
2. **控制面在 PhoneServer**(`~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`,TypeScript):设备注册表(`modules/device/register.ts` + devices map)、WS 网关(`index.ts:460` `/ws/agent`)、**按 device_id 定向推送 `publishToAgent`**(`shared/realtime/publish.ts:66-86`)、COS 预签名全套(`modules/agent/fileDownload.ts`)、设备列表 `GET /api/devices`(`routes/devices.ts:143-147` → `listUserDevices`)全部现成。缺的只是 transfer 登记/状态机/设备向收件 API。
3. **权限模型不用动**:设备持用户 JWT(内存 forwarded,owner 经本地 `/api/agent/sync` 注入)+ 客户端永久 `device_id`(`device_identity.json`,`agent_service.go:1418-1431`);`callAgentServer`(`agent_service.go:1799-1862`)是通用带凭据 HTTP 客户端;PhoneServer 鉴权纯用户级(`requireUserId` 仅看 JWT 解出的 user)——设备天然拥有与手机同级的 API 调用权。
4. **设备主动发起有现成模式**:`tool_ask.request/response` 按 `interaction_id` 幂等关联(`agent_ai_tool_ask.go:182,278` 发起;`:126,156` 回投),`ai.option.request/response` 同理——「agent 发起 + 关联等待」可照抄,不必从零发明。
5. **设备列表客户端已半接线**:`GetAgentDevicesURL`(`processor/config/types.go:755-757`)已定义、指向 PhoneServer 实际存在的 `GET /api/devices`,全仓零调用——接通即得同账号 peer 列表。
6. **发送端路径围栏已被运营移除**(`agent_detail.go:448-480`「confinement REMOVED」,任意可达路径可读):**接收端照抄=任意路径写,必须重建围栏**(见 §9)。
7. **已知局限要兜底**:设备断线后迟到的 `file.upload.result` 无法匹配、看门狗 2 分钟误报(手机下载 spec `2026-09-21-cos-file-download-design.md` §4.2 自述)——互传以服务端状态对账为准,不依赖 agent 补投(§10)。
8. **契约登记有过漏登记先例**:`file.upload.progress/cancel` 在用但未进 `agent_protocol.go` 的 ClientSends/ServerSends 契约清单——transfer 事件族必须一次登全。
9. **双注册教训**:本地 HTTP 端点必须同时注册 core 侧 `app/http/routes/routes.go` 与 runtime 侧 `app/agentruntime/server.go`(缺一处 404,2026-10-21 liang-dev 实测)。
10. **版本协商材料就位**:`agent.hello` 带 capabilities(`agent_remote_ws.go:1018-1040`、`:1122-1135`);未注册的新下行事件回 `unsupported`(`:885-888`)——两端都要升级才能互通,服务端按能力位门控入口。
11. **白名单语义**:现有附件白名单 `normalizeAttachmentAllowedHosts` 剥端口只比 hostname(`agent_ai_attachment_download.go:385-412`),且下载器会尊重 `HTTPS_PROXY` 等代理 env(裸 `&http.Client{}`)——transfer 通道两侧都要显式考虑代理 env;白名单不复用 attachment 语义,新通道自行钉 COS 域(§9)。

## 3. 目标 / 非目标

### 目标(P1)

- 同账号任意两台在线(或事后补投)设备之间互传单文件,≤2GB(服务端可配)。
- 目录传输:发送端 zip 打包 → 单文件传输 → 接收端自动解压。
- 断点续传:上传分块(COS Multipart,单块失败只重传单块)、下载 Range。
- 三入口汇入同一底层:手机 App「同步到设备」、设备仪表盘、AI 会话(工具)。
- 接收文件落在固定收件目录,不覆盖已有文件,完成有通知。

### 非目标

- 不做接收审批流(同账号=信任域,静默接收+通知;后加可演进)。
- 不做 P1 内的 LAN 直连实装(留能力位,P2 实装,§11)。
- 不引入 mesh/中继依赖(Tailscale/Headscale/netbird 等,P3 评估,§11)。
- 不改既有 `file.upload`(手机下载)与 AI 附件链路的行为。
- 不做文本流式/增量同步(那是配置同步与「目录持续同步」的事,不在本 spec)。

## 4. 总体架构

```
设备A(打包zip+分块)──PUT 分块──> 腾讯云 COS <──GET Range──── 设备B(校验+解压)
     │                              │                              │
     └── WS: transfer.upload 信令 ──┤                              │
                                    PhoneServer(transfer 状态机)    │
        WS: publishToAgent(定向推送 transfer.receive)───────────────┘
        HTTP: 设备凭 JWT 主动拉收件箱/对账重签(callAgentServer)
```

- **字节面**走 COS 预签名(WS 承载不了,见 2.2-1);**信令面**走现有 `/ws/agent`。
- 三入口全部汇到 PhoneServer 的 transfer API;PhoneServer 是唯一编排方。
- 目标设备离线:COS 暂存(保留至 confirm 或 7 天兜底),设备上线后 WS 重连/收件箱拉取补投。

## 5. 协议设计(transfer.* 事件族)

照 `file.upload` 四件套模式(协议常量+契约条目+`handleRemoteAgentMessage` switch 臂+`remoteAgentMessageRequiresEnabledDevice` 门禁),并**一次登全契约清单**(吸取 file.upload.progress/cancel 漏登教训,2.2-8)。

### Server→Agent

```jsonc
// 发送指令:设备 A 打包后分块直传 COS
transfer.upload {
  request_id:  string    // RPC 关联(服务端→agent 方向,同 file.upload 模式)
  transfer_id: string    // 传输单 ID,全程贯穿
  filename:    string    // 展示名(目录则为 xxx.zip)
  size_bytes:  int64     // 服务端登记的期望大小
  sha256:      string    // 发送端打包产物哈希
  max_bytes:   int64     // 服务端门控(超限 agent 侧拒发,0=以服务端为准)
  is_directory_archive: bool
  upload: {              // COS Multipart
    key:        string   // tr/{userId}/{transferId}/{filename}
    upload_id:  string
    part_size:  int64    // 64MiB(2GB/64MiB=32 块,预签名 URL 一次性签发可承载于单条 WS 消息)
    parts: [{ part_number: int, url: string }]   // 预签名 PUT,过期走 §10 对账重签
  }
  source: { device_id, device_name }   // 展示用
}

// 接收指令:设备 B 拉取落盘
transfer.receive {
  request_id, transfer_id, filename, size_bytes, sha256,
  is_directory_archive: bool,
  get_url:     string    // 预签名 GET(支持 Range,过期重签)
  target_dir:  string    // 收件子路径(inbox 根下,设备端仍做围栏校验)
  max_bytes:   int64     // 接收端 too_large 兜底闸(设备端在线上值缺失时另落本地常量)
  object_etag: string    // If-Range 对象身份绑定(续传防对象被换)
  source: { device_id, device_name }
}

transfer.cancel { transfer_id }   // 仅 Server→Agent;取消发起一律走 POST /api/transfers/{id}/cancel(见下),agent 不设取消上行 WS 消息
```

### Agent→Server

```jsonc
transfer.progress { request_id?, transfer_id, direction: "upload"|"receive", bytes_done, total_bytes, part_number? }
// request_id 可选:RPC 在飞时携带以关联当次请求;断线重发/对账触发等场景只带 transfer_id 亦可。
transfer.result   { request_id, transfer_id, ok: bool,
                    etags?: [{ part_number, etag }],   // 上传完成必带,服务端据此 CompleteMultipartUpload
                    path?, size_bytes?, sha256?,        // 接收完成必带(端到端确认)
                    error_code?, error? }               // 失败时;错误消息先经脱敏(剥预签名 URL)
```

### 取消的发起(HTTP,不经 WS)

取消意图的**唯一入口是 HTTP**:手机、源设备、目标设备任一方凭归属(user_id 校验)调

- `POST /api/transfers/{id}/cancel` — 服务端更新状态机置 failed(取消)、清理 COS 对象(见 §7.2),并向在飞端下发 `transfer.cancel`。agent 侧不设取消上行 WS 消息,避免与 HTTP 双源竞争。

### 设备主动拉取(HTTP,不经 WS)

- `GET /api/transfers/inbox` — 当前设备的待收列表(callAgentServer 现成通道);**条目携带等价于 `transfer.receive` 的全部字段**(filename/size_bytes/sha256/is_directory_archive/target_dir/source 等,get_url 过期走 download-url 重签)。inbox 不只是通知,可独立驱动接收——离线补投场景(WS 信令从未到达)依赖这一点。
- `POST /api/transfers/{id}/confirm` — 接收完成回执(与 `transfer.result` 双通道并存,HTTP 为准,规避断线丢 result,§10)。
- `GET /api/transfers/{id}/parts` 与 `GET /api/transfers/{id}/download-url` — 两个独立端点,分别对账重签分块 PUT 与下载 GET URL(照 `GET /api/projects/:projectId/files/download/:requestId` 重签先例做设备向版本);`GET /api/transfers/{id}` 只查状态不重签。角色校验:`parts` 仅源设备、`download-url` 仅目标设备可调(同账号信任域下的卫生约束,按 transfer 归属校验)。

### 能力协商

- `agent.hello` capabilities 新增 `file_transfer_v1`(`agentCapabilities`,`agent_remote_ws.go:1122-1135`)。
- 服务端对未上报能力位的设备不发 transfer.* 下行(老版本会回 unsupported 并可能 forceDisconnect);入口 UI 同样按能力位屏蔽。

## 6. 设备端设计(alianggate 增量)

### 6.1 新增/改动文件

| 文件 | 内容 |
|---|---|
| `app/http/models/agent_protocol.go` | `transfer.*` 事件常量 + 契约清单条目(Server/Client 两侧一次登全) |
| `app/http/services/agent_transfer_upload.go`(新) | 发送 handler:resolve 路径→(目录则 zip 打包)→分块 PUT 复用 `file.upload` 内核(流式/进度节流/sanitizeUploadError/ResponseHeaderTimeout 模式)→收集 etag 回 result |
| `app/http/services/agent_transfer_receive.go`(新) | 接收 handler:第三种落盘变体(§9.1)+ Range 续传 + sha256 校验 + (目录则解压,zip-slip 防护) |
| `app/http/services/agent_transfer_inbox.go`(新) | 收件箱拉取/confirm/重签的 HTTP 客户端(callAgentServer) |
| `app/http/services/agent_remote_ws.go` | switch 臂 ×3(transfer.upload / transfer.receive / transfer.cancel)+ enabled-device 门禁登记(`transfer.cancel` 与 `file.upload.cancel` 同款豁免门禁) |
| `processor/config/types.go` | transfer API URL 构造器;P1 仅加构造器,设备列表调用不接线(实际接线在 P2 的 AI 工具/仪表盘入口) |
| AI 工具注册(P2) | `transfer_file` 工具暴露给模型(AI 会话入口) |

### 6.2 并发与生命周期

- 上传/接收**各 1 个独立槽**(不动 `file.upload` 原单飞);第二请求按 request_id 回明确错误(同「another file upload is in progress」模式)。
- cancel 按 transfer_id 匹配在飞 job;不匹配为 no-op;不经 enabled-device 门禁(照 `file.upload.cancel` 先例)。
- **幂等去重**:WS 定向推送与 inbox 拉取可能重复投递同一 transfer——设备端按 transfer_id 去重:已在收的任务忽略重复信令;已收完成的重复投递直接补发 confirm(自愈)。
- **接收槽被占**:回 `transfer.result{ok:false, error_code: receiver_busy}`;服务端保留 ready 态按指数退避重推(封顶 1 小时,达封顶后不再主动重推、依赖目标设备每小时 inbox 拉取兜底),**不置 failed**(两台手机先后向同一设备同步是 P1 常态场景)。
- **在飞中收到 cancel**(`receiving` 态):目标设备中止下载并清理 `.partial-*`;服务端此后收到的该 transfer_id 迟到 `transfer.result` 按 no-op 丢弃(状态机已终态)。
- **收件兜底**:WS 重连成功后与低频定时(默认 1 小时)各拉一次 inbox,防止「ready 后目标设备长期不重连」导致传输卡到 7 天过期。
- `.partial-*` 临时文件+rename 原子写(照 TUI 缓存变体);进程重启后 .partial 保留供续传,超 7 天清理。

## 7. 服务端设计(PhoneServer 增量)

### 7.1 transfer 登记(新表/存储)

`transfers`:user_id、transfer_id、source_device_id、target_device_id、filename、size_bytes、sha256、is_directory_archive、cos_key、target_dir(服务端按 filename 派生默认值,供 receive 信令与 inbox 条目一致)、state、created_at/updated_at。

### 7.2 状态机

```
pending(发起登记,返回分块计划)
  → uploading(收到首个 progress)
  → ready(收到 result.etags,服务端 CompleteMultipartUpload 成功)
  → receiving(publishToAgent(目标) 已推送 / 目标已开始拉)
  → done(目标 confirm,服务端 DeleteObject)
failed(sha 不匹配/取消/超限) / expired(7 天兜底清理)
```

**清理语义**:done → DeleteObject;**cancel → 立即清理**(未 Complete 的先 AbortMultipartUpload 再删,已 ready 的直接 DeleteObject);sha 不匹配/超限 → 同 cancel 即删;expired → 生命周期兜底。除 done/cancel/expired 三条确定路径外,不存在「对象滞留」的中间态(receiver_busy 重推期间对象保留是 ready 态的正常语义)。

### 7.3 API(设备向与手机向同源)

- `POST /api/transfers`(发起:指定 target_device_id;`source_device_id === target_device_id` 拒绝(自传保护);手机入口传已在 COS 的对象则服务端 `CopyObject` 到 transfer key 直接置 ready;设备入口返回分块计划)
- `GET /api/transfers/inbox`、`POST /api/transfers/{id}/confirm`、`POST /api/transfers/{id}/cancel`、`GET /api/transfers/{id}`(对账)
- 设备向调用一律 `requireUserId` + 校验 `device.userId === JWT user`(现有 guards 模式);inbox 仅目标设备可见。

### 7.4 通知与补投

- `ready` → `publishToAgent(target_device_id)` 推 `transfer.receive`(现成管道)。
- 目标离线:上线重连后凭 JWT 拉 inbox(设备主动拉取,`tool_ask` 关联模式可照抄的点在于 WS 侧;HTTP 拉取更省)。
- 对象保留:confirm 即删;7 天生命周期兜底(现桶 1 天策略对互传不够,互传 key 前缀 `tr/` 单独配)。

## 8. 三入口映射

| 入口 | 触发 | 链路 |
|---|---|---|
| 手机 App(P1) | 长按文件/「同步到设备」选目标 | 文件已在 COS(现有手机下载链路)→ `POST /api/transfers` CopyObject → 直接 ready → 目标设备收 |
| 设备仪表盘(P2) | 56431 UI 选文件/目录+目标设备 | agent 本地打包→agent 凭 JWT 调 `POST /api/transfers`(callAgentServer)→ 收 `transfer.upload` 信令走标准发送链 |
| AI 会话(P2) | 模型调用 `transfer_file` 工具 | 同仪表盘链路;工具参数 file path + 目标设备名/ID(设备列表接通 `GetAgentDevicesURL`) |

入口 UI 细节不在本 spec 展开(各端自行出);本 spec 锁定的是三者共用的底层协议与语义。

## 9. 目录传输与安全围栏

### 9.1 收件落盘(第三种落盘变体)

- 收件根 = 登录桌面用户 home 下 `AliangInbox/`(Linux root agent 场景走 EffectiveAgentHome 解析登录用户家目录,防落 `/var/root`——TUI 缓存同款教训);env `ALIANG_TRANSFER_INBOX_DIR` 可覆盖(照 `ALIANG_ATTACHMENT_CACHE_DIR` 先例)。
- 目录 0755、文件 0644(root agent 写、登录用户可读)。
- **固定收件目录、保留原名**;重名不覆盖,自动追加序号 `(1)`、`(2)`。
- 与现有两种变体(临时目录随 run 删 / 哈希改名+7 天 TTL)并存,互不影响。

### 9.2 围栏(接收端必须重建,发送端围栏已移除)

- 落盘只允许 inbox 根内;`target_dir` 规范化后做前缀校验,逃逸即拒(`transfer.result ok:false`)。
- **zip-slip 防护**:解压条目路径清洗,拒绝 `..`、绝对路径、符号链接条目;解压根强制在目标目录内(`extractZipArchive` 前缀检查有先例,`wintun_dependency_windows.go:372-375`)。
- zip 大小闸:解压后累计字节数超过 `size_bytes × 预留系数(默认 4)` 即中止(防 zip 炸弹)。

### 9.3 端到端完整性

- 发送端:打包产物 sha256(打包时顺手算)+ size 上报,登记进 transfer。
- 接收端:边下边算 sha256,与信令比对;不匹配删成品、`error_code: sha_mismatch`,服务端置 failed 并通知源设备。
- 缓存变体现状 sha256 只用于去重不回传——transfer 变体把透出做进返回值(改造成本≈0,复核已确认)。

### 9.4 上行目标钉死

- `transfer.upload` 的分块 PUT 目标 host 钉 COS 域(设备端校验,纵深防御;现状 `file.upload` 对 upload_url 无 host 校验是可接受的历史,但互传把上行常态化,须收紧)。
- 错误消息统一过 `sanitizeUploadError` 模式脱敏(剥预签名 URL/签名参数)。

## 10. 续传与错误处理

- **上传**:单块失败只重 PUT 该块;分块 URL 过期走 `GET /api/transfers/{id}/parts` 重签;全部块完成后 result 带 etags,Complete 由服务端做。
- **下载**:`.partial` + Range 从已收偏移续传;GET URL 过期对账重签;2GB 公网链路上不做整文件重来的分支。
- **断线丢 result**(已知局限,2.2-7):服务端以 COS HeadObject(size/etag)与状态机对账为准;confirm 走 HTTP 双保险;不依赖 agent 补投迟到 WS 消息。
- **代理 env**:发送/接收 HTTP client 与现有两侧一致会尊重 `HTTPS_PROXY`——文档明示,不特殊处理(LAN 直连 P2 时再处理直连语义)。
- **进度语义**:沿用 5%/1s 节流;接收方向补齐 request_id 维度(现有下载侧完全没有进度事件)。

## 11. LAN 直连(P2)与 mesh(P3)占位

- **P2 LAN 直连**:同账号设备 mDNS 发现(`_aliang-transfer._tcp`,实例携带 device_id+账号指纹)→ 直连 TLS + 账号 token 校验互认 → 字节面直传,失败自动回退 COS。信令面不变(仍由 PhoneServer 编排,`transfer.receive` 里多带 `direct_candidates`)。细节 P2 单独 spec;capabilities 预留 `direct_v1` 位。
- **P3 mesh/中继**:开源方案已确认存在(Tailscale/DERP、Headscale、netbird、libp2p circuit relay),届时按运维成本与产品形态评估接入;P1/P2 不引任何依赖。

## 12. 分期

- **P1 核心链路**:transfer 协议四件套+收发 handler+收件目录+zip/解压+分块续传+PhoneServer 状态机/multipart/inbox/confirm;手机 App 长按「同步到设备」入口先行(文件已在 COS,改动最小)。
- **P2 入口铺开**:设备仪表盘互传页、AI `transfer_file` 工具(接通设备列表);LAN 直连实装。
- **P3 远期**:mesh/中继评估;接收审批(如需)。

## 13. 测试策略

- **设备端单测**:zip 流式打包/解压往返;zip-slip 全拒绝矩阵(`..`/绝对路径/symlink);Range 续传(中断→恢复→sha 一致);单块重传;inbox 围栏逃逸拒绝;单飞/取消/进度单调;sha_mismatch 分支;接收槽被占回 receiver_busy;WS+inbox 双投递幂等去重;协议契约测试(常量+契约条目+switch 臂+门禁四点齐,防漏登记);错误脱敏。
- **跨平台注意**:测试名避开 GOOS 尾段(`_windows_test.go` 在 darwin 静默跳过的既有陷阱);新测试先 `-list` 确认真实执行。
- **服务端**:状态机流转全分支;multipart complete;inbox 权限(非目标设备 404 掩蔽);对象清理(confirm 删/cancel 即删/7 天兜底);cancel API 全链路(归属校验→状态置 failed→在飞端下发→对象清理);自传拒绝;receiver_busy 退避重推。
- **e2e**:liang-dev 双设备(或同机双 agent 模拟)互传文件+目录;离线补投场景;URL 过期重签场景。

## 14. 开放问题

- 手机端长按菜单的交互细节(手机端自行出,不阻塞 P1 后端)。
- LAN 直连的配对/互认凭据细节(P2 spec)。
- 收件目录名 `AliangInbox` 是否产品化定名(P1 可先用,改名成本低)。
- AI 工具的授权边界(P2:模型可发起互传的范围是否需要用户确认一次,可复用 tool_ask 模式)。
