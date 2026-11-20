# TODOS

## Security

### 聊天 WS 零登录鉴权:Origin 同源闸可被非浏览器客户端伪造绕过(审计 2026-11-20,需拍板)

**What:** `HandleAIStream`(app/http/handlers/agent_handler.go:70)握手仅过 aiStreamHostAllowed 同源闸(Orgin.Host==r.Host 即放行)——该闸只能拦浏览器跨站 CSRF,拦不住任何可自定义请求头的客户端:LAN 内任意进程带 `Origin: http://<设备IP>:56431` 握手即入,可经 DispatchLocalAI 驱动任意 prompt 并代答工具审批(ai.approval.response/option/tool_ask=命令执行面),dashboard 登录态完全不参与此通道。修复方向:握手前加 dashboard session 校验(至少对写语义帧 create/message/steer/approval/option/tool_ask),或 WS 握手引入登录后签发的一次性 ticket,让「Origin 同源」回归纯 CSRF 防线。

**Why:** 2026-11-19 LAN WS 同源闸重写(用户拍板 B)解决的是浏览器 LAN 可用性,本条是其留下的非对抗盲区——与「addr 闸=非对抗防线」既有口径一致,但审批面开放给 LAN 的后果比当初评估的重,须用户拍板后动。

**Context:** 审计=workflow critic 盲区维度发现,双验证员坐实 high;设计须兼顾 LAN 无登录使用场景(若 LAN 浏览本就要求已登录则纯加 session 校验即可,若允许匿名 LAN 只读则按帧语义分闸)。

**Effort:** M;**Priority:** P1(拍板后)
**Depends on:** 用户拍板(LAN 聊天是否要求登录态)

### Extend request-bound management authentication to all sensitive APIs

**What:** Apply the local management-session boundary introduced for auth snapshots and session events to every sensitive management endpoint.

**Why:** Process-global login state does not establish the identity of an individual HTTP caller, so unrelated local processes or unintended network clients may be able to act with the logged-in user's authority.

**Context:** The auth/proxy consistency work should first make session restore internal to the session-owner process and protect `/api/auth/session` plus session SSE with a request-bound local management session. Follow up by inventorying all sensitive routes, centralizing enforcement in middleware, defining loopback and LAN access policy, validating Origin/CSRF behavior, and adding unauthorized-caller integration tests. Start in `app/http/middleware/`, `app/http/routes/`, and `app/http/server.go`; reuse the session mechanism rather than creating endpoint-specific checks.

**Effort:** L
**Priority:** P2
**Depends on:** Local management-session mechanism from the auth/proxy consistency fix

## Architecture

### Multi-agent orchestration engine (missing capability)

**What:** Build a host-side orchestration layer on top of the existing per-session sub-agent support: planned multi-agent execution for goals (fan-out/fan-in), declarative team/pipeline profiles, controlled depth=2 nesting, and cross-device task scheduling over the peer MCP primitives.

**Why:** As of 2026-11-05 the product's "multi-agent" is model-improvised, single-turn, and stateless — each kernel decides on its own which sub-agents to spawn, and they vanish when the turn ends. There is no host-side orchestration authority (repo-wide keyword audit: crew/swarm/multi-agent/graph all zero hits; the only planner is goal's single-agent bounded loop). The user has decided this is a real product gap, not a nice-to-have.

**Context:** Full gap analysis, constraints (approval threading, depth=1 structural guarantee, journal attribution, timeout budget, codex observability hole), and a four-step candidate roadmap (R1 goal multi-executor → R2 orchestration profiles → R3 controlled depth=2 → R4 cross-device scheduling) live in `docs/superpowers/specs/2026-11-06-multi-agent-orchestration-gap.md`. The same audit's five per-kernel sub-agent defects (sub-run timeout ceiling, parallel sub-agents, skills-in-subrun, peer tool cancellation, task length cap) are already fixed on `fix/agentcore-subagent-blindspots`; the orchestration engine builds on those primitives.

**Effort:** XL
**Priority:** P1
**Depends on:** Sub-agent primitives on fix/agentcore-subagent-blindspots; R1 additionally needs PhoneServer goal-budget coordination

## Architecture

