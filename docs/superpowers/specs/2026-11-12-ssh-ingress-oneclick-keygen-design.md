# SSH 入口一键生成并启用设计(keygen 端点 + 全自动引导流)

日期:2026-11-12
分支:feat/ssh-ingress-oneclick-keygen(基于 master ed88ec2 = v1.1.108)
状态:设计已获用户批准,待实施计划
前置:SSH 入口已随 v1.1.100–v1.1.108 全量上线(spec 2026-11-08-ssh-ingress-design.md / 2026-11-10-mesh-as-ssh-proxy-chain-design.md)

## 0. 一句话

给 SSH 入口卡加「一键生成并启用」:一次点击完成后端密钥对生成 → 公钥入列 →
自动开启并保存 → 私钥弹窗展示+下载,把新装用户的启用路径从「懂 ssh-keygen、
懂公钥概念」压到「点一个按钮」。

## 1. 背景

2026-11-12 在 0.196(Windows,v1.1.108)诊断「SSH 总是失败」定案:

- 后端 400 闸门(`ssh_ingress requires at least one authorized key when
  enabled`)是**有意的 fail-closed**:无钥匙开门=谁也进不来。不动它。
- 真缺口在前端:总开关在**头部常驻可拨**(折叠态也能拨),而「至少一把公钥」
  红字提示、公钥粘贴框、保存按钮全在**默认折叠的正文里**;头部零提示、
  无展开联动。新装用户拨开关 → 界面毫无反应 → 感知为「打不开/bug」。
- 本设计只降**人用钥匙(形态①)**的心智门槛;mesh 互连钥匙(形态②)的
  生成与分发归组网页配对流程,不在本期。

## 2. 已拍板决策

| 决策项 | 结论 | 理由 |
|---|---|---|
| 一键深度 | **全自动到可用**:生成→公钥入列→开启+保存→私钥交付,一次点击 | 用户拍板;门槛降到零 |
| 生成位置 | **后端端点**生成 ed25519 | WebCrypto ed25519 导出残缺+组 OpenSSH 线格式需引 JS 库;后端已有同款套路(host key) |
| 私钥交付 | **弹窗展示全文 + 下载 .pem 双通道** | 手机/电脑都能拿;复制防截断,下载保完整 |
| 私钥托管 | **服务端不落盘、只返回一次**;丢了重新生成 | 托管=第二份持久化敏感材料,收益不成比例 |
| 写路径 | keygen 只产料,**入库走既有 /api/config/customer 保存链** | 单点裁决:钥匙准入仍只过那一道已验证的配置提交 |
| 提交方式 | 前端 emit('save') 整配置透传+ssh_ingress 覆盖(现款) | 与手工流同一载荷契约,父层零改动 |

## §1 后端

### §1.1 生成函数(inbound/ssh 新导出)

```go
// inbound/ssh/keygen.go
type GeneratedIngressKey struct {
    PublicKey         string `json:"public_key"`          // authorized_keys 单行,含注释尾巴
    PrivateKey        string `json:"private_key"`         // OpenSSH PEM( PEM 私钥块原文)
    Fingerprint       string `json:"fingerprint"`         // SHA256:... base64(与 host key 指纹同口径)
    Algorithm         string `json:"algorithm"`           // 恒 "ssh-ed25519"
    Comment           string `json:"comment"`             // aliang-ingress-<hostname>-<YYYYMMDDHHMMSS>
    SuggestedFilename string `json:"suggested_filename"`  // aliang-ingress-<hostname>-<YYYYMMDD>.pem
}
```

json tag 一律 snake_case,与 hostkey 载荷(`fingerprint`/`algorithm`/`generated_at`)同口径;handler 测试须断言线格式字段名(防 Go 字段名直出导致前端读 undefined)。

```go
func GenerateIngressKey() (*GeneratedIngressKey, error)
```

- 密钥类型固定 ed25519(`ed25519.GenerateKey(rand.Reader)` +
  `gossh.MarshalPrivateKey`),与 host key 生成(server.go 同包)同源同款;
