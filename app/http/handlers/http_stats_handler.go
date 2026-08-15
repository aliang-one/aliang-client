package handlers

import (
	"net/http"
	"strconv"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/processor/statistic"
)

type HTTPStatsHandler struct {
	collector *statistic.HTTPStatsCollector
}

func NewHTTPStatsHandler(collector *statistic.HTTPStatsCollector) *HTTPStatsHandler {
	return &HTTPStatsHandler{
		collector: collector,
	}
}

func (h *HTTPStatsHandler) HandleGetRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	records := h.collector.GetRequestRecords(limit)
	common.Success(w, map[string]interface{}{
		"requests": records,
		"count":    len(records),
	})
}

func (h *HTTPStatsHandler) HandleGetDomainStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	domain := r.URL.Query().Get("domain")
	if domain != "" {
		stats := h.collector.GetDomainStatsFor(domain)
		if stats == nil {
			common.ErrorBadRequest(w, "Domain not found in statistics", map[string]interface{}{
				"domain": domain,
			})
			return
		}
		common.Success(w, stats)
		return
	}

	allStats := h.collector.GetDomainStats()
	common.Success(w, map[string]interface{}{
		"domains": allStats,
		"count":   len(allStats),
	})
}

func (h *HTTPStatsHandler) HandleGetChartData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	durationStr := r.URL.Query().Get("duration")
	duration := time.Hour
	if durationStr != "" {
		if d, err := time.ParseDuration(durationStr); err == nil && d > 0 {
			duration = d
		}
	}

	chartData := h.collector.GetTrafficChartDataForDuration(duration)

	var totalUpload, totalDownload, totalInputTokens, totalOutputTokens, totalRequests, totalResponses int64
	for _, point := range chartData {
		totalUpload += point.UploadBytes
		totalDownload += point.DownloadBytes
		totalInputTokens += point.InputTokens
		totalOutputTokens += point.OutputTokens
		totalRequests += point.RequestCount
		totalResponses += point.ResponseCount
	}

	common.Success(w, map[string]interface{}{
		"dataPoints": chartData,
		"summary": map[string]interface{}{
			"totalUploadBytes":   totalUpload,
			"totalDownloadBytes": totalDownload,
			"totalInputTokens":   totalInputTokens,
			"totalOutputTokens":  totalOutputTokens,
			"totalRequests":      totalRequests,
			"totalResponses":     totalResponses,
			"dataPointCount":     len(chartData),
			"interval":           "15s",
		},
	})
}

func (h *HTTPStatsHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	stats := h.collector.GetStats()
	common.Success(w, stats)
}

func (h *HTTPStatsHandler) HandleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	h.collector.ClearAll()
	common.Success(w, map[string]interface{}{
		"message": "All HTTP statistics cleared successfully",
	})
}

func (h *HTTPStatsHandler) HandleGetPresetDomains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	common.Success(w, map[string]interface{}{
		"domains": statistic.PresetDomains,
	})
}