### agentcore MCP 健壮性包最终评审 5 条 NIT(记账不修,合并 commit 1f931f7)

**What:** 打磨 agentcore MCP 检活重连/过滤实现的 5 条非阻塞观察:①过滤汇总日志仅在「过滤确实剔了工具」时打(include 恰等全集时静默),且 `%d` 记归一前原名单长度(`agent_ai_agentcore_mcp.go` 装配循环);②exclude 未知名 Warn 逐名打 `%q` 而非聚合列表(`filterAgentCoreMCPTools`);③重拨失败时模型可见文本取**原** CallTool 错误,重拨失败原因 derr 只进日志(`holder.call` derr 分支);④`Close()` 后调用返回「连续 3 次重连失败」判死文案,与真实原因(holder 已关闭)不符——防御方向正确;⑤spec 测试计划条目 1 的「consecFail 清零」无直接断言(判死计数测试间接覆盖)。

**Why:** 最终评审 READY(0 BLOCKER/0 SHOULD)后余留的观察项,均不影响行为正确性与安全红线;其中④在 run 收尾后的病理性调用才可观测。

**Context:** spec=`docs/superpowers/specs/2026-11-09-agentcore-mcp-resilience-design.md`(§7 测试计划对照第 5 条)。若后续做 MCP 仪表盘 UI 批次或重连策略调优,顺手收敛。

**Effort:** S
**Priority:** P3
**Depends on:** 无

### agentcore 记忆层最终评审遗留账(合并 b75d3cc,均不阻塞)

**What:** 记忆层终审(READY)遗留 6 条:①记忆双注册=双写者,core 手动编辑与 runtime 异步提取/touch 并发直写同一 JSON、store 无跨进程锁——丢更新窗口按 LWW 有意取舍,收敛路径=core 侧补 proxy 面(沿 registry proxyIfNeeded 先例,`agent_memory_handler.go` 头注释已记);②registry 卡 `AgentMCPRegistrySettings.vue:468-470` 的 `isMissingError` 与记忆卡修复前同款「死且反向」掩蔽模式(正则分支吞掉双注册漏注册 404 事故),建议按记忆卡同款「挂 status+收窄」处置;③「跟随默认」无法清回已落盘的显式布尔(deepMergeJSONObjects 跳过 null 且 Merge 只增不减)——卡片 hint 已如实声明,产品上若要支持清除需后端补显式 unset 语义;④slug 大小写敏感,大小写不敏感盘上同项目可能共文件(沿 journal 同族先例);⑤effort 锁存不随 customer 配置切换重探,重启复位(代码注释已记);⑥Task 5 注入预算不计块头尾开销约 65 字符(理论可达 6065,实际不可达)。

**Why:** 终审 READY 后余留,均不影响正确性/安全红线/主流程;①②与多进程写和既有卡片相关,值得后续批次收敛。

**Context:** spec=`docs/superpowers/specs/2026-11-09-agentcore-memory-design.md`;记忆卡修复先例=`AgentMemorySettings.vue` 的 memoryApiRequest 挂 `e.status`+`isMissingError` 收窄形态。

**Effort:** S
**Priority:** P3
**Depends on:** 无

### agent-as-MCP 导出面评审遗留账(ai_run_* 三工具,均不阻塞)

**What:** 五个任务两关评审(规格+质量)余留的观察,按归属分组:

*端点后端(Task 2,`app/http/services/agent_ai_ai_mcp.go`)*
1. **条件键形状无自动化护栏**:status 的 pending_approvals 条目里 `command`/`tool_name`/`expires_at` 是「非空才带」的条件键——变异测试 F(gate 键恒带空串)整套仍绿,说明缺一条「空值不出键」的断言。补法:构造空 Command 的 waiter,断言序列化后 JSON 里没有该键。
2. `agentAIMCPStatusForError` 的第二个 case 与 default 分支等价,可删。
3. history 尾部遍历在 `m.mu` 临界区内(存量同款形态,不是本批引入);history 长时会拉长锁持有时间。
4. `snapshot()` 是浅拷贝,安全性依赖 `fanOutWrite` 的「帧写后不再改」冻结契约;若将来有帧被原地改写,这里会静默串数据。