- 注释含主机名(`os.Hostname()`,失败回落 "node")+时间戳,密钥列表里一眼可辨;
- hostname 进文件名时做保守清洗(非 `[A-Za-z0-9._-]` 段替换为 `-`),防
  Content-Disposition 注入;
- 纯函数无副作用:不读不写任何文件、不碰 config、不计数。

### §1.2 端点(handler 新文件)

`app/http/handlers/ssh_ingress_keygen_handler.go`:

- `POST /api/proxy/ssh-ingress/keygen`;非 POST 一律
  `http.Error 405`(同 SSHIngressHandler.HandleHostkey 先例——直传状态码,
  不过 common.Error 的业务码映射);
- 成功:`common.Success(w, map[string]interface{}{"key": GeneratedIngressKey})`;
- 生成失败:`common.ErrorInternalServer`(环境级故障,报错优于编造);
- **无状态**:两进程(core/user-agent)各持实例等价,与 hostkey/known-hosts
  handler 同款注释惯例;
- **双注册**(2026-10-21 教训,缺任一侧=404 透传):
  - core:`routes.go` `register("/api/proxy/ssh-ingress/keygen", h.SSHIngressKeygen.HandleKeygen, http.MethodPost)`;
  - user-agent:`agentruntime/server.go registerAgentRoutes` `mux.HandleFunc` 同路径;
- 鉴权与 /api/proxy 代理族同档(core 默认中间件栈,user-agent 侧
  loopbackOnly),无额外会话关卡。

## §2 前端(SshIngressSettings.vue)

### §2.1 按钮位与显隐

- 正文(`data-ssh-ingress-body`)**顶部**新增主按钮
  「一键生成并启用」(`data-ssh-oneclick`,随组件 `data-ssh-*` 族),
  显隐条件:`!form.enabled || !form.authorizedKeys.length`
  (已启用且有钥匙 = 隐藏,界面归手工流);
- 已有钥匙但未启用时也显示(一键流对它同样成立);
- `:disabled="loading || saving || !!portError"`——**不挂 validationError**
  (无钥匙时 validationError 恒真,挂上会锁死一键按钮;portError 时禁用,
  红字在已展开的同屏可见);
- **折叠态联动(§1 头部症状的正面修复)**:watch `form.enabled`,变 true 且
  `!form.authorizedKeys.length` 时 `bodyExpanded = true`——拨头部开关即自动
  展开正文,一键按钮与红字提示当场入眼;「头部常驻短提示」列入明确不做
  (自动展开已覆盖主路径)。rationale 修正:`validationError` 仅在
  `enabled=true 且无钥匙` 时为真——恰是一键流必经的状态,挂上它会锁死按钮;
  开关关闭时它本为 '',挂不挂无害,但禁用条件仍只挂 `!!portError`。

### §2.2 一键流(组件内状态机)

```
点击 → POST /api/proxy/ssh-ingress/keygen
     → 失败:走现有 error 通道(卡片顶部红框),流程终止
     → 成功:暂存 pendingKeygen = key 结果
       → form.authorizedKeys = [key.public_key, ...原列表]  (插队头,可辨)
       → form.enabled = true
       → emit('save', {...config, ssh_ingress: 现款四字段载荷})
         (父层 SettingsPage 走既有 POST /api/config/customer,零改动)
```

