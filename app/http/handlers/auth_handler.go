package handlers

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
)

// AuthHandler Token和用户认证处理器
type AuthHandler struct {
	authService *services.AuthService
}

// NewAuthHandler 创建新的认证处理器实例
func NewAuthHandler() *AuthHandler {
	return &AuthHandler{
		authService: services.NewAuthService(),
	}
}

func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.LoginRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	result := h.authService.Login(req.Email, req.Password, req.TurnstileToken)
	writeAuthResult(w, r, result)
}

func (h *AuthHandler) HandleRestoreSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	common.Success(w, h.authService.GetSessionSnapshot())
}

// recoverOrExpireLocalSession is the owner-side recovery entry point, hoisted
// into a package-level variable so tests can assert the handler triggers it
// without spinning up the real SoftExpired recovery loop.
var recoverOrExpireLocalSession = auth.RecoverOrExpireLocalSession

// lastAppliedNotifyUnix records (unix seconds) when the handler last let an
// applicable notification through to the recovery chain. It collapses
// notification bursts into at most one recovery run per apply window.
var lastAppliedNotifyUnix atomic.Int64

// agentAuthRejectedRequest is the notification an agent (non-owner) process
// POSTs when the remote (PhoneServer) rejected its delegated credentials.
type agentAuthRejectedRequest struct {
	Reason     string `json:"reason"`
	DeviceID   string `json:"device_id"`
	ObservedAt int64  `json:"observed_at"` // unix seconds
	// Generation 是纯日志信息，不参与判定（跨进程语义不成立，见
	// HandleAgentAuthRejected 的说明与 2026-09-22 生产实证）。
	Generation int64 `json:"generation"` // 0 = agent could not read a generation
}

// agentAuthRejectedMaxObservationAge bounds how old a notification may be
// before the owner discards it (design §4.4: ≤5 minutes; applies regardless
// of the carried generation — that field is log-only, see
// HandleAgentAuthRejected).
const agentAuthRejectedMaxObservationAge = 5 * time.Minute

// agentAuthRejectedApplyMinInterval is the minimum spacing between two
// actually-applied notifications; anything applicable arriving inside this
// window is answered with ignored=rate_limited instead of triggering another
// recovery run.
const agentAuthRejectedApplyMinInterval = 60 * time.Second

// HandleAgentAuthRejected 接收 agent 进程上报的"凭据被远端拒绝"通知，
// 触发 owner 侧 SoftExpired 恢复链（刷新成功→既有 handleAuthRefreshed 自动转发新会话；
// 刷新 401 才落 refresh_invalid 真终态）。
//
// generation 字段自 2026-09 起是纯日志信息，不参与判定：跨进程语义不成立。
// agent 子进程的 session authority 懒初始化把 Generation 定格在 1（见
// processor/auth/session_authority.go 的 initialize），且 non-owner 从不
// publish；owner 进程每次登录/刷新 publish 时 +1（≥2）。生产实证
// 2026-09-22 12:44：owner 侧曾用 GenerationActive 等值门，把 agent 恒为 1 的
// generation 通知一律判 stale_generation 丢弃，通知链 dead-on-arrival。
//
// 现行判定序（与携带 generation 与否无关）：
//  1. 非法 body/时间戳 → 400；
//  2. observed_at 为未来或距今 >5min → stale_notification（迟到通知）；
//  3. owner 快照非 Active → stale_notification（恢复已在途/未登录）；
//  4. 60s 去重窗口 → rate_limited；
//  5. 应用恢复（applied=true）。
//
// 防回退论证：等值门移除后，跨进程迟到通知若在 owner 已刷新后才到达，代价
// 只是一次经 arbiter 串行化的幂等恢复刷新（cache-hit 或 rotate 一次），换来
// "转发凭据真被远端拒了"时的即时恢复——值得。Active 门+5min 时间窗+60s 去重
// +恢复链幂等共同承担防回退。不设 dashboard 会话门槛：agent 子进程无
// cookie，dashboard HTTP 监听（loopback）即信任边界。但 `--host` 可将管理
// 监听重绑到非 loopback 地址，届时本端点随整个 dashboard 暴露；60s 去重 +
// 恢复链单飞是仅有的滥用闸门。
func (h *AuthHandler) HandleAgentAuthRejected(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req agentAuthRejectedRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}
	if req.ObservedAt <= 0 || req.Generation < 0 {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	observedAge := time.Since(time.Unix(req.ObservedAt, 0))
	// observedAge < 0（未来时间戳：时钟漂移或恶意构造）与超龄一样视为不
	// 新鲜，不得绕过新鲜度门。
	if observedAge < 0 || observedAge > agentAuthRejectedMaxObservationAge {
		logger.Warn(fmt.Sprintf("Ignored agent auth-rejected notification: observed %.0fs ago (device %q, generation %d, reason %q)", observedAge.Seconds(), req.DeviceID, req.Generation, req.Reason))
		common.Success(w, map[string]interface{}{"applied": false, "ignored": "stale_notification"})
		return
	}

	if snapshot := auth.GetSessionAuthority().Snapshot(); snapshot.State != auth.StateActive {
		logger.Warn(fmt.Sprintf("Ignored agent auth-rejected notification: state %s (device %q, generation %d, reason %q)", snapshot.State, req.DeviceID, req.Generation, req.Reason))
		common.Success(w, map[string]interface{}{"applied": false, "ignored": "stale_notification"})
		return
	}

	// 60s 最小间隔去重：通知风暴每个窗口最多放行一次恢复。刻意用先读后写而
	// 非 CAS——并发竞态最坏=两次恢复（恢复链本身幂等），可接受。
	nowUnix := time.Now().Unix()
	if elapsed := nowUnix - lastAppliedNotifyUnix.Load(); elapsed < int64(agentAuthRejectedApplyMinInterval/time.Second) {
		logger.Warn(fmt.Sprintf("Ignored agent auth-rejected notification: last applied %ds ago (device %q)", elapsed, req.DeviceID))
		common.Success(w, map[string]interface{}{"applied": false, "ignored": "rate_limited"})
		return
	}
	lastAppliedNotifyUnix.Store(nowUnix)

	logger.Info(fmt.Sprintf("Agent auth-rejected notification applied (reason %q, device %q, generation %d) — starting session recovery", req.Reason, req.DeviceID, req.Generation))
	recoverOrExpireLocalSession(req.Reason)
	common.Success(w, map[string]interface{}{"applied": true})
}

