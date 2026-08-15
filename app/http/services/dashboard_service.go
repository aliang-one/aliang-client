package services

import (
	"encoding/json"
	"net/url"
	"strings"

	auth "aliang.one/nursorgate/processor/auth"
)

type DashboardService struct{}

var (
	getDashboardStatsFn   = auth.GetDashboardStats
	getDashboardTrendFn   = auth.GetDashboardTrend
	getDashboardModelsFn  = auth.GetDashboardModels
	getUsageRecordsFn     = auth.GetUsageRecords
	getHealthScoreFn      = auth.GetHealthScore
)

func NewDashboardService() *DashboardService {
	return &DashboardService{}
}

func (s *DashboardService) GetStats(query url.Values) map[string]interface{} {
	data, err := getDashboardStatsFn(query)
	return dashboardResult("stats_fetch_failed", "Failed to fetch dashboard stats", data, err)
}

func (s *DashboardService) GetTrend(query url.Values) map[string]interface{} {
	data, err := getDashboardTrendFn(query)
	return dashboardResult("trend_fetch_failed", "Failed to fetch dashboard trend", data, err)
}

func (s *DashboardService) GetModels(query url.Values) map[string]interface{} {
	data, err := getDashboardModelsFn(query)
	return dashboardResult("models_fetch_failed", "Failed to fetch dashboard models", data, err)
}

func (s *DashboardService) GetUsageRecords(query url.Values) map[string]interface{} {
	data, err := getUsageRecordsFn(query)
	return dashboardResult("usage_fetch_failed", "Failed to fetch usage records", data, err)
}

func (s *DashboardService) GetHealth() map[string]interface{} {
	data, err := getHealthScoreFn()
	return dashboardResult("health_fetch_failed", "Failed to fetch health score", data, err)
}

func dashboardResult(errorCode, errorPrefix string, data json.RawMessage, err error) map[string]interface{} {
	if err != nil {
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  errorCode,
			"msg":    errorPrefix + ": " + err.Error(),
		}
	}

	var payload interface{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &payload); err != nil {
			return map[string]interface{}{
				"status": "failed",
				"error":  "decode_failed",
				"msg":    "Failed to decode dashboard payload: " + err.Error(),
			}
		}
	}

	return map[string]interface{}{
		"status": "success",
		"data":   payload,
	}
}