- 保存结果回流靠现有 `successMessage` props watch(v3 修正:**error 一概
  不挂 watch**),方向语义钉死(父层每次保存**开头清空** error/success、
  结尾再置值——跃迁必触发):
  - `successMessage` **变为非空** 且 `keygenPending` 非空:若
    `keygenPending.public_key` 仍在 `form.authorizedKeys` 里 → **弹私钥
    弹窗**(展示该 pending 载荷),随后清 pending;已不在(用户删过钥匙)
    → 直接清 pending 不弹窗(不交付无主私钥)。保留既有 `loadHostkey()`
    重拉(指纹当场出现);
  - **error 任何变化都不动 keygenPending**(含保存失败的非空跃迁与开头
    清空的空串跃迁)。设计修正 v3:原「error 变非空即丢 pending」会在最
    自然的恢复链上永久吞私钥——一键保存失败后表单不回滚(enabled=true
    且钥匙在,一键按钮已按显隐规则隐藏),用户手工再点保存即可成功入库,
    此时本应补交付弹窗;丢 pending 会让用户拿到一把永远见不到私钥的已
    启用钥匙且无报错;
  - `keygenPending` 只被三种动作清掉:成功弹窗消费、新一键覆盖、公钥已
    不在列表时的静默丢弃。一键重试的空串跃迁因不挂 error watch、
    successMessage 只认非空,天然无误动作。
- 保存失败时**不**回滚本地表单(与现款手工流行为一致,用户改完再存);
  恢复链=手工再点保存(按钮仍在、校验已满足)→ 成功 → 弹窗补交付原钥,
  或列表删钥匙后重按一键换新钥。

### §2.3 私钥弹窗(一次性)

- 内容:算法+指纹、**私钥全文**(`<pre>` 等宽可选中)、
  「下载 .pem」(`data:application/octet-stream` href + `<a download>`,
  文件名取 `suggested_filename`;不用 Blob URL——jsdom 与部分受限环境
  createObjectURL 不可用,data URL 两端通吃)、
  「复制私钥」(复用卡片内现成 `copyText` 剪贴板降级模式)、
  连接示例:`ssh -D 1080 -i aliang-ingress-….pem 任意用户名@<节点IP> -p 56432`
  (注明**用户名随意**——入口鉴权只认公钥,server.go `meta.User()` 不参与匹配);
- 顶部警示条:「私钥仅展示这一次,关闭即弃;丢失请重新生成并在列表删除旧钥匙」;
- 关闭交互(v3.1 修订):**背景点击不关闭**(私钥不可恢复,误触代价过高),
  仅显式关闭按钮;关闭按钮用专用 aria 键 `sshIngress_privKeyClose`(照
  guide_close/agent_closeSettings 家族惯例,不复用弹窗标题);
- 关闭弹窗 → 组件内 state 清空;**不写 localStorage、不打日志、不进任何
  持久化**;
- 弹窗期间 `saving=false`(保存已成功),无阻塞交互。

## §3 错误处理汇总

| 故障 | 表现 | 用户出路 |
|---|---|---|
| keygen 请求失败/500 | 卡片顶部红框(现 error 通道) | 重试按钮再点 |
| 保存 400/500(校验不过) | 现有 error prop 红框;**pending 保留**、暂不弹私钥 | 手工再点保存成功→弹窗补交付原钥;或列表删钥匙重按一键换新钥 |
| 保存成功但公钥已被用户删除 | 不弹窗,pending 静默丢弃 | —(不交付无主私钥) |
| 下载失败(浏览器拦截) | 弹窗仍在,私钥文本可手动复制 | 复制兜底 |
| 用户关弹窗才想起没存私钥 | — | 列表删旧钥匙,重新一键 |

已知残差(最终审查修正:实际已被中和,记录存档):`successMessage` 是父层
全卡片共享的保存成功信号——理论上一键保存**失败**后,若用户先保存**别的
卡片**,共享 success 跃迁会让本卡评估弹窗交付。但实际链路自洽:他卡保存会
携带存量 ssh_ingress 走 config 深度 watch 重置本卡表单(生成的公钥被剥出
列表)→ 入列校验不通过 → pending 静默丢弃、不弹窗。困惑场景不复存在,无需
按卡片关联保存结果。

双击防重(v3.1 修正 + 终审修正):fetch 窗口由 `keygenInFlight` 在途旗标
封死(set 前置、try/finally 清),配确定性双击回归测试(在途第二击断言仅
一次 POST)。**keygenPending 刻意不进守卫**:它是 v3 恢复链的载体(跨保存
失败保留),守卫它会把 spec §3「删钥匙后重按一键换新钥」恢复路径变成哑
按钮(按钮可见但点击静默无响应)——终审抓出后撤出,并配换新钥回归测试
(旧守卫下第二次 POST 计数为 1 必红)。fetch resolve 到 pending 赋值之间
只有微任务,真实用户事件插不进来。

