package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

type ConfigCenterHandler struct {
	configCenterService *services.ConfigCenterService
}

func NewConfigCenterHandler() *ConfigCenterHandler {
	return &ConfigCenterHandler{configCenterService: services.NewConfigCenterService()}
}

// HandleGetRenderedConfig handles GET /api/configs/rendered?tag=xxx
// It fetches the config template from the backend by tag, renders it with
// global variables and the user's apikey, then returns the rendered result.
func (h *ConfigCenterHandler) HandleGetRenderedConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	tag := r.URL.Query().Get("tag")
	result := h.configCenterService.GetRenderedConfig(tag)
	common.Success(w, result)
}