// HandleDashboardSessionBootstrap establishes request-bound local management
// identity before the dashboard reads auth state or opens SSE. Only loopback or
// an already authenticated dashboard session may rotate this credential.
func (h *AuthHandler) HandleDashboardSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.CanBootstrapDashboardSession(r) {
		common.ErrorUnauthorized(w, "Dashboard session bootstrap is restricted to loopback")
		return
	}
	if err := middleware.IssueDashboardSession(w, r); err != nil {
		common.ErrorInternalServer(w, "Failed to establish dashboard session", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"status": "success"})
}

func (h *AuthHandler) HandleRefreshSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.RefreshTokenRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	result := h.authService.RefreshSession(req.RefreshToken)
	writeAuthResult(w, r, result)
}

func (h *AuthHandler) HandleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.authService.GetUserInfo()
	common.Success(w, result)
}

// HandleScanInit 扫码登录初始化
// POST /api/auth/scan/init
func (h *AuthHandler) HandleScanInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.authService.ScanInit()
	common.Success(w, result)
}

// HandleScanStatus 扫码登录状态轮询
// GET /api/auth/scan/status?device_code=<PC密钥>
func (h *AuthHandler) HandleScanStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.authService.ScanStatus(r.URL.Query().Get("device_code"))
	common.Success(w, result)
}

// HandleScanActivate 扫码登录激活（两个字段均为本地 session 凭证）
// POST /api/auth/scan/activate
func (h *AuthHandler) HandleScanActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.ScanActivateRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	result := h.authService.ActivateScanLogin(req.SessionToken, req.RefreshToken, req.UpstreamExpiresIn)
	writeAuthResult(w, r, result)
}

// HandleLogout 处理登出请求
// POST /api/auth/logout
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.LogoutRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		if err != io.EOF {
			common.ErrorBadRequest(w, "Invalid request format", nil)
			return
		}
	}

	result := h.authService.LogoutUser(req.RefreshToken)
	common.Success(w, result)
}

func authResultSucceeded(result map[string]interface{}) bool {
	status, _ := result["status"].(string)
	return status == "success"
}

func writeAuthResult(w http.ResponseWriter, r *http.Request, result map[string]interface{}) {
	if authResultSucceeded(result) {
		if err := middleware.IssueDashboardSession(w, r); err != nil {
			common.ErrorInternalServer(w, "Failed to establish dashboard session", map[string]interface{}{"error": err.Error()})
			return
		}
	}
	common.Success(w, result)
}
