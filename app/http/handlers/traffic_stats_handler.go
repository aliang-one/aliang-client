package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/processor/statistic"
)

// TrafficStatsHandler 流量统计API处理器
type TrafficStatsHandler struct {
	collector *statistic.StatsCollector
}

// NewTrafficStatsHandler 创建新的流量统计处理器实例
func NewTrafficStatsHandler(collector *statistic.StatsCollector) *TrafficStatsHandler {
	return &TrafficStatsHandler{
		collector: collector,
	}
}

// HandleGetStats 获取指定时间尺度的流量统计数据
// GET /api/stats/{timescale}
func (h *TrafficStatsHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	// 从URL路径中提取timescale参数
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/stats/traffic/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		common.ErrorBadRequest(w, "Missing timescale parameter", nil)
		return
	}

	timescaleStr := pathParts[0]
	timescale := statistic.Timescale(timescaleStr)

	// 验证timescale参数
	if !timescale.IsValid() {
		common.ErrorBadRequest(w, "Invalid timescale parameter. Must be one of: 1s, 5s, 15s", map[string]interface{}{
			"provided_timescale": timescaleStr,
			"valid_values":       []string{"1s", "5s", "15s"},
		})
		return
	}

	// 获取统计数据
	snapshot, err := h.collector.GetStats(timescale)
	if err != nil {
		common.ErrorInternalServer(w, "Failed to get statistics", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	if snapshot == nil {
		common.ErrorServiceUnavailable(w, "Traffic statistics service is temporarily unavailable")
		return
	}

	common.Success(w, snapshot)
}

// HandleGetCurrentStats 获取当前实时流量信息
// GET /api/stats/current
func (h *TrafficStatsHandler) HandleGetCurrentStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	currentStats := h.collector.GetCurrent()
	if currentStats == nil {
		common.ErrorServiceUnavailable(w, "Traffic statistics service is temporarily unavailable")
		return
	}

	common.Success(w, currentStats)
}

// HandleGetCacheInfo 获取缓存信息（调试用）
// GET /api/stats/cache/info
func (h *TrafficStatsHandler) HandleGetCacheInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	size1s, size5s, size15s := h.collector.GetCacheSize()

	common.Success(w, map[string]interface{}{
		"cache_1s":  fmt.Sprintf("%d/300", size1s),
		"cache_5s":  fmt.Sprintf("%d/300", size5s),
		"cache_15s": fmt.Sprintf("%d/300", size15s),
	})
}

// HandleClearCache 清空统计缓存（调试用）
// POST /api/stats/cache/clear
func (h *TrafficStatsHandler) HandleClearCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	h.collector.ClearCache()

	common.Success(w, map[string]interface{}{
		"message": "All stats caches cleared successfully",
	})
}
