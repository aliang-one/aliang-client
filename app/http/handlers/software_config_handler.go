package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
)

type SoftwareConfigHandler struct {
	service *services.SoftwareConfigService
}

func NewSoftwareConfigHandler(service *services.SoftwareConfigService) *SoftwareConfigHandler {
	return &SoftwareConfigHandler{service: service}
}

func (h *SoftwareConfigHandler) HandleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.SaveSoftwareConfigRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	cfg, err := h.service.Save(req)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}

	common.Success(w, cfg)
}

func (h *SoftwareConfigHandler) HandleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.ActivateSoftwareConfigRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	cfg, err := h.service.Activate(req)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}

	common.Success(w, cfg)
}

func (h *SoftwareConfigHandler) HandlePushToCloud(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.CloudPushRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.PushToCloud(req)
	if err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Cloud push failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, models.CloudSyncResponse{
		SyncedCount:  resp.PushedCount,
		LastSyncedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *SoftwareConfigHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	software := strings.TrimSpace(r.URL.Query().Get("software"))
	configs, err := h.service.ListBySoftware(software)
	if err != nil {
		common.ErrorInternalServer(w, "Failed to list software configs", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{
		"items":    configs,
		"software": software,
	})
}

func (h *SoftwareConfigHandler) HandlePullFromCloud(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.CloudPullRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.PullFromCloud(req)
	if err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Cloud pull failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, resp)
}

func (h *SoftwareConfigHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.DeleteSoftwareConfigRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	if err := h.service.Delete(req); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "record not found") {
			common.ErrorNotFound(w, "Config not found")
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Delete failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{"deleted": true})
}

func (h *SoftwareConfigHandler) HandleSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.SelectSoftwareConfigRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	if err := h.service.SetSelected(req); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "record not found") {
			common.ErrorNotFound(w, "Config not found")
			return
		}
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Select failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{"selected": req.Selected})
}

func (h *SoftwareConfigHandler) HandleCompareWithCloud(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.CompareSoftwareConfigRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	resp, err := h.service.CompareWithCloud(req)
	if err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Cloud compare failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, resp)
}

func (h *SoftwareConfigHandler) HandlePushSelectedToCloud(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.CloudPushRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}
	req.OnlySelected = true

	resp, err := h.service.PushToCloud(req)
	if err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Cloud push failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, models.CloudSyncResponse{
		SyncedCount:  resp.PushedCount,
		LastSyncedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *SoftwareConfigHandler) HandleLogOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.LogSoftwareConfigOperationRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	if err := h.service.LogOperation(req); err != nil {
		if isBadRequestError(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, "Log operation failed", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{"logged": true})
}

func isBadRequestError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, http.ErrMissingFile) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "is required") || strings.Contains(msg, "must be rfc3339") || strings.Contains(msg, "not valid")
}
