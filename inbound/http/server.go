package http

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/tcp"
)

// HTTP server state management
var (
	httpListener  net.Listener
	httpCtx       context.Context
	httpCancel    context.CancelFunc
	httpMutex     sync.Mutex
	isHttpRunning bool
	connIDCounter int64
)

// 资源防护旋钮(var 仅为测试注入短值;默认值即生产值)。
var (
	// httpReadTimeout 是 HTTP 面读取请求头(请求行+头部)的期限。嗅探层在读完
	// 首段后清掉了自己的读期限,ReadRequest 此前在无期限状态下等待——发半行
	// 请求行就停住的客户端每条连接白占一个 fd + 一个 goroutine,且发生在会话
	// 租约之前,任何来源都能触发。
	httpReadTimeout = 15 * time.Second
	// acceptErrorBackoff 是 accept 循环吃到非关闭类错误(fd 耗尽/EMFILE 等)
	// 后的退避:裸 continue 在错误持续期间会空转打满 CPU 并刷爆日志环形缓冲。
	acceptErrorBackoff = 100 * time.Millisecond
	// listenTCP 是监听器创建缝(测试注入假监听器)。
	listenTCP = net.Listen
)

// StartMitmHttp starts the HTTP CONNECT proxy server.
// This is a blocking function that runs until stopped. 绑定失败即进程退出
// (boot fail-fast:起不来让 systemd 拉起重试,坏状态不留活)。绑定重试路径
// (ReconcileUnifiedIngressBinding)须用 StartMitmHttpTolerant。
func StartMitmHttp() {
	startUnifiedIngress(true)
}

// StartMitmHttpTolerant 是绑定重试路径的容忍变体(评审 I1):绑定失败只记
// Error 并返回,不 Fatal——重绑(ssh_ingress 开关切换触发停→按新地址重绑,
// SPEC §5.5)在 goroutine 里跑,瞬时端口占用/环境异常把整个 agent 进程杀掉
// 属不成比例的失败;留活进程让下次提交或自愈链重试。
func StartMitmHttpTolerant() {
	startUnifiedIngress(false)
}

// startUnifiedIngress 是两条启动路径共用主体;fatalOnBindErr 区分 boot
// fail-fast 与重试容忍。
func startUnifiedIngress(fatalOnBindErr bool) {
	httpMutex.Lock()
	if isHttpRunning {
		httpMutex.Unlock()
		logger.Warn("HTTP proxy is already running")
		return
	}
	if !auth.GetSessionAuthority().CanProxy() {
		httpMutex.Unlock()
		logger.Warn("HTTP proxy start rejected: active session required")
		return
	}

	// Bind while holding the lifecycle lock. Logout first demotes authority, then
	// StopHttpProxy takes this same lock; this prevents a delayed start from
	// binding after the stop path observed an empty listener.
	listenAddr := config.UnifiedIngressListenAddr()
	listener, err := listenTCP("tcp", listenAddr)
	if err != nil {
		isHttpRunning = false
		httpMutex.Unlock()
		if fatalOnBindErr {
			logger.GetMainLogger().Fatal(fmt.Sprintf("Failed to listen on %s: %v", listenAddr, err))
			return
		}
		logger.Error(fmt.Sprintf("统一入口绑定失败(留待重试): %s: %v", listenAddr, err))
		return
	}

	httpCtx, httpCancel = context.WithCancel(context.Background())
	// gen 是本次 listener 的「代次」标记:重启绑定(Stop→Start)时旧 accept
	// 循环可能晚于新循环观察到取消,若无代次校验它会把新一代的 isHttpRunning
	// 清成 false(状态与实际绑定脱钩,后续自愈探针永远看不到端口在跑)。
	gen := httpCtx
	isHttpRunning = true
	httpListener = listener
	httpMutex.Unlock()

	logger.Info(fmt.Sprintf("统一入口监听已启动 addr=%s(嗅探 SSH/socks5/HTTP)", listenAddr))

	// Accept connections in a loop until context is cancelled
	for {
		// Accept will block until a connection arrives or listener is closed
		conn, err := listener.Accept()
		if err != nil {
			// Check if context was cancelled (listener was closed for shutdown)
			select {
			case <-gen.Done():
				logger.Info("HTTP proxy server stopped")
				httpMutex.Lock()
				if httpCtx == gen {
					isHttpRunning = false
					httpListener = nil
				}
				httpMutex.Unlock()
				return
			default:
				// Other errors (should be rare, e.g., network issues)
				logger.Debug(fmt.Sprintf("Accept error: %v", err))
				// 退避:错误持续期间(fd 耗尽等)裸 continue 会空转打满 CPU。
				select {
				case <-gen.Done():
				case <-time.After(acceptErrorBackoff):
				}
				continue
			}
		}

		// 每个连接先进统一分派:嗅探协议族后移交 ssh/socks/http。
		// 与合并前三口分立相比,「连哪个口」不再编码协议,「流量是什么」
		// 才决定走哪条处理链(spec §4)。
		go dispatchConn(conn)
	}
}

