package statistic

import (
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// StatsCollector 流量统计收集器
// 定期从Manager收集流量数据并存储到多个时间维度的缓存中
type StatsCollector struct {
	cache1s  *RingBuffer
	cache5s  *RingBuffer
	cache15s *RingBuffer

	statManager *Manager
	mu          sync.RWMutex
	stopChan    chan struct{}
	started     bool

	// 用于5s和15s聚合的计数器
	count5s  int
	count15s int
}

// NewStatsCollector 创建新的统计收集器实例
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{
		cache1s:     NewRingBuffer(300),
		cache5s:     NewRingBuffer(300),
		cache15s:    NewRingBuffer(300),
		statManager: DefaultManager,
		stopChan:    make(chan struct{}),
		started:     false,
		count5s:     0,
		count15s:    0,
	}
}

// Start 启动统计收集器后台任务
func (c *StatsCollector) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		logger.Warn("StatsCollector already started")
		return nil
	}

	c.started = true
	logger.Info("Starting StatsCollector background tasks...")

	// 启动1秒收集任务
	go c.collectEvery1Second()

	return nil
}

// Stop 停止统计收集器
func (c *StatsCollector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return
	}

	close(c.stopChan)
	c.started = false
	logger.Info("StatsCollector stopped")
}

// collectEvery1Second 每秒收集一次统计数据
func (c *StatsCollector) collectEvery1Second() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.collectStats()
		}
	}
}

// collectStats 收集当前统计数据
func (c *StatsCollector) collectStats() {
	snapshot := c.statManager.Snapshot()
	if snapshot == nil {
		return
	}

	// 创建TrafficStats对象
	stats := NewTrafficStats(
		int32(len(snapshot.Connections)),
		uint64(snapshot.UploadTotal),
		uint64(snapshot.DownloadTotal),
	)

	// 推送到1秒缓存
	c.cache1s.Push(stats)

	// 计数器增加
	c.count5s++
	c.count15s++

	// 每5秒推送到5秒缓存
	if c.count5s >= 5 {
		c.cache5s.Push(stats)
		c.count5s = 0
	}

	// 每15秒推送到15秒缓存
	if c.count15s >= 15 {
		c.cache15s.Push(stats)
		c.count15s = 0
	}
}

// GetStats 获取指定时间尺度的统计数据快照
func (c *StatsCollector) GetStats(timescale Timescale) (*StatsSnapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var cache *RingBuffer
	switch timescale {
	case Timescale1s:
		cache = c.cache1s
	case Timescale5s:
		cache = c.cache5s
	case Timescale15s:
		cache = c.cache15s
	default:
		return nil, nil
	}

	stats := cache.GetAll()

	// 获取当前活跃连接数
	var activeConns int32
	if latest := cache.GetLatest(); latest != nil {
		activeConns = latest.ActiveConnections
	}

	return &StatsSnapshot{
		Timescale:         string(timescale),
		ActiveConnections: activeConns,
		Stats:             stats,
	}, nil
}

// GetCurrent 获取当前实时流量信息
func (c *StatsCollector) GetCurrent() *CurrentStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := c.statManager.Snapshot()
	if snapshot == nil {
		return &CurrentStats{
			Timestamp:         time.Now().Unix(),
			ActiveConnections: 0,
			UploadBytes:       0,
			DownloadBytes:     0,
			UploadRate:        0,
			DownloadRate:      0,
		}
	}

	// 获取当前速率
	uploadRate, downloadRate := c.statManager.Now()

	return &CurrentStats{
		Timestamp:         time.Now().Unix(),
		ActiveConnections: int32(len(snapshot.Connections)),
		UploadBytes:       uint64(snapshot.UploadTotal),
		DownloadBytes:     uint64(snapshot.DownloadTotal),
		UploadRate:        uint64(uploadRate),
		DownloadRate:      uint64(downloadRate),
	}
}

// GetCacheSize 获取缓存大小（用于调试）
func (c *StatsCollector) GetCacheSize() (size1s, size5s, size15s int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache1s.Size(), c.cache5s.Size(), c.cache15s.Size()
}

// ClearCache 清空所有缓存
func (c *StatsCollector) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache1s.Clear()
	c.cache5s.Clear()
	c.cache15s.Clear()
	c.count5s = 0
	c.count15s = 0

	logger.Debug("All stats caches cleared")
}