## §4 测试

- **inbound/ssh 单测**(keygen_test.go):
  - 公钥行过 `gossh.ParseAuthorizedKey`,注释前缀 `aliang-ingress-` 可辨;
  - 私钥 PEM 过 `gossh.ParsePrivateKey` 回读,公私钥匹配(指纹互证);
  - 连续两次调用:公钥不同(熵独立)、结构齐、SuggestedFilename 合法且无路径分隔符;
- **handler 单测**:非 POST=405;成功信封 `data.key` 六字段齐,
  **json tag snake_case 断言**(`public_key`/`private_key`/`fingerprint`/
  `algorithm`/`comment`/`suggested_filename`,防 Go 字段名直出);
- **前端 vitest**(SshIngressSettings.test.js 增量):
  - mock fetch:POST keygen → 断言 emit('save') 载荷含生成公钥且 enable=true;
  - 模拟 successMessage 回流 → 断言弹窗渲染私钥全文+下载+复制+连接示例;
  - **恢复链单测(v3 核心回归)**:一键保存失败(error 变非空)断言不弹窗;
    随后**手工再存成功**(error 清空→successMessage 变非空)断言弹窗补交付
    **原钥**(pending 跨失败保留);
  - **无主私钥防交付**:保存失败后配置里公钥被换掉(不在列表)→ success
    回流断言**不**弹窗;
  - 折叠态联动:enabled false→true 且无钥匙 → 断言 body 展开;
  - 已启用且有钥匙 → 断言一键按钮不渲染;
- 回归:现有 SshIngressSettings.test.js 与 vitest 全量基线绿;不新增 e2e。

## §5 i18n(zh.js / en.js 同步)

新增 9 条:`sshIngress_oneClick`、
`sshIngress_privKeyTitle`、`sshIngress_privKeyOnceWarning`、
`sshIngress_privKeyDownload`、`sshIngress_privKeyCopy`、
`sshIngress_privKeyCopied`、`sshIngress_privKeyClose`、
`sshIngress_privKeyConnectExample`、
`sshIngress_keygenFailed`。口径沿用现族:全角标点、「你」、「…」。
(privKeyClose 为 v3.1 增补——关闭按钮专用 aria,不复用弹窗标题。)
(原 `sshIngress_oneClickHint` 已删——它服务的头部提示已列明确不做。)

## 明确不做(YAGNI)

- 头部常驻短提示(§2.1 折叠态联动=自动展开已覆盖主路径);
- 服务端私钥托管/二次下载;
- 密钥吊销专用 UI(列表删除已覆盖);
- 密钥类型/强度配置(恒 ed25519);
- 浏览器端生成;
- mesh 互连钥匙自动生成(归组网页配对流程);
- 手机 App/RN 端同款能力(网页仪表盘先行,PhoneServer 不涉及)。

## 安全边界(明写不遮)

- 私钥经仪表盘 HTTP 回传:**56431 局域网直连无 TLS**(现状),私钥明文过
  LAN。边界=LAN 内仪表盘会话持有者;与 SSH 入口本身的暴露面(0.0.0.0:56432)
  同级同域,不引入越权放大。发布说明照此措辞。
- 服务端内存即弃:进程崩溃/重启后该私钥不可恢复,属设计使然而非缺陷。
- 弹窗内容零持久化(§2.3)。

## 影响面与兼容

- 纯新增:一个端点 + 卡片内一块交互;零存量行为变化;
- 400 钥匙闸门不动(一键流自行满足它);
- config 线格式零变化(`ssh_ingress` 四字段照旧);
- 发布说明需写:安全边界一条 + 「旧版客户端无此按钮,手工流不受影响」。