*端点层(Task 3)*
5. **`HandleAIApprovalHook` 存量 bug**(`app/http/handlers/agent_handler.go:~304`):用 `common.Error(w, http.StatusMethodNotAllowed, ...)`,而 8 行之上的注释明确写着「直传 405 会被映射成 500」——实测确实返回 500。本批新增的 `handleAIMCPOp` 已按正确写法(`http.Error`)实现,老函数未动(不在本批范围)。修法:改成 `http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)`。
6. `cleanExistingAgentDirectory` 的 stat 错误会把**绝对路径**回吐给工具调用方(手机/桌面同款行为,存量)。是否脱敏需产品口径。
7. 已被逐出的工具面会话调 status 返回 `200 {state:"idle"}` 而非 400——「会话不存在」的语义在逐出场景下要不要区分,待定。

*工具面(Task 4)*
8. 工具面会话可能在内存压力下被逐出(ResidentCap=8)或随 agent 重启消失,之后 status/approve 会 403/400。工具描述里已有「会话可能被逐出,重新 start」的提示,但**没有**把「旧 session_id 会永久失效」讲透。
9. `cmd/mcp.go` 的 `invokeTool` 注释写「服务端已脱敏」(`:~359`),但 ai_run_* 三端点回的是本机会话状态摘要,脱敏与否取决于上游 handler,措辞偏乐观。

*其它*
10. `ai_mcp` 的仪表盘 UI 卡片(spec §6 明确 v1 不做):目前只能改文件/API 关闭,产品上缺可见开关。
11. ~~`ai_mcp` 线格式键缺 JSON round-trip 测试~~ **已补**:`TestAgentCoreAIMCPJSONRoundTrip`(键改名变异实测变红)。
12. SettingsPage 层仍缺「ai_mcp 三态透传」的**页面级**断言——`agentCoreSettings.test.js` 已覆盖 `normalizeAgentCoreTuning` 本身的三态(`ai_mcp: false/true/非法类型/缺键`),但 SettingsPage 的 `mapCustomerConfigFromApi` 出口没有专门用例;该页共用同一出口,回归风险低。
13. spec 行文里「第 6 工具组」的序数在群组增删后需要复核(纯文档一致性)。**当前成立**:peer 五工具 + ai_run_* 三工具 = 六组。
14. 记忆批沿用下来的 NIT 里与 `agentAIMCPPreflight` 同型的一条:每次调用都 `config.GetGlobalConfig()` 取指针后读字段,指针在 `SetGlobalConfig` 下会被整体替换(读取方要么拿旧要么拿新、各自视为不可变),因此**当前无竞态**;但若将来有人改成「锁外原地改字段」,这里会静默读到半更新的配置。**注**:`cmd/mcp.go` 的门控不走这条路(冷子进程直读文件,见 `agentMCPGateOn` 注释)。

*终审(全分支)三条已闭环*
15. ~~status/approve 的 `session_id 必填` 早返分支无测试兜底(删掉整套仍绿)~~ **已补**:`TestAgentMCPStatusRejectsEmptySessionID` / `TestAgentMCPApproveRejectsEmptySessionID`(两向变异实测变红)。
16. ~~core 侧三条路由注册存在性无测试(删除注册 / 去掉 `POST ` 前缀两向变异均绿)~~ **已补**:`app/http/routes/ai_mcp_route_test.go` 同时断言注册存在、`mux.Handler` 返回的 pattern 带 `POST ` 前缀、GET 被 ServeMux 兜底成 405 且 `Allow: POST`(两向变异实测变红)。
17. ~~`handleAIMCPOp` 方法闸注释写错折算目标(实为 500 而非 400)~~ **已改**:注释改为「过 ErrorCodeToHTTPStatus 落 default → 500」,结论(必须 `http.Error`)不变。

**Why:** 五任务全部两关评审通过(0 BLOCKER/0 SHOULD 未决),以上均为「不影响正确性、安全红线、主流程」的观察项;其中 5 是存量真 bug(HTTP 500 而非 405),6/7 是产品口径问题,1/11 是可测性缺口。

