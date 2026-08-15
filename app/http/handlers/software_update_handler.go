package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

type SoftwareUpdateHandler struct {
	service *services.SoftwareUpdateService
}

func NewSoftwareUpdateHandler(service *services.SoftwareUpdateService) *SoftwareUpdateHandler {
	if service == nil {
		service = services.GetSharedSoftwareUpdateService()
	}
	return &SoftwareUpdateHandler{service: service}
}

func (h *SoftwareUpdateHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	common.Success(w, h.service.GetFrontendStatus())
}

func (h *SoftwareUpdateHandler) HandleDismiss(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.DismissCurrentUpdate()
	if err != nil {
		common.ErrorConflict(w, err.Error())
		return
	}
	common.Success(w, status)
}