// StopHttpProxy stops the HTTP proxy server gracefully
func StopHttpProxy() {
	httpMutex.Lock()
	defer httpMutex.Unlock()

	if !isHttpRunning {
		logger.Warn("HTTP proxy is not running")
		return
	}

	logger.Info("Stopping HTTP proxy server...")

	// Cancel context to signal shutdown
	if httpCancel != nil {
		httpCancel()
	}

	// Close listener to interrupt Accept() call
	// This must be done outside the mutex to avoid deadlock
	listener := httpListener
	httpMutex.Unlock()

	if listener != nil {
		if err := listener.Close(); err != nil {
			logger.Debug(fmt.Sprintf("Error closing listener: %v", err))
		}
	}

	// Re-acquire lock to update state
	httpMutex.Lock()
	isHttpRunning = false
	// 清指针:CurrentListenAddr 以运行态为判据,指针留着会让已关闭的 listener
	// 继续被 Addr() 读出旧地址(重绑比对时误判「已经是期望地址」)。
	httpListener = nil
	logger.Info("HTTP proxy server stopped")
}

// IsHttpRunning returns the current state of HTTP proxy
func IsHttpRunning() bool {
	httpMutex.Lock()
	defer httpMutex.Unlock()
	return isHttpRunning
}

// CurrentListenAddr 当前实际绑定的统一入口地址(未运行返回 "")。供配置
// reconcile 比对「期望地址 ≠ 实际地址 → 重启 listener」——绑定随 ssh_ingress
// 开关变化(SPEC §5.5;config.UnifiedIngressListenAddr 现读配置),变化后必须
// 重绑才能生效(spec §6.2)。以 isHttpRunning 而非指针非空为判据:StopHttpProxy 关
// listener 但不清指针(close 后的 Addr() 仍返回旧地址),单看指针会把「已
// 停」误读成「还在旧地址上跑」,从而让 reconcile 漏掉该起的情形。
func CurrentListenAddr() string {
	httpMutex.Lock()
	defer httpMutex.Unlock()
	if !isHttpRunning || httpListener == nil {
		return ""
	}
	return httpListener.Addr().String()
}

