package handlers

import (
	"io"
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
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
	common.Success(w, result)
}

func (h *AuthHandler) HandleRestoreSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.authService.RestoreSession()
	common.Success(w, result)
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
	common.Success(w, result)
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

// HandleScanActivate 扫码登录激活（用 st_ + refresh_token 完成本地登录）
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

	result := h.authService.ActivateScanLogin(req.SessionToken, req.RefreshToken)
	common.Success(w, result)
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
