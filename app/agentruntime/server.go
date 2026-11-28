package agentruntime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/handlers"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

var (
	serverMu sync.Mutex
	server   *http.Server
	running  bool
)

func RunForeground(ctx context.Context) error {
	// 进程级独占锁：保证全局只有一个 user-agent。拿不到锁说明已有实例在跑
	// （如 EnsureStarted 在本进程启动前刚 spawn 了一份，或用户手动双开）。
	// 干净退出、不 listen、不报错 —— EnsureStarted 探 56433 会看到先到的实例。
	lockFile, err := acquireAgentLock()
	if err != nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] foreground_runtime another_instance_running exiting: %v", err))
		return nil
	}
	defer lockFile.Close()

	logger.Info("[AGENT-BOOT] foreground_runtime starting")
	if err := StartLocalServer(); err != nil {
		return err
	}
	<-ctx.Done()
	logger.Info("[AGENT-BOOT] foreground_runtime stopping")
	return StopLocalServer()
}

func StartLocalServer() error {
	serverMu.Lock()
	defer serverMu.Unlock()

	if running {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] local_server already_running addr=%s", config.DefaultUserAgentAddr))
		return nil
	}

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	listener, err := net.Listen("tcp", config.DefaultUserAgentAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on user agent address %s: %w", config.DefaultUserAgentAddr, err)
	}
	services.SetAgentAIApprovalHookBaseURL("http://" + config.DefaultUserAgentAddr)

	srv := &http.Server{
		Handler:           loopbackOnly(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	server = srv
	running = true

	// 用量采集循环不受本地 HTTP 服务器影响，此处刻意不联动停止（远程 WS 独立拨号）。
	go func() {
		err := srv.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			logger.Error(fmt.Sprintf("User agent HTTP server failed: %v", err))
		}

		serverMu.Lock()
		defer serverMu.Unlock()
		if server == srv {
			server = nil
			running = false
		}
	}()

	logger.Info(fmt.Sprintf("User agent runtime listening on http://%s", config.DefaultUserAgentAddr))
	logger.Info(fmt.Sprintf("[AGENT-BOOT] local_server listening url=http://%s routes_registered=true", config.DefaultUserAgentAddr))
	services.StartUsageTrackerRuntime()
	services.StartAgentRegistrationKeepalive()
	// listener 镜像与 watcher 归 WS 持有进程(user-agent);cmd 侧不接——
	// 分裂身份/共享 home 拓扑下双进程共写 offset 文件会丢事件(Task 10 评审
	// C1 定案)。先镜像后 watcher(1b8c36b 教训);拉取失败 Warn 降级回落
	// 本地文件,不阻断启动;cmd/boot_wiring_audit_test.go 的
	// TestListenerWiringAudit 拦 exactly-once。
	services.LoadListenerMirror()
	services.StartEventListenerWatchers()
	services.WarmAgentAuthorizedExecutionDirectories()
	// aliang-peer 注册自愈:按 customer.peer_mcp 对齐内核配置文件(开=确保
	// 注册且 command 最新;关=确保注销)。只 Warn 不阻断启动;应用细节见
	// services.EnsurePeerMCPMatchesConfig。
	services.EnsurePeerMCPMatchesConfig()
	// OpenViking 跨设备记忆面自愈(spec Phase2 §7.2):按 customer.openviking
	// 对齐注册表+claude.json+codex 托管块。默认开(2026-11-26 产品拍板;
	// 显式 false=清理对齐);managed 凭据端点未就绪时按增强层失败语义跳过
	// 本 boot。只 Warn 不阻断启动。
	services.EnsureOpenVikingMatchesConfig()
	// OpenViking 心跳拨测(spec Phase2 §7.5):每 10 分钟轻探 /health,连续
	// 3 次失败置「不可达」状态(仪表盘卡消费)。进程级一次;不做自动关停。
	services.StartOpenVikingProbe()
	// mesh runbook 自愈安装(D-9):内嵌 SKILL.md → ~/.claude/skills/mesh-runbook/。
	// 归属用户技能域,非 mesh 装配族(boot 接线审计 bootFiles 只扫 start.go/
	// core.go,server.go 不在其表,刻意不扩)。挂在 agent(56433)启动位:
	// core(SYSTEM)可能无用户家目录,装不动也无害。错误仅日志不阻断 boot;
	// 总闸关时安装器内诚实日志一条。
	services.EnsureMeshRunbookInstalled()
	// 持久附件缓存全局帽清扫(1GiB,按会话最旧驱逐):recall/truncateBefore 的
	// 孤儿文件统一在此收敛。只 Warn 不阻断启动。
	if err := services.AgentAttachmentSweepStartup(); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] attachment cache sweep failed err=%v", err))
	}
	// 统一 MCP 注册表 codex 托管块自愈(同 peer 自愈纪律:只 Warn 不阻断)。
	services.EnsureAliangMCPCodexMatchesRegistry()
	logger.Info("[AGENT-BOOT] local_server awaiting session owner sync")
	return nil
}

