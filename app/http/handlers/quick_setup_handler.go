package handlers

import (
	"errors"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
)

const quickSetupRequestMaxBytes = 2 << 20

type QuickSetupHandler struct {
	service *services.QuickSetupService
}

func NewQuickSetupHandler() *QuickSetupHandler {
	return &QuickSetupHandler{service: services.NewQuickSetupService()}
}

func (h *QuickSetupHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	common.Success(w, h.service.Catalog())
}

func (h *QuickSetupHandler) HandleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	var req models.QuickSetupRenderRequest
	r.Body = http.MaxBytesReader(w, r.Body, quickSetupRequestMaxBytes)
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.Render(req)
	if err != nil {
		if errors.Is(err, services.ErrQuickSetupUnauthenticated) {
			common.ErrorUnauthorized(w, err.Error())
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Quick setup render failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, resp)
}

func (h *QuickSetupHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	var req models.QuickSetupModelsRequest
	r.Body = http.MaxBytesReader(w, r.Body, quickSetupRequestMaxBytes)
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.Models(req)
	if err != nil {
		if errors.Is(err, services.ErrQuickSetupUnauthenticated) {
			common.ErrorUnauthorized(w, err.Error())
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}

	common.Success(w, resp)
}

func (h *QuickSetupHandler) HandleConfigState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	software := strings.TrimSpace(r.URL.Query().Get("software"))
	if software == "" {
		common.ErrorBadRequest(w, "software query parameter is required", nil)
		return
	}

	state, err := h.service.ConfigState(software)
	if err != nil {
		if errors.Is(err, services.ErrQuickSetupUnauthenticated) {
			common.ErrorUnauthorized(w, err.Error())
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Quick setup config state failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, state)
}

func (h *QuickSetupHandler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	var req models.QuickSetupApplyRequest
	r.Body = http.MaxBytesReader(w, r.Body, quickSetupRequestMaxBytes)
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.Apply(req)
	if err != nil {
		if errors.Is(err, services.ErrQuickSetupUnauthenticated) {
			common.ErrorUnauthorized(w, err.Error())
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Quick setup apply failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, resp)
}

// HandleRestore 一键还原原始配置。注意：Restore 在 manifest 保存失败等场景会返回
// 部分成功的 resp + 非 nil error——err != nil 一律按整体失败处理（500），不得把
// resp 的 Restored/Deleted 当成功结果返回给前端。
func (h *QuickSetupHandler) HandleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	var req models.QuickSetupRestoreRequest
	r.Body = http.MaxBytesReader(w, r.Body, quickSetupRequestMaxBytes)
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Software) == "" {
		common.ErrorBadRequest(w, "software is required", nil)
		return
	}

	resp, err := h.service.Restore(req.Software)
	if err != nil {
		if errors.Is(err, services.ErrQuickSetupUnauthenticated) {
			common.ErrorUnauthorized(w, err.Error())
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Quick setup restore failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, resp)
}