**Context:** spec=`docs/superpowers/specs/2026-11-10-agent-as-mcp-design.md`;plan=`docs/superpowers/plans/2026-11-10-agent-as-mcp.md`;记忆批同款卡片修复先例=`AgentMemorySettings.vue` 的 `memoryApiRequest` 挂 `e.status`+`isMissingError` 收窄形态。

**Effort:** S
**Priority:** P3
**Depends on:** 无

## Web Chat

### 网页聊天缺陷审计 27 条定案(批次 2026-11-20,workflow 7 维+逐条双对抗验证)

**What:** 网页聊天(app/website)全量审计产出 33 候选→坐实 27(去重后)→**首批 7 条修复进行中 worktree `fix/webchat-audit-batch1`**:①ai.status 终态不收窗(`useAliangChat.js:3286`,后端 ctx 取消只发 status 帧,前端气泡写字面量 "stopping"+输入区卡 90s+误标 error,修复=终态词表走 ai.done 同款收尾);②ensureAISocket close 永不落定(`:2057`,审批卡永久 responding+输入锁死只能刷新,修复=onclose reject 挂起 Promise);③审批帧写非响应式 raw 壳(`:3130`,已打开设备壳审批卡永不渲染,同文件 1922 行已有换代理先例,双验证员降级 medium);④附件按钮死键(`ChatComposer.vue:101`,bindFileInputEl 是函数 ref 注册函数,click 抛 TypeError);⑤上传契约错位(`agentApi.js:201`↔`agent_handler_attachment.go:88`,前端强制 path/Go 只回 ref,name,mime,bytes);⑥同名图片覆盖+chips 重复 key(`ChatComposer.vue:64`,伴生);⑦纯图消息客户端放行/服务端拒(`agent_ai.go:2370`,伴生,修复=服务端镜像 journal 写侧「content 空且附件空才拒」)。

**Why:** 前四条是用户日常必撞的 high(点停止必触发/断线点审批只能刷新/发图链路整条是死的);终态词表/换代理两处仓库内均有现成先例可镜像;⑥⑦在④⑤修好后立即转必现须同批带上。

**Context:** 审计=7 维并行(WS流式/审批运行/输入交互/Markdown-XSS/列表渲染/状态竞态/前后端契约)+每条 2 对抗验证员,84 agent 零误报率红线(5 条真实证伪剔除账在案:看门狗审批期误杀/pendingAttachments 串扰/多 tab 清史/回底徽标残留/localStorage 配额,均有具体证伪理由)。首批实现后双评审(正确性+契约两镜,发现即对抗验证):4 坐实全修——①迟到 close 无属主校验拆新连接的超时保险丝/挂起 Promise(timer 改 wrapper 挂 sock+onclose 重排,promise 清理进陈旧判定之后)②改名碰撞键换服务端净化域(逐字复刻 sanitizeAgentAttachmentBase+小写 ext,"a b.png"/"ab.png" 净化后撞名/大小写扩展名/80 截断全覆盖)③dot-at-0 ".png" 不再改名越白名单(base 空=服务端时间戳兜底不参与撞名域)④并发批次同名失效被证伪驳回(file input 受 attachmentUploading 闸控);评审新用例 4 枚(迟到 close 保险丝/净化域撞名/大小写 ext/dot-at-0)先红后绿,stash 回退实测判别力 18 红。

**Effort:** M(首批 7 条);**Priority:** P1
**Depends on:** 无

### 网页聊天审计遗留(仅剩拍板 2 条+存疑 3 条,三批修复全收口)

**收口记录:**①首批 7 条(2026-11-20,fix/webchat-audit-batch1 已合 master)+双评审 4 坐实全修(迟到 close 属主校验/净化域撞名键/dot-at-0/并发批证伪);②二批 11 条(2026-11-21,fix/webchat-audit-batch2 已合 master fb016da9)+双评审 6 坐实全修(blocker=审批族豁免白名单);③三批 Markdown 族 6 条(2026-11-21,fix/webchat-audit-batch3 已合 master)+双评审 9 坐实(去重 4 根)全修(blocker=列表缩进围栏保护/should=M5 快照前缀+M4 缩进收敛+JSON 闸门/nit=黏行死分支)。终态前端 813/813 绿。