func StopLocalServer() error {
	serverMu.Lock()
	srv := server
	serverMu.Unlock()

	if srv == nil {
		return nil
	}

	// RUN-03:先收口本进程托管的 AI 会话与终端 PTY,再关 HTTP。closeAll 会
	// cancel 每个 run 的 ctx,newBackgroundCommandContext 的进程组组杀随之
	// 触发——否则 claude/codex 子进程成为孤儿,继续写工作区。必须在 HTTP
	// shutdown 之前:shutdown 最多等 5s,期间新请求仍可能派发 run。
	services.GetSharedAgentService().Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	services.StopUsageTrackerRuntime()
	services.StopAgentRegistrationKeepalive()
	return nil
}

// userAgentHandlerBusyTimeout 是 56433 上三把「锁串行」端点的服务端收割上限:
// AgentService.s.mu 忙(最坏 ~50s)或被焊死(2026-11-22 生产死锁)时,客户端
// 在上限内拿到 503,而非无限挂。var 而非 const 供测试收缩窗口。
var userAgentHandlerBusyTimeout = 30 * time.Second

// wrapAgentBusyRoute 给锁串行端点套 http.TimeoutHandler。与 services.Status()
// 的锁忙降级构成双保险——降级覆盖 status 常规轮询,503 兜底 sync/disable/
// enable/auth-recover 与极端锁死。超时体用 common.Error 同形 JSON envelope:
// 经 core proxy 透传后,仪表盘/手机按 {code,data} 解析,纯文本体会变成解析
// 错误。注意:TimeoutHandler 不回收已 park 的 handler goroutine(根治靠
// services 侧锁纪律),只保证客户端可见性;core 侧 proxy 12s 超时先于本 30s
// 触发,直连 56433 的调用方(测试/curl/MCP stdio)由本层兜底。
func wrapAgentBusyRoute(h http.HandlerFunc) http.Handler {
	return http.TimeoutHandler(h, userAgentHandlerBusyTimeout, `{"code":503,"msg":"agent busy","data":null}`)
}

