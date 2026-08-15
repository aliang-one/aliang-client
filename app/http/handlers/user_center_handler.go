package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
)

type UserCenterHandler struct {
	userCenterService *services.UserCenterService
}

func NewUserCenterHandler() *UserCenterHandler {
	return &UserCenterHandler{userCenterService: services.NewUserCenterService()}
}

func (h *UserCenterHandler) HandleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result := h.userCenterService.GetProfile()
		common.Success(w, result)
		return
	}

	if r.Method != http.MethodPut {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.UpdateProfileRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	result := h.userCenterService.UpdateProfile(req.Username)
	common.Success(w, result)
}

func (h *UserCenterHandler) HandleGetUsageSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.userCenterService.GetUsageSummary()
	common.Success(w, result)
}

func (h *UserCenterHandler) HandleGetUsageProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.userCenterService.GetUsageProgress()
	common.Success(w, result)
}

func (h *UserCenterHandler) HandleGetAPIKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	result := h.userCenterService.GetAPIKeys()
	common.Success(w, result)
}

func (h *UserCenterHandler) HandleRedeemCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var req models.RedeemCodeRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	result := h.userCenterService.RedeemCode(req.Code)
	common.Success(w, result)
}