**What:** 仅剩两类不动账:①拍板 2 条——聊天 WS 零登录鉴权/Origin 伪造(见 Security 节,P1 拍板后动);LAN 附件上传/取图 loopback+Origin 双闸 403(需拍板对齐聊天 WS 真同源口径,`agent_handler_attachment.go:16`,注意取图端点服务 <img> 无 Origin GET 需 Host 对齐)。②存疑 3 条(验证员 1:1 分歧,挂账):openDeviceSession 5min TTL 重入丢流式前缀/子代理面板状态不随会话切换复位/localStorage 跨 tab 覆盖。另有 AI 图片无白名单=追踪信标(需拍板:禁远程 or 限同源,`chatMarkdown.js:161` DOMPurify 配置)。

**Why:** 均有坐实证据;Markdown 族为渲染正确性/性能/安全面,逐条独立;两条需拍板项等待用户定口径。

**Context:** 全部细节(触发场景+修复方向+双验证员理由)在审计 workflow 输出;生命周期族收口记录在上。

**Effort:** M(逐条独立);**Priority:** P2(需拍板两条 P2)/P3(其余)
**Depends on:** 首批(worktree fix/webchat-audit-batch1)

## Networking

### mesh UDP 打洞载体遗留账(批次 2026-11-12,分支 feat/mesh-udp-punch)

**What:** 打洞一期落地后的七条非阻塞遗留:①端口预测/生日攻击与 coturn 自托管——二期项,闸门=两类真实网络(家宽非对称/蜂窝 CGNAT)真机打洞率数据;②IPv6 打洞(v1 udp4,候选解析 To4 断言对齐 agent_peer_lan);③`advertise_ip` 收下不消费(LAN 提示作候选源,beacon 候选注入同族);④诊断面板 UDP 可用性/载体统计 UI(本期载体可见性已落 [MESH-OUT] 日志行);⑤MeshSettings.vue:521 unconfigured 启发式未考虑 punch_enable(现不可达:punch 无 UI 开关);⑥PhoneServer punch.ts 两条 NIT(result 查表无 TTL 过期检查——A 端 pending 10s≪TTL 120s 无害;audit 元数据缺 from/to 对端 ID,事后关联需 session_id join);⑦v1 无 close 帧(同 session_id 重发=顶替收摊;过期 result 仍转发给 A 的窗口由 A 端 pending miss 兜底)。

**Why:** 均为「不影响正确性、安全红线、主流程」的记账项;验收口径=打洞率锚 70%±7.1% 学术基线(ACM IMC 2025)按网络族分桶,二期投入看一期真机数据。

**Context:** 决策记录=`docs/superpowers/specs/2026-11-12-mesh-udp-punch-design.md`;计划=`docs/superpowers/plans/2026-11-12-mesh-udp-punch.md`;B 侧 mTLS v1 降级(token 门禁+A 钉 B fail-closed,严格强于 peer.lan 纯 token 模型)与 target_device_id 路由信封(转发前剥除)已入 spec §2。

**Effort:** S(逐条独立)
**Priority:** P3
**Depends on:** 真机两类网络实测数据(①②);其余无

### 传输腿 punch 直连+STUN 可配置化遗留账(批次 2026-11-18,feat/transfer-punch-stun)

**What:** 八条非阻塞遗留:①传输腿预热(接收方并行 punch 会话)——被预算吸收(agentTransferPunchBudget=12s)替代,二期看真机数据;②诊断面板传输腿 carrier 分桶统计(lan/punch/cos);③coturn 自托管部署手册(集群 UDP 端口暴露面);④STUN 健康探测/多档 fallback(列表内逐台择优,现固定首台可达即用语义);⑤punch 失败阶段审计细分(dial-error 现记 probe,流中断 mid_transfer,LanPushFailStage 字段名两腿共用有轻度名实漂移);⑥cert_generate_failed 臂 consume-not-putback 口径与 listen_failed 不一致(极罕见路径,重试撞 transfer_context_missing 落 COS=设计退化方向);⑦A 侧 fail-closed 守卫未随计划落地(purpose=transfer 且 transferID 空串的三行守卫,dialSourceOpts/dialMiss 未实现——下游双闸兜住:编排入口拒空+B 侧 bad_transfer_id 拒单,防御纵深补 3 行即收);⑧punch 传输腿 90s TTL 体积悬崖(B 侧 punchAcceptLoop 定时器到点无条件收摊,A 侧 session 帧恒发 ttl_seconds=90——传输 >~85s 未完 transport 中途死→mid_transfer→COS 全量重传(punch 字节白付),与 lan_push 协议 15min serve grace 不对称;按 size 跳过 punch 或传输腿加长 ttl,真机数据定)。

