package handlers

import (
	"errors"
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/setup"
)

type SystemServiceHandler struct {
	service *services.SystemServiceService
}

func NewSystemServiceHandler(service *services.SystemServiceService) *SystemServiceHandler {
	if service == nil {
		service = services.NewSystemServiceService()
	}
	return &SystemServiceHandler{service: service}
}

func (h *SystemServiceHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.GetStatus()
	if err != nil {
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	common.Success(w, result)
}

func (h *SystemServiceHandler) HandleInstall(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Install()
	if err != nil {
		if errors.Is(err, setup.ErrNotRoot) {
			common.ErrorForbidden(w, "Administrator privileges are required to register the system service.")
			return
		}
		if errors.Is(err, setup.ErrServiceExists) {
			common.ErrorConflict(w, "System service is already registered.")
			return
		}
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	common.Success(w, result)
}

func (h *SystemServiceHandler) HandleUninstall(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Uninstall()
	if err != nil {
		if errors.Is(err, setup.ErrNotRoot) {
			common.ErrorForbidden(w, "Administrator privileges are required to uninstall the system service.")
			return
		}
		if errors.Is(err, setup.ErrServiceNotInstalled) {
			common.ErrorNotFound(w, "System service is not registered.")
			return
		}
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	common.Success(w, result)
}
