package middleware

// 远程管理面访问闸测试(2026-11-24 审计):--host 把管理 API 绑到非回环地址后,
// 绝大多数端点(配置读写、软件配置、运行控制、终端启动命令、证书……)此前
// 完全不查 dashboard 会话——局域网里任何设备都能无凭证调用(跨机实证:
// POST /api/config/customer 的畸形体直达 handler)。信任边界本就是「回环
// = 本机;远程 = 必须登录拿 cookie」,缺的只是把它落到中间件层统一执法。

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteDashboardAccessMiddleware(t *testing.T) {
	ResetDashboardSessionForTest()
	t.Cleanup(ResetDashboardSessionForTest)

	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})
	h := RemoteDashboardAccessMiddleware(next)

	// 先在回环请求上签发一个合法 dashboard 会话 cookie。
	issue := httptest.NewRequest(http.MethodPost, "/api/dashboard/session", nil)
	issue.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	if err := IssueDashboardSession(rec, issue); err != nil {
		t.Fatalf("IssueDashboardSession() error = %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("issued cookies = %#v", cookies)
	}

	cases := []struct {
		name       string
		method     string
		path       string
		remoteAddr string
		headers    map[string]string
		withCookie bool
		wantStatus int
	}{
		{"回环来源读配置", http.MethodGet, "/api/config/customer", "127.0.0.1:40000", nil, false, http.StatusOK},
		{"回环来源写配置", http.MethodPost, "/api/config/customer", "127.0.0.1:40000", nil, false, http.StatusOK},
		{"IPv4-mapped 回环来源", http.MethodGet, "/api/config/customer", "[::ffff:127.0.0.1]:40000", nil, false, http.StatusOK},

		{"远程读配置被拒", http.MethodGet, "/api/config/customer", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程写配置被拒", http.MethodPost, "/api/config/customer", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程软件配置激活被拒", http.MethodPost, "/api/software-config/activate", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程启动命令被拒", http.MethodPost, "/api/agent/tools/launch", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程读日志被拒", http.MethodGet, "/api/logs", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程日志流握手被拒", http.MethodGet, "/api/logs/stream", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程启动详情(含账号资料)被拒", http.MethodGet, "/api/startup/detail", "192.168.1.20:5555", nil, false, http.StatusUnauthorized},
		{"远程伪造 X-Forwarded-For 不被信任", http.MethodGet, "/api/config/customer", "192.168.1.20:5555", map[string]string{"X-Forwarded-For": "127.0.0.1"}, false, http.StatusUnauthorized},

		{"远程加载仪表盘静态页", http.MethodGet, "/", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程加载静态资源", http.MethodGet, "/assets/index-abc.js", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程健康检查", http.MethodGet, "/api/health", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程启动状态(登录页渲染所需)", http.MethodGet, "/api/startup/status", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程密码登录", http.MethodPost, "/api/auth/login", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程扫码登录初始化", http.MethodPost, "/api/auth/scan/init", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程扫码状态轮询", http.MethodGet, "/api/auth/scan/status", "192.168.1.20:5555", nil, false, http.StatusOK},
		{"远程扫码激活", http.MethodPost, "/api/auth/scan/activate", "192.168.1.20:5555", nil, false, http.StatusOK},

		{"远程携带合法 cookie 放行", http.MethodGet, "/api/config/customer", "192.168.1.20:5555", nil, true, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := reached
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if tc.withCookie {
				req.AddCookie(cookies[0])
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK && reached != before+1 {
				t.Fatal("放行的请求必须到达下游处理器")
			}
			if tc.wantStatus != http.StatusOK && reached != before {
				t.Fatal("被拒的请求不得到达下游处理器")
			}
		})
	}
}

// 默认中间件栈必须包含远程访问闸——否则「handler 里忘了查 cookie」的端点
// 又会整体裸奔(这正是本闸存在的原因)。
func TestDefaultMiddlewareStackEnforcesRemoteDashboardAccess(t *testing.T) {
	ResetDashboardSessionForTest()
	t.Cleanup(ResetDashboardSessionForTest)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Chain(ok, GetDefaultMiddleware()...)

	remote := httptest.NewRequest(http.MethodPost, "/api/config/customer", nil)
	remote.RemoteAddr = "192.168.1.20:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, remote)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote request via default stack: status=%d, want 401", rec.Code)
	}

	local := httptest.NewRequest(http.MethodGet, "/api/config/customer", nil)
	local.RemoteAddr = "127.0.0.1:40000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, local)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback request via default stack: status=%d, want 200", rec.Code)
	}
}