**Why:** 均不影响正确性/安全红线/主流程;验收口径=vm220↔liang-dev 真机打洞实测(部署后),真机数据决定二期投入(端口预测/IPv6 等原有二期项)。

**Context:** spec=`docs/superpowers/specs/2026-11-18-transfer-punch-stun-design.md`;计划=`docs/superpowers/plans/2026-11-18-transfer-punch-stun.md`;PhoneServer 侧 punch_peer_id 下发为独立仓提交(AliangPhoneServer feat/transfer-punch-peer-id)。

**Effort:** S(逐条)
**Priority:** P3
**Depends on:** 真机实测数据(部署后)

### 组网拓扑画布节点卡 8+ peer 单环压框(节点卡扩容批 2026-11-19)

**What:** MeshSettings.vue 拓扑画布节点卡由 120×48 扩到 168×60(四行:标题/device_id 或地址/在线态/钉扎·中继徽标,防 device_id 溢出+分不清谁是谁)后,椭圆环 rx/ry 分档 190/130、200/110、230/110 对 n=1..7 数值复验无压框;8+ peer 单环相邻卡压框(旧 120×48 布局首个坏点同为 n=8,同病非回归),多环/自适应布局留待后续。同批改版:AI 规则改名「Aliang 的 AI 加速」并置顶(表单序 AI 加速→上游代理→代理规则),本机副行截断预算扣除端口段、徽标行截 26(节点详情右栏有全量行)。

**Why:** 8+ peer 是记账级边缘(现网部署 1-3 peer);压框只损可读性不损数据;评审以双反驳验证确认 n=5/7 曾被 150 半径引入回归,已按椭圆分档修复。

**Context:** 几何推导注释在 `app/website/src/components/settings/MeshSettings.vue` canvasNodes computed;评审过程=3 维度发现+逐条双反驳对抗验证(7 条坐实全修,含本条)。

**Effort:** S
**Priority:** P3
**Depends on:** 无
### 组网诊断地基批遗留账(批次 2026-11-19,feat/mesh-diag-foundation)

**What:** 七条非阻塞遗留:①mesh forward coreBase 解析链不覆盖「core 56431 被占回退随机端口」场景(ownerBaseURL 在 56433 进程的生效腿解析落回默认 56431,与旧硬编码同;--host 与 ALIANG_MANAGEMENT_ADDR 两场景已真修);②mesh 诊断日志按 [MESH-*]/[AGENT-TCP] 等 token 查,勿按 level 过滤——约 10 行错误态行按原样等价原则留在 Info 级,显式 level=error 查询漏看、运行时级别升 WARN+ 会静默出 buffer;③日志 sink stderr→stdout:stderr 分流部署(2>err.log)会丢行,journald 合流无影响;④mesh_assembly.go:490 无参 fmt.Sprintf(S1039 味,仓无 lint 有先例);⑤mesh_roster_sync.go 全程无日志,roster 失败仅经 /api/mesh/status 的 roster_sync 三态呈现——写 AI 组网 runbook 时须知;⑥buffer 测试全局态卫生(t.Cleanup 快照恢复更整洁,当前无冲突面);⑦**部署前置:server 侧 roster 端点版本(auto-networking 批 server=4708bd1)须先于/同步部署**,否则 roster_sync 恒 last_error(fail-visible 不假绿,但污染实弹观测)。

**Why:** 均不影响正确性/安全红线/主流程;验收口径=vm220↔liang-dev 打洞实弹(部署后),读面(状态三字段+/api/logs 收编)正是实弹排障仪器。

