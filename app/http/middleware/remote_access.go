package middleware

// remote_access.go — 远程管理面访问闸(2026-11-24 审计):管理 API(56431)的
// 信任边界是「来源为回环 = 本机;非回环 = 远程浏览器,必须登录拿 dashboard
// 会话」。但此前只有 quick-setup/监听器/SSE/聊天 WS 等少数端点真的查了
// cookie,配置读写、软件配置、运行控制、终端启动命令、证书操作等绝大多数
// 端点完全不查——--host 把管理面绑到非回环地址后(审计实证 liang-dev 的
// *:56431),局域网里任何设备都能无凭证调用。
//
// 本中间件把这条边界落到统一入口:非回环来源的 /api/* 请求必须携带有效
// dashboard 会话 cookie,否则 401;放行面只有仪表盘静态资源与登录链路
// (密码登录/刷新/扫码 + 登录页渲染所需的只读状态)。回环来源行为不变
// (本机进程/回环代理腿不吃 cookie)。只认 TCP 对端地址,不信任
// X-Forwarded-For 等转发头——否则一行头就能冒充回环。
//
// 回环来源为何仍「免 cookie」:core→agent 的回环代理腿、本机 CLI/TUI、
// agent 子进程的 owner 通知都无 cookie,它们的信任基础是「同机回环」。
// 代理面(HTTP CONNECT/socks5)的非回环来源已在入站口被来源闸与目标守卫
// 拦住,无法再借网关代拨把自己变成回环来源。

import (
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
)

// remotePreAuthAPIPaths 是非回环来源无需 dashboard 会话即可访问的 API:
// 登录链路(密码/刷新/扫码)与登录页渲染所需的只读状态。刷新与扫码激活
// 凭请求体里的令牌(持有证明)换会话,登录成功时由 handler 下发 cookie。
var remotePreAuthAPIPaths = map[string]struct{}{
	"/api/auth/login":         {},
	"/api/auth/refresh":       {},
	"/api/auth/scan/init":     {},
	"/api/auth/scan/status":   {},
	"/api/auth/scan/activate": {},
	// 自身只做 cookie 轮换,handler 另有「有效 cookie 或回环」闸;远程无
	// cookie 在那里得 401,这里放行只为保留其专属错误语义。
	"/api/dashboard/session": {},
	"/api/health":            {},
	"/api/startup/status":    {},
}

// RemoteDashboardAccessMiddleware 非回环来源访问管理 API 必须携带有效
// dashboard 会话 cookie。放在 LoggingMiddleware 之内:被拒请求照常进访问
// 日志(状态码 401),无需另记。
func RemoteDashboardAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackRequest(r) || ValidateDashboardSession(r) || remoteRequestPreAuth(r) {
			next.ServeHTTP(w, r)
			return
		}
		common.ErrorUnauthorized(w, "Authenticated dashboard session is required")
	})
}

// remoteRequestPreAuth 报告非回环来源的请求是否属于登录前放行面:
// 非 /api/ 路径(嵌入的仪表盘页面与静态资源)与 remotePreAuthAPIPaths。
func remoteRequestPreAuth(r *http.Request) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	_, ok := remotePreAuthAPIPaths[path]
	return ok
}
