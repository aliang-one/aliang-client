package handlers

// 聊天 WS 握手鉴权（2026-11-21 审计拍板：握手要登录态）。
// 现状缺陷：Origin 同源闸只能拦浏览器跨站，LAN 内任意进程可伪造 Origin 驱动
// AI 会话并代答工具审批（命令执行面），dashboard 登录态完全不参与此通道。
// 修法：HandleAIStream 顶端（本地/透传分叉之前）一处卡口——
// middleware.RequireDashboardSession 未过即 401，两条路径同门。
import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/middleware"
)

// aiStreamGateRequest 构造一个通过了 Origin 同源闸的握手请求（Origin=Host，
// 模拟同源浏览器页面发起的 WS 升级）。
func aiStreamGateRequest(sessionCookie *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/agent/ai/stream", nil)
	req.Header.Set("Origin", "http://127.0.0.1:56431")
	req.Host = "127.0.0.1:56431"
	if sessionCookie != nil {
		req.AddCookie(sessionCookie)
	}
	return req
}

func TestHandleAIStreamRejectsHandshakeWithoutDashboardSession(t *testing.T) {
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleAIStream(rec, aiStreamGateRequest(nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无登录态握手应 401，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "dashboard session") {
		t.Fatalf("错误语义应指向 dashboard session: %s", rec.Body.String())
	}
}

func TestHandleAIStreamLetsHandshakeWithDashboardSessionThrough(t *testing.T) {
	// 先签发一枚真实 session cookie（走 IssueDashboardSession 的正式路径）
	mint := httptest.NewRecorder()
	mintReq := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", nil)
	mintReq.RemoteAddr = "127.0.0.1:55555"
	if err := middleware.IssueDashboardSession(mint, mintReq); err != nil {
		t.Fatalf("签发 session: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range mint.Result().Cookies() {
		if c.Name == middleware.DashboardSessionCookieName {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatal("未签发 session cookie")
	}

	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleAIStream(rec, aiStreamGateRequest(cookie))
	// 有登录态：门放行。非 WS 请求后续会死在 Upgrade（400 级）或透传拨号
	// （离线 502/1011 语义）——无论哪种，绝不能是 401（门未过才有的形态）。
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("持有效 session 的握手不应 401，got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAIStreamRejectsTamperedSessionCookie(t *testing.T) {
	h := NewAgentHandler(nil)
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged-token")}
	rec := httptest.NewRecorder()
	h.HandleAIStream(rec, aiStreamGateRequest(forged))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie 应 401，got %d", rec.Code)
	}
}
