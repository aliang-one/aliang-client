package statistic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

var (
	defaultHTTPStatsCollector     *HTTPStatsCollector
	defaultHTTPStatsCollectorOnce sync.Once
)

func GetDefaultHTTPStatsCollector() *HTTPStatsCollector {
	defaultHTTPStatsCollectorOnce.Do(func() {
		defaultHTTPStatsCollector = NewHTTPStatsCollector()
	})
	return defaultHTTPStatsCollector
}

type TrafficChartBuffer struct {
	data     []*TrafficDataPoint
	capacity int
	head     int
	tail     int
	count    int
	mu       sync.RWMutex
}

func NewTrafficChartBuffer(capacity int) *TrafficChartBuffer {
	return &TrafficChartBuffer{
		data:     make([]*TrafficDataPoint, capacity),
		capacity: capacity,
		head:     0,
		tail:     0,
		count:    0,
	}
}

func (rb *TrafficChartBuffer) Push(item *TrafficDataPoint) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.data[rb.head] = item
	rb.head = (rb.head + 1) % rb.capacity

	if rb.count < rb.capacity {
		rb.count++
	} else {
		rb.tail = (rb.tail + 1) % rb.capacity
	}
}

func (rb *TrafficChartBuffer) GetAll() []TrafficDataPoint {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.count == 0 {
		return []TrafficDataPoint{}
	}

	result := make([]TrafficDataPoint, rb.count)
	for i := 0; i < rb.count; i++ {
		idx := (rb.tail + i) % rb.capacity
		if rb.data[idx] != nil {
			result[i] = *rb.data[idx]
		}
	}

	return result
}

func (rb *TrafficChartBuffer) GetSince(since int64) []TrafficDataPoint {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.count == 0 {
		return []TrafficDataPoint{}
	}

	var result []TrafficDataPoint
	for i := 0; i < rb.count; i++ {
		idx := (rb.tail + i) % rb.capacity
		if rb.data[idx] != nil && rb.data[idx].Timestamp >= since {
			result = append(result, *rb.data[idx])
		}
	}

	return result
}

func (rb *TrafficChartBuffer) Size() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

func (rb *TrafficChartBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.data = make([]*TrafficDataPoint, rb.capacity)
	rb.head = 0
	rb.tail = 0
	rb.count = 0
}

type HTTPStatsCollector struct {
	requestCache    *HTTPRequestCache
	domainTracker   *DomainStatsTracker
	tokenCounter    *TokenCounter
	trafficChart    *TrafficChartBuffer
	chartInterval   time.Duration
	chartCapacity   int
	currentInterval *TrafficDataPoint
	intervalStart   time.Time
	mu              sync.RWMutex
	stopChan        chan struct{}
	started         bool
	totalUpload     int64
	totalDownload   int64
	totalRequests   int64
	totalResponses  int64
	totalDurationMs int64
	totalFirstMs    int64
}

const (
	ChartInterval15s   = 15 * time.Second
	ChartCapacity1Hour = 240
	RequestCacheSize   = 100
)

func NewHTTPStatsCollector() *HTTPStatsCollector {
	return &HTTPStatsCollector{
		requestCache:    NewHTTPRequestCache(RequestCacheSize),
		domainTracker:   NewDomainStatsTracker(),
		tokenCounter:    NewTokenCounter(),
		trafficChart:    NewTrafficChartBuffer(ChartCapacity1Hour),
		chartInterval:   ChartInterval15s,
		chartCapacity:   ChartCapacity1Hour,
		currentInterval: &TrafficDataPoint{},
		intervalStart:   time.Now(),
		stopChan:        make(chan struct{}),
		started:         false,
	}
}

func (c *HTTPStatsCollector) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return nil
	}

	c.started = true
	go c.runIntervalCollector()

	return nil
}

func (c *HTTPStatsCollector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return
	}

	close(c.stopChan)
	c.started = false
}

func (c *HTTPStatsCollector) runIntervalCollector() {
	ticker := time.NewTicker(c.chartInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.flushInterval()
		}
	}
}

