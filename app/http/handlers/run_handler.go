package handlers

import (
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

// RunHandler handles HTTP requests for run mode operations
type RunHandler struct {
	runService *services.RunService
}

// NewRunHandler creates a new run handler instance with dependency injection
func NewRunHandler(runService *services.RunService) *RunHandler {
	return &RunHandler{
		runService: runService,
	}
}

// HandleRunStart handles POST /api/run/start
// No authentication required - starts the service for the current mode
func (rh *RunHandler) HandleRunStart(w http.ResponseWriter, r *http.Request) {
	result := rh.runService.StartService()
	common.Success(w, result)
}

// HandleRunStop handles POST /api/run/stop
func (rh *RunHandler) HandleRunStop(w http.ResponseWriter, r *http.Request) {
	result := rh.runService.StopService()
	common.Success(w, result)
}

// HandleRunStatus handles GET /api/run/status
func (rh *RunHandler) HandleRunStatus(w http.ResponseWriter, r *http.Request) {
	result := rh.runService.GetStatus()
	common.Success(w, result)
}

// HandleAliangLinkStatus handles GET /api/run/aliang/status
func (rh *RunHandler) HandleAliangLinkStatus(w http.ResponseWriter, r *http.Request) {
	probe := shouldProbeLinkStatus(r)
	result := rh.runService.GetAliangLinkStatus(r.Context(), probe)
	common.Success(w, result)
}

// HandleRunWintunInstall handles POST /api/run/wintun/install
func (rh *RunHandler) HandleRunWintunInstall(w http.ResponseWriter, r *http.Request) {
	result := services.StartWintunDependencyInstall()
	common.Success(w, result)
}

// HandleRunWintunStatus handles GET /api/run/wintun/status
func (rh *RunHandler) HandleRunWintunStatus(w http.ResponseWriter, r *http.Request) {
	result := services.RefreshWintunDependencyStatus()
	common.Success(w, result)
}

// HandleRunTUNStatus handles GET /api/run/tun/status
func (rh *RunHandler) HandleRunTUNStatus(w http.ResponseWriter, r *http.Request) {
	result := services.GetTUNStartupStatus()
	common.Success(w, result)
}

// HandleRunTUNConflictStatus handles GET /api/run/tun/conflicts
func (rh *RunHandler) HandleRunTUNConflictStatus(w http.ResponseWriter, r *http.Request) {
	result := services.GetTunConflictPromptStatus()
	common.Success(w, result)
}

// HandleRunSwift handles POST /api/run/swift
func (rh *RunHandler) HandleRunSwift(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetMode string `json:"mode"`
	}

	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}

	result := rh.runService.SwitchMode(req.TargetMode)
	common.Success(w, result)
}

func shouldProbeLinkStatus(r *http.Request) bool {
	value := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("probe")))
	return value == "1" || value == "true" || value == "yes"
}
