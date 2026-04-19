package statistic

import (
	"sort"
	"strings"
	"sync"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

const (
	DefaultAIActivityTTL              = 15 * time.Second
	DefaultAIActivityVisibilityWindow = 10 * time.Minute
)

var (
	defaultAIActivityTracker     *AIActivityTracker
	defaultAIActivityTrackerOnce sync.Once
)

type AIActivityTracker struct {
	mu               sync.RWMutex
	ttl              time.Duration
	visibilityWindow time.Duration
	detections       map[string]*AIActivityDetection
	totalHits        int64
	latestSeenAt     time.Time
	latestProvider   string
	latestLabel      string
	latestDomain     string
	latestHost       string
	latestSource     string
	latestRoute      string
	latestMatchedVia string
}

type AIActivityDetection struct {
	ProviderKey   string    `json:"providerKey"`
	ProviderLabel string    `json:"providerLabel"`
	Domain        string    `json:"domain"`
	RecentHost    string    `json:"recentHost"`
	Source        string    `json:"source"`
	Route         string    `json:"route"`
	MatchedVia    string    `json:"matchedVia"`
	LastSeenAt    time.Time `json:"lastSeenAt"`
	LastSeenUnix  int64     `json:"lastSeenUnix"`
	HitCount      int64     `json:"hitCount"`
	Active        bool      `json:"active"`
	RemainingTTL  int64     `json:"remainingTtlSeconds"`
	TTLSeconds    int64     `json:"ttlSeconds"`
	DetectedBySNI bool      `json:"detectedBySNI"`
}

type AIActivitySummary struct {
	Active                bool                   `json:"active"`
	ActiveCount           int                    `json:"activeCount"`
	TTLSeconds            int64                  `json:"ttlSeconds"`
	VisibleWindowSeconds  int64                  `json:"visibleWindowSeconds"`
	TotalHits             int64                  `json:"totalHits"`
	LastSeenAt            int64                  `json:"lastSeenAt"`
	LastProvider          string                 `json:"lastProvider,omitempty"`
	LastLabel             string                 `json:"lastLabel,omitempty"`
	LastDomain            string                 `json:"lastDomain,omitempty"`
	LastHost              string                 `json:"lastHost,omitempty"`
	LastSource            string                 `json:"lastSource,omitempty"`
	LastRoute             string                 `json:"lastRoute,omitempty"`
	LastMatchedVia        string                 `json:"lastMatchedVia,omitempty"`
	DetectedBySNI         bool                   `json:"detectedBySNI"`
	ActiveDetections      []*AIActivityDetection `json:"activeDetections"`
	RecentProviderTraffic []*AIActivityDetection `json:"recentProviderTraffic"`
	TrackedPatterns       []string               `json:"trackedPatterns"`
}

func GetDefaultAIActivityTracker() *AIActivityTracker {
	defaultAIActivityTrackerOnce.Do(func() {
		defaultAIActivityTracker = NewAIActivityTracker(DefaultAIActivityTTL)
	})
	return defaultAIActivityTracker
}

func NewAIActivityTracker(ttl time.Duration) *AIActivityTracker {
	if ttl <= 0 {
		ttl = DefaultAIActivityTTL
	}

	return &AIActivityTracker{
		ttl:              ttl,
		visibilityWindow: maxDuration(DefaultAIActivityVisibilityWindow, ttl),
		detections:       make(map[string]*AIActivityDetection),
	}
}

func (t *AIActivityTracker) RecordMetadata(metadata *M.Metadata) {
	if metadata == nil || metadata.HostName == "" {
		return
	}
	if metadata.Route == "" || metadata.Route == "RouteDirect" {
		return
	}

	provider, matchedDomain, ok := matchTrackedAIProvider(metadata.HostName)
	if !ok {
		return
	}

	source := "unknown"
	if metadata.DNSInfo != nil && metadata.DNSInfo.BindingSource != "" {
		source = string(metadata.DNSInfo.BindingSource)
	}

	t.RecordDetection(provider, matchedDomain, metadata.HostName, source, metadata.Route, time.Now())
}

func (t *AIActivityTracker) RecordDetection(provider trackedAIProvider, matchedDomain, host, source, route string, seenAt time.Time) {
	providerKey := strings.TrimSpace(provider.Key)
	providerLabel := strings.TrimSpace(provider.Label)
	normalizedDomain := normalizeAIDomainPattern(matchedDomain)
	normalizedHost := normalizeAIDomainHost(host)
	if providerKey == "" || providerLabel == "" || normalizedDomain == "" || normalizedHost == "" {
		return
	}
	if seenAt.IsZero() {
		seenAt = time.Now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneExpiredLocked(seenAt)

	detection, exists := t.detections[providerKey]
	if !exists {
		detection = &AIActivityDetection{ProviderKey: providerKey}
		t.detections[providerKey] = detection
	}

	detection.ProviderKey = providerKey
	detection.ProviderLabel = providerLabel
	detection.Domain = normalizedDomain
	detection.RecentHost = normalizedHost
	detection.Source = source
	detection.Route = route
	detection.MatchedVia = normalizedDomain
	detection.LastSeenAt = seenAt
	detection.LastSeenUnix = seenAt.Unix()
	detection.HitCount++
	detection.TTLSeconds = int64(t.ttl / time.Second)
	detection.DetectedBySNI = source == string(M.BindingSourceSNI)

	t.totalHits++
	t.latestSeenAt = seenAt
	t.latestProvider = providerKey
	t.latestLabel = providerLabel
	t.latestDomain = normalizedDomain
	t.latestHost = normalizedHost
	t.latestSource = source
	t.latestRoute = route
	t.latestMatchedVia = normalizedDomain
}

func (t *AIActivityTracker) Summary() *AIActivitySummary {
	return t.SummaryAt(time.Now())
}

func (t *AIActivityTracker) SummaryAt(now time.Time) *AIActivitySummary {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneExpiredLocked(now)

	recent := make([]*AIActivityDetection, 0, len(t.detections))
	for _, detection := range t.detections {
		copy := *detection
		remaining := detection.LastSeenAt.Add(t.ttl).Sub(now)
		visibleRemaining := detection.LastSeenAt.Add(t.visibilityWindow).Sub(now)
		if visibleRemaining <= 0 {
			continue
		}

		copy.Active = remaining > 0
		copy.RemainingTTL = ttlSecondsCeil(remaining)
		copy.TTLSeconds = int64(t.ttl / time.Second)
		recent = append(recent, &copy)
	}

	sort.Slice(recent, func(i, j int) bool {
		if recent[i].LastSeenAt.Equal(recent[j].LastSeenAt) {
			return recent[i].ProviderLabel < recent[j].ProviderLabel
		}
		return recent[i].LastSeenAt.After(recent[j].LastSeenAt)
	})

	active := make([]*AIActivityDetection, 0, len(recent))
	for _, detection := range recent {
		if detection.Active {
			active = append(active, detection)
		}
	}

	return &AIActivitySummary{
		Active:                len(active) > 0,
		ActiveCount:           len(active),
		TTLSeconds:            int64(t.ttl / time.Second),
		VisibleWindowSeconds:  int64(t.visibilityWindow / time.Second),
		TotalHits:             t.totalHits,
		LastSeenAt:            t.latestSeenAt.Unix(),
		LastProvider:          t.latestProvider,
		LastLabel:             t.latestLabel,
		LastDomain:            t.latestDomain,
		LastHost:              t.latestHost,
		LastSource:            t.latestSource,
		LastRoute:             t.latestRoute,
		LastMatchedVia:        t.latestMatchedVia,
		DetectedBySNI:         t.latestSource == string(M.BindingSourceSNI),
		ActiveDetections:      active,
		RecentProviderTraffic: recent,
		TrackedPatterns:       currentTrackedAIDomains(),
	}
}

func (t *AIActivityTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.detections = make(map[string]*AIActivityDetection)
	t.totalHits = 0
	t.latestSeenAt = time.Time{}
	t.latestProvider = ""
	t.latestLabel = ""
	t.latestDomain = ""
	t.latestHost = ""
	t.latestSource = ""
	t.latestRoute = ""
	t.latestMatchedVia = ""
}

func (t *AIActivityTracker) pruneExpiredLocked(now time.Time) {
	cutoff := now.Add(-t.visibilityWindow)
	for key, detection := range t.detections {
		if detection.LastSeenAt.Before(cutoff) {
			delete(t.detections, key)
		}
	}
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func ttlSecondsCeil(remaining time.Duration) int64 {
	if remaining <= 0 {
		return 0
	}
	return int64((remaining + time.Second - 1) / time.Second)
}