func (c *HTTPStatsCollector) flushInterval() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.currentInterval.Timestamp = c.intervalStart.Unix()
	c.trafficChart.Push(c.currentInterval)

	c.currentInterval = &TrafficDataPoint{}
	c.intervalStart = time.Now()
}

func (c *HTTPStatsCollector) RecordRequest(record *HTTPRequestRecord) {
	if record == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.requestCache.Add(record)
	c.domainTracker.RecordRequest(record)

	c.currentInterval.UploadBytes += record.UploadBytes
	c.currentInterval.DownloadBytes += record.DownloadBytes
	c.currentInterval.InputTokens += int64(record.InputTokens)
	c.currentInterval.OutputTokens += int64(record.OutputTokens)
	c.totalUpload += record.UploadBytes
	c.totalDownload += record.DownloadBytes
	requestCount := int64(record.RequestCount)
	if requestCount <= 0 {
		requestCount = 1
	}
	c.currentInterval.RequestCount += requestCount
	c.currentInterval.ResponseCount += int64(record.ResponseCount)
	c.totalRequests += requestCount
	c.totalResponses += int64(record.ResponseCount)
	c.totalDurationMs += record.Duration().Milliseconds()
	c.totalFirstMs += record.FirstResponseLatency().Milliseconds()
}

func (c *HTTPStatsCollector) GetRequestRecords(limit int) []*HTTPRequestRecord {
	return c.requestCache.GetRecent(limit)
}

func (c *HTTPStatsCollector) GetDomainStats() []*DomainStats {
	return c.domainTracker.GetAllStats()
}

func (c *HTTPStatsCollector) GetDomainStatsFor(domain string) *DomainStats {
	return c.domainTracker.GetStats(domain)
}

func (c *HTTPStatsCollector) GetTrafficChartData(since time.Time) []TrafficDataPoint {
	return c.trafficChart.GetSince(since.Unix())
}

func (c *HTTPStatsCollector) GetTrafficChartDataForDuration(duration time.Duration) []TrafficDataPoint {
	since := time.Now().Add(-duration)
	return c.GetTrafficChartData(since)
}

func (c *HTTPStatsCollector) GetTokenCounter() *TokenCounter {
	return c.tokenCounter
}

func (c *HTTPStatsCollector) ClearAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requestCache.Clear()
	c.domainTracker.Reset()
	c.trafficChart.Clear()
	c.currentInterval = &TrafficDataPoint{}
	c.intervalStart = time.Now()
	c.totalRequests = 0
	c.totalResponses = 0
	c.totalUpload = 0
	c.totalDownload = 0
	c.totalDurationMs = 0
	c.totalFirstMs = 0
}

func (c *HTTPStatsCollector) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	avgDuration := int64(0)
	avgFirst := int64(0)
	if c.totalRequests > 0 {
		avgDuration = c.totalDurationMs / c.totalRequests
		avgFirst = c.totalFirstMs / c.totalRequests
	}

	return map[string]interface{}{
		"requestCacheSize":   c.requestCache.Size(),
		"chartDataPoints":    c.trafficChart.Size(),
		"totalRequests":      c.totalRequests,
		"totalResponses":     c.totalResponses,
		"totalUploadBytes":   c.totalUpload,
		"totalDownloadBytes": c.totalDownload,
		"totalTrafficBytes":  c.totalUpload + c.totalDownload,
		"totalDurationMs":    c.totalDurationMs,
		"totalFirstRespMs":   c.totalFirstMs,
		"avgDurationMs":      avgDuration,
		"avgFirstRespMs":     avgFirst,
		"chartInterval":      c.chartInterval.String(),
		"chartCapacity":      c.chartCapacity,
	}
}

