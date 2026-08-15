package user

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"aliang.one/nursorgate/processor/config"
)

type dashboardEnvelope struct {
	Data json.RawMessage `json:"data"`
}

func getSub2APIURL(path string, query url.Values) (string, error) {
	globalCfg := config.GetGlobalConfig()
	if globalCfg == nil {
		return "", fmt.Errorf("global config is not initialized")
	}

	baseURL := strings.TrimSpace(globalCfg.APIBaseURL())
	if baseURL == "" {
		return "", fmt.Errorf("api base url is not configured")
	}

	baseParsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	baseParsed.Path = path
	baseParsed.RawQuery = query.Encode()
	return baseParsed.String(), nil
}

func fetchDashboardData(path string, query url.Values) (json.RawMessage, error) {
	endpoint, err := getSub2APIURL(path, query)
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	var envelope dashboardEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse dashboard response: %w", err)
	}
	return envelope.Data, nil
}

func GetDashboardStats(query url.Values) (json.RawMessage, error) {
	return fetchDashboardData("/api/v1/usage/dashboard/stats", query)
}

func GetDashboardTrend(query url.Values) (json.RawMessage, error) {
	return fetchDashboardData("/api/v1/usage/dashboard/trend", query)
}

func GetDashboardModels(query url.Values) (json.RawMessage, error) {
	return fetchDashboardData("/api/v1/usage/dashboard/models", query)
}

func GetUsageRecords(query url.Values) (json.RawMessage, error) {
	return fetchDashboardData("/api/v1/usage", query)
}

// GetHealthScore fetches the health score from the backend service
func GetHealthScore() (json.RawMessage, error) {
	return fetchDashboardData("/api/v1/admin/ops/dashboard/snapshot-v2", nil)
}
