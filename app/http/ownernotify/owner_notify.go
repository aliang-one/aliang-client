// Package ownernotify 给 session owner 进程提供一个自有、可达的环回通知端点。
//
// 背景：agent 子进程把"凭据被远端拒绝"POST 给 owner（services.
// NotifyOwnerAuthRejected → ALIANG_SESSION_OWNER_ADDR），owner 侧由
// /api/auth/agent-auth-rejected 触发 SoftExpired 恢复链。该端点历史上挂在
// HTTP dashboard 上，而 dashboard 与"会话权威"并不总是同进程：
//   - macOS/Windows 的 tray/app owner 刻意不起 dashboard（cmd/core.go：由
//     Core daemon 兜底），dashboard 端口被 root core 抢占；
//   - 即便 owner 起了 dashboard，56431 被别的进程先占时虽有随机端口回退，
//     但依赖 StartHttpServer 被调用。
// 一旦通知打到一个持有**另一套会话权威**的 dashboard（如 root core 的），
// generation 校验必然判 stale_generation 而丢弃，恢复链永远不跑，agent 被
// sticky 禁用后只能人工上线。
//
// 解法：owner 在 spawn agent 前调用 EnsureServer，于 127.0.0.1 随机端口起
// 一个只挂 agent-auth-rejected 的微监听（与自身 SessionAuthority 同进程，
// generation 语义天然对齐），并回灌 SessionOwnerAddrOverride——ownerBaseURL
// 的最高优先级——保证 agent 的通知永远命中"生下自己的那个进程"。
// 同进程内若完整 dashboard 也在监听，两者等价（同一套 handler 与权威）。
package ownernotify

import (
	"fmt"
	"net"
	"net/http"
	"sync"

	"aliang.one/nursorgate/app/http/handlers"
	"aliang.one/nursorgate/app/http/services"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/common/logger"
)

var (
	notifyMu   sync.Mutex
	notifyAddr string
	notifySrv  *http.Server

	// listenHook / serverHook 仅供测试注入（默认真实监听）。
	listenHook = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	serveHook  = func(srv *http.Server, l net.Listener) error { return srv.Serve(l) }
)

// EnsureServer 幂等启动 owner 自有的环回通知监听，并回灌
// SessionOwnerAddrOverride（仅在当前无显式 override 时设置，避免覆盖
// StartHttpServer 端口回退写入的真实 dashboard 地址——两者同进程时等价）。
// 非 session-owner 进程返回空串：只有 owner 会 spawn agent，也只需 owner
// 提供 notify 端点。监听随进程生命周期存活，无需关闭钩子。
func EnsureServer() string {
	if !auth.IsSessionOwnerProcess() {
		return ""
	}
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if notifyAddr != "" {
		return notifyAddr
	}
	listener, err := listenHook()
	if err != nil {
		logger.Warn(fmt.Sprintf("owner notify server listen failed: %v", err))
		return ""
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/agent-auth-rejected", handlers.NewAuthHandler().HandleAgentAuthRejected)
	notifySrv = &http.Server{Handler: mux}
	notifyAddr = fmt.Sprintf("http://%s", listener.Addr().String())
	go func() {
		if serveErr := serveHook(notifySrv, listener); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Warn(fmt.Sprintf("owner notify server stopped: %v", serveErr))
		}
	}()
	if services.SessionOwnerAddrOverride() == "" {
		services.SetSessionOwnerAddrOverride(notifyAddr)
	}
	logger.Info(fmt.Sprintf("owner notify server listening on %s (/api/auth/agent-auth-rejected)", notifyAddr))
	return notifyAddr
}

// Addr 返回已启动的通知端点基地址（未启动返回空串）。仅供测试断言。
func Addr() string {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	return notifyAddr
}

// ResetForTest 停止并清空本进程的通知监听状态，供其他包的测试隔离
// userAgentEnv 的副作用（仅测试使用）。
func ResetForTest() {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if notifySrv != nil {
		_ = notifySrv.Close()
	}
	notifySrv = nil
	notifyAddr = ""
}