func (c *HTTPStatsCollector) RecordConnection(metadata *M.Metadata, requestPayload, responsePayload []byte, uploadBytes, downloadBytes int64, startedAt, firstResponseAt, completedAt time.Time) {
	if metadata == nil {
		return
	}

	record := &HTTPRequestRecord{
		Timestamp:          startedAt,
		RequestCount:       1,
		Host:               metadata.HostName,
		UploadBytes:        uploadBytes,
		DownloadBytes:      downloadBytes,
		FirstResponseAt:    firstResponseAt,
		CompleteResponseAt: completedAt,
		Route:              metadata.Route,
	}
	if !firstResponseAt.IsZero() || len(responsePayload) > 0 || downloadBytes > 0 {
		record.ResponseCount = 1
	}

	if record.Host == "" && metadata.DstIP.IsValid() {
		record.Host = metadata.DstIP.String()
	}

	method, path, modelFromReq := parseRequestInfo(requestPayload)
	if method != "" {
		record.Method = method
	}
	if path != "" {
		record.Path = path
	}

	statusCode, responseBody := parseResponseInfo(responsePayload)
	if statusCode > 0 {
		record.StatusCode = statusCode
	}

	if usage, model := c.tokenCounter.ParseTokenUsage(record.Host, responseBody); usage != nil {
		record.InputTokens = usage.InputTokens
		record.OutputTokens = usage.OutputTokens
		record.TotalTokens = usage.TotalTokens
		if model != "" {
			record.Model = model
		}
	}

	if record.Model == "" {
		record.Model = modelFromReq
	}

	c.RecordRequest(record)
}

func parseRequestInfo(requestPayload []byte) (method string, path string, model string) {
	if len(requestPayload) == 0 {
		return "", "", ""
	}

	req, err := http.ReadRequest(bufioReaderFromBytes(requestPayload))
	if err != nil {
		fallbackMethod, fallbackPath := parseRequestLineFallback(requestPayload)
		fallbackModel := parseModelFromBody(extractLikelyJSON(requestPayload))
		return fallbackMethod, fallbackPath, fallbackModel
	}
	defer req.Body.Close()

	method = req.Method
	if req.URL != nil {
		path = req.URL.Path
	}

	body, _ := readBodyBytes(req.Body)
	if len(body) > 0 {
		model = parseModelFromBody(body)
	}

	return method, path, model
}

func parseResponseInfo(responsePayload []byte) (statusCode int, body []byte) {
	if len(responsePayload) == 0 {
		return 0, nil
	}

	resp, err := http.ReadResponse(bufioReaderFromBytes(responsePayload), nil)
	if err != nil {
		return parseStatusCodeFallback(responsePayload), extractLikelyJSON(responsePayload)
	}
	defer resp.Body.Close()

	body, _ = readBodyBytes(resp.Body)
	return resp.StatusCode, body
}

func parseRequestLineFallback(payload []byte) (method string, path string) {
	lineEnd := bytes.Index(payload, []byte("\r\n"))
	if lineEnd <= 0 {
		lineEnd = bytes.IndexByte(payload, '\n')
	}
	if lineEnd <= 0 {
		return "", ""
	}

	parts := strings.Fields(string(payload[:lineEnd]))
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func parseStatusCodeFallback(payload []byte) int {
	lineEnd := bytes.Index(payload, []byte("\r\n"))
	if lineEnd <= 0 {
		lineEnd = bytes.IndexByte(payload, '\n')
	}
	if lineEnd <= 0 {
		return 0
	}

	parts := strings.Fields(string(payload[:lineEnd]))
	if len(parts) < 2 {
		return 0
	}

	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return code
}

func parseModelFromBody(body []byte) string {
	var modelReq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &modelReq); err != nil {
		return ""
	}
	return modelReq.Model
}

func bufioReaderFromBytes(data []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(data))
}

func readBodyBytes(body io.Reader) ([]byte, error) {
	const maxBodySize = 1024 * 1024
	limited := io.LimitReader(body, maxBodySize)
	return io.ReadAll(limited)
}

func extractLikelyJSON(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}

	start := bytes.IndexByte(payload, '{')
	end := bytes.LastIndexByte(payload, '}')
	if start == -1 || end == -1 || end <= start {
		return nil
	}

	candidate := payload[start : end+1]
	if json.Valid(candidate) {
		return candidate
	}

	trimmed := strings.TrimSpace(string(candidate))
	if trimmed == "" {
		return nil
	}

	if json.Valid([]byte(trimmed)) {
		return []byte(trimmed)
	}

	return nil
}
