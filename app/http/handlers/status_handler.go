package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/processor/statistic"
)

type publicAIActivityDetection struct {
	ProviderKey                   string `json:"providerKey"`
	ProviderLabel                 string `json:"providerLabel"`
	LastSeenAt                    int64  `json:"lastSeenAt"`
	LastSeenUnix                  int64  `json:"lastSeenUnix"`
	HitCount                      int64  `json:"hitCount"`
	Active                        bool   `json:"active"`
	ActiveConnectionCount         int    `json:"activeConnectionCount"`
	LastConnectionDurationSeconds int64  `json:"lastConnectionDurationSeconds"`
	RemainingTTL                  int64  `json:"remainingTtlSeconds"`
	TTLSeconds                    int64  `json:"ttlSeconds"`
	DetectedBySNI                 bool   `json:"detectedBySNI"`
}

type publicAIActivitySummary struct {
	Active                bool                        `json:"active"`
	ActiveCount           int                         `json:"activeCount"`
	TTLSeconds            int64                       `json:"ttlSeconds"`
	VisibleWindowSeconds  int64                       `json:"visibleWindowSeconds"`
	TotalHits             int64                       `json:"totalHits"`
	LastSeenAt            int64                       `json:"lastSeenAt"`
	LastProvider          string                      `json:"lastProvider,omitempty"`
	LastLabel             string                      `json:"lastLabel,omitempty"`
	DetectedBySNI         bool                        `json:"detectedBySNI"`
	ActiveDetections      []publicAIActivityDetection `json:"activeDetections"`
	RecentProviderTraffic []publicAIActivityDetection `json:"recentProviderTraffic"`
}

type StatusHandler struct {
	trafficCollector *statistic.StatsCollector
	httpCollector    *statistic.HTTPStatsCollector
	aiTracker        *statistic.AIActivityTracker
}

func NewStatusHandler(
	trafficCollector *statistic.StatsCollector,
	httpCollector *statistic.HTTPStatsCollector,
	aiTracker *statistic.AIActivityTracker,
) *StatusHandler {
	return &StatusHandler{
		trafficCollector: trafficCollector,
		httpCollector:    httpCollector,
		aiTracker:        aiTracker,
	}
}

func (h *StatusHandler) HandleGetSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	var traffic *statistic.CurrentStats
	if h.trafficCollector != nil {
		traffic = h.trafficCollector.GetCurrent()
	}
	if traffic == nil {
		traffic = &statistic.CurrentStats{}
	}

	httpStats := map[string]interface{}{}
	if h.httpCollector != nil {
		httpStats = h.httpCollector.GetStats()
	}

	aiSummary := statistic.GetDefaultAIActivityTracker().Summary()
	if h.aiTracker != nil {
		aiSummary = h.aiTracker.Summary()
	}

	common.Success(w, map[string]interface{}{
		"traffic": traffic,
		"http":    httpStats,
		"ai":      sanitizeAIActivitySummary(aiSummary),
	})
}

func sanitizeAIActivitySummary(summary *statistic.AIActivitySummary) publicAIActivitySummary {
	if summary == nil {
		return publicAIActivitySummary{}
	}

	return publicAIActivitySummary{
		Active:                summary.Active,
		ActiveCount:           summary.ActiveCount,
		TTLSeconds:            summary.TTLSeconds,
		VisibleWindowSeconds:  summary.VisibleWindowSeconds,
		TotalHits:             summary.TotalHits,
		LastSeenAt:            summary.LastSeenAt,
		LastProvider:          summary.LastProvider,
		LastLabel:             summary.LastLabel,
		DetectedBySNI:         summary.DetectedBySNI,
		ActiveDetections:      sanitizeAIActivityDetections(summary.ActiveDetections),
		RecentProviderTraffic: sanitizeAIActivityDetections(summary.RecentProviderTraffic),
	}
}

func sanitizeAIActivityDetections(detections []*statistic.AIActivityDetection) []publicAIActivityDetection {
	if len(detections) == 0 {
		return []publicAIActivityDetection{}
	}

	publicDetections := make([]publicAIActivityDetection, 0, len(detections))
	for _, detection := range detections {
		if detection == nil {
			continue
		}

		publicDetections = append(publicDetections, publicAIActivityDetection{
			ProviderKey:                   detection.ProviderKey,
			ProviderLabel:                 detection.ProviderLabel,
			LastSeenAt:                    detection.LastSeenAt.Unix(),
			LastSeenUnix:                  detection.LastSeenUnix,
			HitCount:                      detection.HitCount,
			Active:                        detection.Active,
			ActiveConnectionCount:         detection.ActiveConnectionCount,
			LastConnectionDurationSeconds: detection.LastConnectionDurationSeconds,
			RemainingTTL:                  detection.RemainingTTL,
			TTLSeconds:                    detection.TTLSeconds,
			DetectedBySNI:                 detection.DetectedBySNI,
		})
	}

	if len(publicDetections) == 0 {
		return []publicAIActivityDetection{}
	}

	return publicDetections
}