// handleRawConnection 处理一条已嗅探为 HTTP 的连接(统一分派后本函数只剩
// HTTP 职责,名字沿用旧名以保持既有测试与调用面不变)。reader 必须保留:
// http.ReadRequest 需要 *bufio.Reader,透明代理分支还要把它交给
// HandleHttpConnection 续读——对 sniff 回放 conn 再包一层 bufio 是双层缓冲,
// 读路径无损(内层缓存先耗尽)。
func handleRawConnection(conn net.Conn) {
	defer conn.Close()

	// 为每个连接分配唯一ID
	connID := atomic.AddInt64(&connIDCounter, 1)
	logger.Debug(fmt.Sprintf("[CONN#%d] 新连接建立 - %s → %s", connID, conn.RemoteAddr(), config.UnifiedIngressListenAddr()))

	// 读取客户端初始数据，检查是否为 CONNECT 请求
	reader := bufio.NewReader(conn)
	// 读期限只覆盖请求头:嗅探层已在读完首段后清掉自己的期限,这里不设限则
	// 半截请求行能无限期占住连接(且发生在会话租约之前)。读到即清除,不污染
	// 后续隧道流量。
	_ = conn.SetReadDeadline(deadlineAfter(httpReadTimeout))
	req, err := http.ReadRequest(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		logger.Error(fmt.Sprintf("[CONN#%d] 读取请求失败: %v", connID, err))
		return
	}

	lease, err := auth.GetSessionAuthority().AcquireProxyLease(func() { _ = conn.Close() })
	if err != nil {
		writeHTTPStatusResponse(conn, http.StatusServiceUnavailable, "active session required")
		return
	}
	defer lease.Release()

	if req.Method == "CONNECT" {
		logger.Debug(fmt.Sprintf("[CONN#%d] CONNECT请求: %s", connID, req.Host))

		// Extract metadata from CONNECT request
		metadata, err := ExtractMetadataFromCONNECT(req, conn)
		if err != nil {
			logger.Error(fmt.Sprintf("[CONN#%d] 提取元数据失败: %v", connID, err))
			resp := &http.Response{
				Status:        "400 Bad Request",
				StatusCode:    http.StatusBadRequest,
				Proto:         "HTTP/1.1",
				ProtoMajor:    1,
				ProtoMinor:    1,
				Body:          io.NopCloser(strings.NewReader("")),
				ContentLength: 0,
			}
			resp.Write(conn)
			return
		}

		logger.Debug(fmt.Sprintf("[CONN#%d] 目标信息: %s:%d (IP:%s)", connID, metadata.HostName, metadata.DstPort, metadata.DstIP))

		// 代理面目标守卫(200 应答之前):非回环来源不得让网关代拨本机回环/
		// 内网目标——56431/56433 的信任边界是「来源为回环」,代拨发生在本进程。
		if err := tcp.CheckProxyFaceDestination(metadata); err != nil {
			logger.Info(fmt.Sprintf("[CONN#%d] 拒绝代理面请求: %v", connID, err))
			writeHTTPStatusResponse(conn, http.StatusForbidden, "")
			return
		}

		// Send 200 Connection Established
		// IMPORTANT: Use raw write instead of http.Response for proper formatting
		response200 := "HTTP/1.1 200 Connection Established\r\n\r\n"
		if _, err := conn.Write([]byte(response200)); err != nil {
			logger.Error(fmt.Sprintf("[CONN#%d] 发送200应答失败: %v", connID, err))
			return
		}
		logger.Debug(fmt.Sprintf("[CONN#%d] ✓ 已发送200 Connection Established", connID))

		// Handle CONNECT tunnel - establishes direct tunnel between client and target
		logger.Debug(fmt.Sprintf("[CONN#%d] 开始建立隧道...", connID))
		if err := HandleRawConnect(conn, metadata); err != nil {
			logger.Error(fmt.Sprintf("[CONN#%d] ❌ 隧道建立失败: %v", connID, err))
		} else {
			logger.Debug(fmt.Sprintf("[CONN#%d] ✓ 隧道建立成功并已关闭", connID))
		}
	} else {
		// 透明代理，基本不存在
		HandleHttpConnection(conn, reader, req)
	}
}

// writeHTTPStatusResponse 在 HTTP 面写一条定长错误应答(状态行+头+体一次
// 写成,Content-Length 与实际体一致)。
func writeHTTPStatusResponse(conn net.Conn, status int, body string) {
	response := &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
	}
	_ = response.Write(conn)
}
