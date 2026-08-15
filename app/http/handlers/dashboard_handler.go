package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

type DashboardHandler struct {
	dashboardService *services.DashboardService
}

func NewDashboardHandler() *DashboardHandler {
	return &DashboardHandler{dashboardService: services.NewDashboardService()}
}

func (h *DashboardHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, h.dashboardService.GetStats(r.URL.Query()))
}

func (h *DashboardHandler) HandleGetTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, h.dashboardService.GetTrend(r.URL.Query()))
}

func (h *DashboardHandler) HandleGetModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, h.dashboardService.GetModels(r.URL.Query()))
}

func (h *DashboardHandler) HandleGetUsageRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, h.dashboardService.GetUsageRecords(r.URL.Query()))
}

func (h *DashboardHandler) HandleGetHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, h.dashboardService.GetHealth())
}