**Context:** 设计=`docs/superpowers/specs/2026-11-19-mesh-diag-foundation-design.md`;计划=`docs/superpowers/plans/2026-11-19-mesh-diag-foundation.md`(残余账 7 条全录);双评审+终审 Ready to commit。

**Effort:** S(逐条独立)
**Priority:** P3(⑦为 P1 部署注意项)
**Depends on:** vm220↔liang-dev 实弹排期;server 侧部署节奏

### 组网诊断读面批遗留账(批次 2026-11-19/20,feat/mesh-diag-tools)

**What:** 阶段 1(读面聚合端点 diag/identity/stun_probe+MCP 四枚 stdio/agentcore 双面+runbook 技能 embed 自愈安装)非阻塞遗留:①mesh gate 语义抽 meshGateSnapshot 公共函数单源化(diag 为闸语义权威超集,status 闸字段降位仪表盘兼容面,双 handler 现整块复制);②MCP 第三面(LLM 注册表 agent_tool_registry)——peer 五件套有三面先例,mesh 暂缓系读面价值排序,rev 漂移重拉机制现成;③组级三态门控键(mesh_diag,现继承 customer.peer_mcp 总闸隐式耦合——关 peer_mcp 连带失去 mesh 读工具);④agent 侧身份对比(56433 进程身份 vs core,/api/agent/status 手动交叉);⑤peer 维度载体分桶(v1 全局);⑥runbook 安装器「运行期开闸→下一进程周期生效」的用户文档;⑦完整 AI 会话回放验收(三案例真机,roster 孤儿/双身份脑裂/utun7——仓内已落 drift 护栏+数据特征测试,LLM 行为回放需真机);⑧classifyLedger A/B 逐字重复 ~60 行可抽小账本类型;⑨CLI bridge 面不含 mesh 四件(ask-peer 对齐已松为映射⊂agentcore,noBridgeFace 显式在册——将来注入 CLI 回合需回补测试)。

**Why:** 均不影响正确性/安全红线/主流程;读面+runbook 已可支撑实弹排障(阶段 2),写面(阶段 3)仍绑死实弹绿。

**Context:** 设计=`docs/superpowers/specs/2026-11-19-mesh-diag-tools-design.md`(拍板记录 D-1~D-11);计划=`docs/superpowers/plans/2026-11-19-mesh-diag-tools.md`;九任务全双评审+多轮修复环;SKILL.md 有 drift 护栏测试(端点引用+承重字段 token 双断言)。

**Effort:** S(逐条独立)
**Priority:** P3
**Depends on:** 实弹排期;真机 AI 回放验收

## Completed

## OpenViking Phase2 遗留拍板/补齐项(2026-11-20,Phase2 spec §13 关联)

### §13.2 卸载数据默认语义拍板(阻塞 UI 删除按钮)
- 现状:OpenVikingSettings.vue 三态卡已落地,**数据删除按钮挂起**(代码注释锚点=openviking 卡头注释);§11.2-4「删除按钮按用户域抹除」验收项连带挂起
- 待办:拍板「卸载=保留(建议)还是删除」→ 删除语义下实现按用户域抹除(MCP forget 用户域)+UI 按钮

### §13.4 AGPL「修改」认定(商用前专项审查)
- 现状:OpenViking v0.5.5 自托管+派生镜像(仅 pip 加 llama-cpp-python 未改 server 源码),倾向不触发 §13;spec §9 原文明写「正式商用前过一次许可证审查」
- 待办:商用前许可证专项审查(独立小活,不阻塞当前)

### Phase2 审计批补齐队列(2026-11-20 三路对抗审计坐实,代码外/部署类)
- PhoneServer deployment 加 OPENVIKING_ROOT_KEY env(secretKeyRef→secret `openviking-root-key`,yaml 已带 kubectl 命令注释)= managed 链上线硬前置
- PhoneServer vitest:credential 端点集成用例(requireAgentIdentity mock+reconcile 路径)——slug 纯函数 5 用例已补
- integrations 报告 openviking 节的前端消费(AgentIntegrationsSettings.vue 现只直出 notes;openviking 字段已可用)
- OV 账户探针用户 `ps-cred-probe` 清理(2026-11-20 契约实测残留)
- litellm spend $0.00:custom_openai 通道定价配置(美元口径)
