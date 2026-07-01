package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
)

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

	common.Success(w, h.service.Catalog())
}

func (h *QuickSetupHandler) HandleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.QuickSetupRenderRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.Render(req)
	if err != nil {
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

	var req models.QuickSetupModelsRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.Models(req)
	if err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}

	common.Success(w, resp)
}

func (h *QuickSetupHandler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.QuickSetupApplyRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.Apply(req)
	if err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Quick setup apply failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, resp)
}
