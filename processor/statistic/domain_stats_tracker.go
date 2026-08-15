package statistic

import (
	"sync"
	"time"
)

type DomainStatsTracker struct {
	stats        map[string]*DomainStats
	totalReq     int64
	totalTraffic int64
	mu           sync.RWMutex
}

func NewDomainStatsTracker() *DomainStatsTracker {
	tracker := &DomainStatsTracker{
		stats: make(map[string]*DomainStats),
	}

	tracker.ensureTrackedDomainsLocked()

	return tracker
}

func (t *DomainStatsTracker) RecordRequest(record *HTTPRequestRecord) {
	if record == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.ensureTrackedDomainsLocked()

	matchedDomain := matchTrackedAIDomain(record.Host)
	if matchedDomain == "" {
		return
	}

	stats, exists := t.stats[matchedDomain]
	if !exists {
		stats = &DomainStats{Domain: matchedDomain}
		t.stats[matchedDomain] = stats
	}

	stats.RequestCount++
	stats.ResponseCount += int64(record.ResponseCount)
	stats.UploadBytes += record.UploadBytes
	stats.DownloadBytes += record.DownloadBytes
	stats.InputTokens += int64(record.InputTokens)
	stats.OutputTokens += int64(record.OutputTokens)

	if record.StatusCode >= 200 && record.StatusCode < 300 {
		stats.SuccessCount++
	} else if record.StatusCode >= 400 {
		stats.ErrorCount++
	}

	latency := record.Duration().Milliseconds()
	if stats.RequestCount > 1 {
		stats.AvgLatency = ((stats.AvgLatency * float64(stats.RequestCount-1)) + float64(latency)) / float64(stats.RequestCount)
	} else {
		stats.AvgLatency = float64(latency)
	}

	t.totalReq++
	t.totalTraffic += record.UploadBytes + record.DownloadBytes
}
func (t *DomainStatsTracker) GetStats(domain string) *DomainStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if stats, exists := t.stats[domain]; exists {
		return t.copyStatsWithShares(stats)
	}
	return nil
}

func (t *DomainStatsTracker) GetAllStats() []*DomainStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ensureTrackedDomainsLocked()

	result := make([]*DomainStats, 0, len(t.stats))
	for _, stats := range t.stats {
		result = append(result, t.copyStatsWithShares(stats))
	}
	return result
}

func (t *DomainStatsTracker) copyStatsWithShares(stats *DomainStats) *DomainStats {
	copy := *stats

	if t.totalTraffic > 0 {
		copy.TrafficShare = float64(stats.UploadBytes+stats.DownloadBytes) / float64(t.totalTraffic) * 100
	}
	if t.totalReq > 0 {
		copy.RequestShare = float64(stats.RequestCount) / float64(t.totalReq) * 100
	}

	return &copy
}

func (t *DomainStatsTracker) GetTotals() (totalReq int64, totalTraffic int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalReq, t.totalTraffic
}

func (t *DomainStatsTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stats = make(map[string]*DomainStats)
	t.ensureTrackedDomainsLocked()
	t.totalReq = 0
	t.totalTraffic = 0
}

func (t *DomainStatsTracker) GetStatsSince(since time.Time) []*DomainStats {
	return t.GetAllStats()
}

func (t *DomainStatsTracker) ensureTrackedDomainsLocked() {
	for _, domain := range currentTrackedAIDomains() {
		if _, exists := t.stats[domain]; !exists {
			t.stats[domain] = &DomainStats{Domain: domain}
		}
	}
}