func registerAgentRoutes(mux *http.ServeMux) {
	logger.Info("[AGENT-BOOT] local_server registering_routes routes=/api/agent/status,/api/agent/sync,/api/agent/enable,/api/agent/auth-recover,/api/agent/disable,/api/agent/tools,/api/agent/protocol,/api/agent/tools/launch,/api/agent/ai/approval-hook,/api/agent/ai/stream,/api/agent/web-terminal*,/api/agent/fs/*,/api/agent/peer/*")
	agentHandler := handlers.NewAgentHandler(services.GetSharedAgentService())
	mux.Handle("/api/agent/status", wrapAgentBusyRoute(agentHandler.HandleStatus))
	mux.Handle("/api/agent/sync", wrapAgentBusyRoute(agentHandler.HandleSync))
	mux.Handle("/api/agent/enable", wrapAgentBusyRoute(agentHandler.HandleEnable))
	// auth_expired 自愈链（2026-10-29 修复）：core 在 token 刷新成功后经
	// requestLocalUserAgentRecoverAfterAuthExpired 直连 POST 本端点，仪表盘侧
	// HandleAuthRecover 的 proxyIfNeeded 也转发到本 runtime。缺注册=404 透传，
	// auth_expired 终态在 core+user agent 双进程拓扑下永不自愈（双注册教训，
	// 2026-10-21 liang-dev 同族）。
	mux.Handle("/api/agent/auth-recover", wrapAgentBusyRoute(agentHandler.HandleAuthRecover))
	mux.Handle("/api/agent/disable", wrapAgentBusyRoute(agentHandler.HandleDisable))
	mux.HandleFunc("/api/agent/reconnect", agentHandler.HandleReconnect)
	mux.HandleFunc("/api/agent/session-event", agentHandler.HandleSessionEvent)
	mux.HandleFunc("/api/agent/tools", agentHandler.HandleTools)
	mux.HandleFunc("/api/agent/protocol", agentHandler.HandleProtocol)
	mux.HandleFunc("/api/agent/tools/launch", agentHandler.HandleLaunch)
	// 设备互传本地触发（D13）：核心侧 routes.go 已有同名注册，缺这里会 404
	//（双注册教训，2026-10-21 liang-dev 实测）。
	mux.HandleFunc("/api/agent/transfers", agentHandler.HandleTransferCreate)
	// 设备互访本地面（peer-link / `aliang mcp` stdio 面的 HTTP 后端）：核心侧
	// routes.go 同名注册透传（双注册教训同上，缺任一侧=404）。
	mux.HandleFunc("/api/agent/peer/exec", agentHandler.HandlePeerExec)
	mux.HandleFunc("/api/agent/peer/fetch-file", agentHandler.HandlePeerFetchFile)
	mux.HandleFunc("/api/agent/peer/devices", agentHandler.HandlePeerDevices)
	mux.HandleFunc("/api/agent/peer/device-info", agentHandler.HandlePeerDeviceInfo)
	mux.HandleFunc("/api/agent/peer/note", agentHandler.HandlePeerNote)
	mux.HandleFunc("/api/agent/peer/mcp", agentHandler.HandlePeerMcp)
	mux.HandleFunc("/api/agent/peer/gates", agentHandler.HandlePeerGates)
	// 远程协助总开关 gates(spec 2026-11-26-remote-assist-design §7.1):GET 读 /
	// POST 写 remote_assist_enabled,真身在本 runtime;core routes.go 侧同名
	// 注册只做 proxy 透传。缺任一侧=404(双注册纪律,route_parity_test 兜底)。
	mux.HandleFunc("/api/agent/assist/gates", agentHandler.HandleAssistGates)
	// 远程协助客户端会话面(Task 6):request/session/end/cancel 四端点真身
	// 在本 runtime(状态机+信令+审计);core routes.go 侧同名注册只做双闸+
	// 同源闸+真源标记+proxy 透传(零 body 解析)。方法前缀模式与 core 侧
	// register 形态对齐(同 /api/agent/fs/* 先例);缺任一侧=404(双注册纪律,
	// route_parity_test 兜底)。
	mux.HandleFunc("POST /api/agent/assist/request", agentHandler.HandleAssistRequest)
	mux.HandleFunc("GET /api/agent/assist/session", agentHandler.HandleAssistSession)
	mux.HandleFunc("POST /api/agent/assist/end", agentHandler.HandleAssistEnd)
	mux.HandleFunc("POST /api/agent/assist/cancel", agentHandler.HandleAssistCancel)
	// 远程协助观看 WS(Task 7):纯推送只读+回放续屏,真身在本 runtime;
	// core routes.go 侧同名注册只做双闸+同源闸+proxyAIStream 透传。缺任一
	// 侧=404(双注册纪律,route_parity_test 兜底)。
	mux.HandleFunc("/api/agent/assist/stream", agentHandler.HandleAssistStream)
	mux.HandleFunc("/api/agent/operations/events", agentHandler.HandleOperationEvents)
	mux.HandleFunc("/api/agent/ai/approval-hook", agentHandler.HandleAIApprovalHook)
	// agent-as-MCP 工具面端点（spec §7）：真身在此（AI 管理器住本 runtime），
	// core routes.go 侧同名注册只做 proxy 透传。缺任一侧 = 404（双注册教训，
	// 2026-10-21 liang-dev 实测）。方法前缀模式与 core 侧 register 形态对齐。
	mux.HandleFunc("POST /api/agent/ai/mcp/start", agentHandler.HandleAIMCPStart)
	mux.HandleFunc("POST /api/agent/ai/mcp/status", agentHandler.HandleAIMCPStatus)
	mux.HandleFunc("POST /api/agent/ai/mcp/approve", agentHandler.HandleAIMCPApprove)
	// 监听器 stdio MCP 面四端点(监听器批 plan Task 13,spec §5):`aliang mcp`
	// 的 listener_* 工具照 ai_run_* 同款经本地路由到本 runtime,再复用 Task 12
	// 共享处理器——云端凭据住本进程,冷子进程直调恒失败。双注册纪律:core 侧
	// routes.go 同名注册只做 proxy 透传(proxyIfNeeded),缺任一侧=404
	// (2026-10-21 教训,ai_run_* 同族)。
	mux.HandleFunc("POST /api/agent/listeners/mcp/create", agentHandler.HandleListenerMCPCreate)
	mux.HandleFunc("POST /api/agent/listeners/mcp/list", agentHandler.HandleListenerMCPList)
	mux.HandleFunc("POST /api/agent/listeners/mcp/update", agentHandler.HandleListenerMCPUpdate)
	mux.HandleFunc("POST /api/agent/listeners/mcp/delete", agentHandler.HandleListenerMCPDelete)
	// 监听器本地 REST 代理真身(监听器批 Mac/双单元拓扑补丁,2026-11-22):
	// 仪表盘 mux 住 core,core 侧云端凭据恒空(数据目录分离,身份快照在
	// core 进程恒不满足)→ listenerCloudRequest 识别后回环转发同名端点到
	// 此处,由持有云端凭据的本进程直拨云端。无会话闸——dashboard 会话库
	// 住 core 转发链不可携带,信任边界=本 mux 仅 loopback 绑定(同
	// /api/agent/* 家族)。缺注册=core 侧转发 404(双注册教训,2026-10-21
	// 同族);路径形态与 core 侧 routes.go 逐字对齐。
	mux.HandleFunc("GET /api/listeners", services.HandleListenerListAgent)
	mux.HandleFunc("POST /api/listeners", services.HandleListenerCreateAgent)
	mux.HandleFunc("PATCH /api/listeners/{id}", services.HandleListenerPatchAgent)
	mux.HandleFunc("DELETE /api/listeners/{id}", services.HandleListenerDeleteAgent)
	mux.HandleFunc("GET /api/listeners/{id}/events", services.HandleListenerEventsAgent)
	// 仪表盘聊天 WS（2026-10-28 统一）：core 把 /api/agent/ai/stream 透传到本
	// runtime，由本地 AI 管理器跑（journal/清单与手机同源）。缺注册=404 透传
	// （2026-10-21 liang-dev 双注册教训）。
	mux.HandleFunc("/api/agent/ai/stream", agentHandler.HandleAIStream)
	// 网页终端(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md):
	// 真身在此(保险箱/令牌/终端管理器住本 runtime),core routes.go 侧同名
	// 注册只做 proxy 透传。缺任一侧=404(双注册教训,2026-10-21 liang-dev 实测)。
	mux.HandleFunc("GET /api/agent/web-terminal", agentHandler.HandleWebTerminalState)
	mux.HandleFunc("POST /api/agent/web-terminal", agentHandler.HandleWebTerminalCommand)
	mux.HandleFunc("/api/agent/web-terminal/stream", agentHandler.HandleWebTerminalStream)
	// 网页终端文件浏览 FS 面(spec §6.3,阶段 3):list/read/download/upload
	// 四端点。真身在此(路径守卫/令牌/主开关全在本 runtime 的 handler+服务
	// 层),core routes.go 侧同名注册只做 proxy 透传——upload 的 core 腿零
	// body 解析(multipart 原样流穿,阶段1 勘误#10)。缺任一侧=404(双注册
	// 教训,2026-10-21 liang-dev 实测)。方法前缀模式与 core 侧对齐。
	mux.HandleFunc("GET /api/agent/fs/list", agentHandler.HandleWebTerminalFSList)
	mux.HandleFunc("GET /api/agent/fs/read", agentHandler.HandleWebTerminalFSRead)
	mux.HandleFunc("GET /api/agent/fs/download", agentHandler.HandleWebTerminalFSDownload)
	mux.HandleFunc("POST /api/agent/fs/upload", agentHandler.HandleWebTerminalFSUpload)
	// FS 写操作三端点(文件管理批,spec §6.3):mkdir/rename/delete 真 body 在
	// 本 runtime(handler+服务层守卫/清洗/审计),core routes.go 侧同名注册只
	// 做 proxy 透传(零 body 解析)。缺任一侧=404(双注册教训,2026-10-21
	// liang-dev 实测)。方法前缀模式与 core 侧对齐。
	mux.HandleFunc("POST /api/agent/fs/mkdir", agentHandler.HandleWebTerminalFSMkdir)
	mux.HandleFunc("POST /api/agent/fs/rename", agentHandler.HandleWebTerminalFSRename)
	mux.HandleFunc("POST /api/agent/fs/delete", agentHandler.HandleWebTerminalFSDelete)
	// OpenViking 跨设备记忆面读点/apply(spec Phase2 §7.5/§7.7):状态/拨测/对齐
	// 真身住本 runtime(boot 处 StartOpenVikingProbe/EnsureOpenVikingMatchesConfig
	// 在此),core routes.go 侧同名注册只做 proxyIfNeeded 透传(handler 注释自证)。
	// 0829febd 落地时漏了本侧注册=404 透传(双注册教训,2026-10-21 同族),parity
	// 护栏 2026-11-21 坐实补齐。
	mux.HandleFunc("/api/agent/openviking/status", agentHandler.HandleOpenVikingStatus)
	mux.HandleFunc("/api/agent/openviking/apply", agentHandler.HandleOpenVikingApply)
	// 聊天图片链路（spec §3.4）：上传+取图,双注册另一处 routes.go。
	mux.HandleFunc("/api/agent/ai/attachment/upload", agentHandler.HandleAttachmentUpload)
	mux.HandleFunc("/api/agent/ai/attachment", agentHandler.HandleAttachmentGet)
	mux.HandleFunc("/mcp/aliang/", agentHandler.HandleToolAskMCP)
	mux.HandleFunc("/api/agent/sessions", agentHandler.HandleVibeSessions)
	mux.HandleFunc("/api/agent/session", agentHandler.HandleVibeSession)
	mux.HandleFunc("/api/agent/scan-directories", agentHandler.HandleScanDirectories)
	// 文件访问授权：探测必须在本 runtime 内执行（tccguard 的 grant 按可执行
	// 文件 stamp 记录，在 core 里探测等于给另一个进程记授权）。核心侧 routes.go
	// 同名注册透传，缺任一侧=404（双注册教训，2026-10-21 实测）。
	mux.HandleFunc("/api/agent/file-access", agentHandler.HandleFileAccess)
	mux.HandleFunc("/api/agent/file-access/request", agentHandler.HandleFileAccessRequest)
	mux.HandleFunc("/api/agent/file-access/settings", agentHandler.HandleFileAccessSettings)
	// MCP 与技能只读报告：核心侧 routes.go 同名注册透传（双注册教训同上）。
	mux.HandleFunc("/api/agent/integrations", agentHandler.HandleAgentIntegrations)
	// 统一 MCP 注册表三端点；双注册纪律——core routes.go + 本 mux 缺任一侧
	// =404（2026-10-21 教训）。{name} 用方法前缀模式（PUT/DELETE 分流到不同
	// handler，同 core 侧写法）。
	mux.HandleFunc("/api/agent/mcp", agentHandler.HandleAliangMCPRegistry)
	mux.HandleFunc("PUT /api/agent/mcp/servers/{name}", agentHandler.HandleAliangMCPServerPut)
	mux.HandleFunc("DELETE /api/agent/mcp/servers/{name}", agentHandler.HandleAliangMCPServerDelete)
	// agentcore 记忆 CRUD;双注册纪律——core routes.go 同名注册透传,缺任一
	// 侧=404(老坑)。全部方法前缀形态,两条均为精确 pattern 互不吞。
	agentMemoryHandler := handlers.NewAgentMemoryHandler()
	mux.HandleFunc("GET /api/agent/memory", agentMemoryHandler.HandleMemoryList)
	mux.HandleFunc("DELETE /api/agent/memory", agentMemoryHandler.HandleMemoryDelete)
	mux.HandleFunc("POST /api/agent/memory", agentMemoryHandler.HandleMemoryPost)
	mux.HandleFunc("PUT /api/agent/memory/{id}", agentMemoryHandler.HandleMemoryPut)
	mux.HandleFunc("DELETE /api/agent/memory/{id}", agentMemoryHandler.HandleMemoryDelete)
	mux.HandleFunc("GET /api/agent/memory/projects", agentMemoryHandler.HandleMemoryProjects)
	// 仪表盘数据面端点：核心进程会把这两个请求代理到本 runtime，缺注册会让
	// 仪表盘 Agent 页的「远程终端」列表和「进行中」徽章永远拿不到数据
	// （2026-10-21 liang-dev 实测 404 透传）。
	mux.HandleFunc("/api/agent/runtime-sessions", agentHandler.HandleRuntimeSessions)
	mux.HandleFunc("/api/agent/terminal/{sessionId}/scrollback", agentHandler.HandleTerminalScrollback)
	// SSH TOFU 指纹库管理(spec §⑨):GET 列表 / DELETE 单条或裸删清空,方法分流
	// 在 handler 内 switch。known_hosts 路径与 core 侧同源(cache/ssh/known_hosts,
	// 与 outbound registry 共库);无状态,两进程各持一个实例等价。双注册另一处
	// routes.go(缺任一侧=404,2026-10-21 教训)。
	sshKnownHostsHandler := handlers.NewSSHKnownHostsHandlerFromCache()
	mux.HandleFunc("/api/proxy/ssh/known-hosts", sshKnownHostsHandler.HandleKnownHosts)
	// SSH 入口 host key 指纹(spec §7):GET 只读,缺失=404 未启动语义(生成归
	// inbound/ssh Server.Start,防双进程竞态换钥)。无状态纯文件读,两进程各持
	// 一个实例等价。双注册另一处 routes.go(缺任一侧=404 透传,2026-10-21 教训)。
	sshIngressHandler := handlers.NewSSHIngressHandlerFromCache()
	mux.HandleFunc("/api/proxy/ssh-ingress/hostkey", sshIngressHandler.HandleHostkey)
	// SSH 入口一键生成(spec 2026-11-12 §1.2):POST 无状态纯生成,私钥只在
	// 响应出现一次,服务端不落盘。无状态实例两进程各持等价。双注册另一处
	// routes.go(缺任一侧=404 透传,2026-10-21 教训)。
	sshIngressKeygenHandler := handlers.NewSSHIngressKeygenHandler()
	mux.HandleFunc("/api/proxy/ssh-ingress/keygen", sshIngressKeygenHandler.HandleKeygen)
	// SSH 出站「测试连接」(客户端连接状态反馈):POST 无状态现场探测,凭据
	// 不落盘。无状态,两进程各持等价。双注册另一处 routes.go(缺任一侧=404
	// 透传,2026-10-21 教训;/api/proxy 前缀 parity 护栏覆盖)。
	mux.HandleFunc("/api/proxy/ssh/test", handlers.HandleSSHTest)
	// mesh 面端点(D3 双注册):数据面真值住 core,本侧一律 forward-to-core
	// (56431)取真值——裸挂 MeshHandler 会恒报 running=false 反向假红。
	// 方法闸两形态:core 侧 register 裸路径+handler 内手判 405,本侧 mux
	// 方法前缀——行为等价,parity 提取剥可选前缀兼容两形态(护栏已扩
	// api/mesh,缺任一侧=404 透传,2026-10-21 教训同族)。
	// coreBase 走 ownerBaseURL 解析链(override > env > 默认),不做硬编码。
	meshForward := handlers.NewMeshCoreForwardHandlerWithBase(ownerBaseURL)
	mux.HandleFunc("GET /api/mesh/lan_peers", meshForward.ServeHTTP)
	mux.HandleFunc("GET /api/mesh/status", meshForward.ServeHTTP)
	mux.HandleFunc("GET /api/mesh/diag", meshForward.ServeHTTP)
	mux.HandleFunc("GET /api/mesh/identity", meshForward.ServeHTTP)
	// mesh STUN 显式活探测(组网诊断读面批 Task 7):POST 零体,真探测在
	// core 侧 handler(punch socket 出厂绑物理口,56433 侧一律 forward-to-core
	// 同族),本层只透传。双注册另一处 routes.go(缺任一侧=404 透传,
	// 2026-10-21 教训同族;/api/mesh 前缀 parity 护栏覆盖)。
	mux.HandleFunc("POST /api/mesh/stun_probe", meshForward.ServeHTTP)
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(strings.TrimSpace(host))
		if ip == nil || !ip.IsLoopback() {
			common.ErrorForbidden(w, "user agent only accepts loopback requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
