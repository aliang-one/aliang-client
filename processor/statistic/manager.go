package statistic

import (
	"sync"
	"time"

	"go.uber.org/atomic"
)

var DefaultManager *Manager

func init() {
	DefaultManager = &Manager{
		stopCh:        make(chan struct{}),
		uploadTemp:    atomic.NewInt64(0),
		downloadTemp:  atomic.NewInt64(0),
		uploadBlip:    atomic.NewInt64(0),
		downloadBlip:  atomic.NewInt64(0),
		uploadTotal:   atomic.NewInt64(0),
		downloadTotal: atomic.NewInt64(0),
	}
	go DefaultManager.handle()
}

type Manager struct {
	connections   sync.Map
	stopCh        chan struct{}
	stopped       atomic.Bool
	uploadTemp    *atomic.Int64
	downloadTemp  *atomic.Int64
	uploadBlip    *atomic.Int64
	downloadBlip  *atomic.Int64
	uploadTotal   *atomic.Int64
	downloadTotal *atomic.Int64
}

func (m *Manager) Join(c tracker) {
	m.connections.Store(c.ID(), c)
}

func (m *Manager) Leave(c tracker) {
	m.connections.Delete(c.ID())
}

func (m *Manager) PushUploaded(size int64) {
	m.uploadTemp.Add(size)
	m.uploadTotal.Add(size)
}

func (m *Manager) PushDownloaded(size int64) {
	m.downloadTemp.Add(size)
	m.downloadTotal.Add(size)
}

func (m *Manager) Now() (up int64, down int64) {
	return m.uploadBlip.Load(), m.downloadBlip.Load()
}

func (m *Manager) Snapshot() *Snapshot {
	var connections []tracker
	byRoute := make(map[string]*RouteStats)

	// Initialize route statistics for tracked proxy routes only.
	routes := []string{"RouteToALiang", "RouteToSocks"}
	for _, route := range routes {
		byRoute[route] = &RouteStats{
			RouteType: route,
		}
	}

	m.connections.Range(func(key, value any) bool {
		t := value.(tracker)
		connections = append(connections, t)

		// Access tracker's metadata and statistics
		// We need to assert the concrete type to access trackerInfo
		var route string
		var upload, download int64

		if tcpT, ok := t.(*tcpTracker); ok {
			route = tcpT.Metadata.Route
			if route == "" || route == "RouteDirect" {
				return true
			}
			upload = tcpT.UploadTotal.Load()
			download = tcpT.DownloadTotal.Load()
		} else if udpT, ok := t.(*udpTracker); ok {
			route = udpT.Metadata.Route
			if route == "" || route == "RouteDirect" {
				return true
			}
			upload = udpT.UploadTotal.Load()
			download = udpT.DownloadTotal.Load()
		}

		// Accumulate statistics for this route
		if stats, exists := byRoute[route]; exists {
			stats.ConnectionCount++
			stats.UploadTotal += upload
			stats.DownloadTotal += download
		} else {
			// If route not in predefined list, add it
			byRoute[route] = &RouteStats{
				RouteType:       route,
				ConnectionCount: 1,
				UploadTotal:     upload,
				DownloadTotal:   download,
			}
		}

		return true
	})

	// Calculate averages for each route
	for _, stats := range byRoute {
		if stats.ConnectionCount > 0 {
			stats.AverageUpload = stats.UploadTotal / int64(stats.ConnectionCount)
			stats.AverageDownload = stats.DownloadTotal / int64(stats.ConnectionCount)
		}
	}

	return &Snapshot{
		UploadTotal:   m.uploadTotal.Load(),
		DownloadTotal: m.downloadTotal.Load(),
		Connections:   connections,
		ByRoute:       byRoute,
	}
}

func (m *Manager) ResetStatistic() {
	m.uploadTemp.Store(0)
	m.uploadBlip.Store(0)
	m.uploadTotal.Store(0)
	m.downloadTemp.Store(0)
	m.downloadBlip.Store(0)
	m.downloadTotal.Store(0)
}

func (m *Manager) handle() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.uploadBlip.Store(m.uploadTemp.Swap(0))
			m.downloadBlip.Store(m.downloadTemp.Swap(0))
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) Stop() {
	if m.stopped.CompareAndSwap(false, true) {
		close(m.stopCh)
	}
}

// RouteStats holds statistics for a specific route type
type RouteStats struct {
	RouteType       string `json:"routeType"`       // "RouteToALiang", "RouteToSocks", "RouteDirect"
	ConnectionCount int    `json:"connectionCount"` // Number of connections
	UploadTotal     int64  `json:"uploadTotal"`     // Total upload bytes
	DownloadTotal   int64  `json:"downloadTotal"`   // Total download bytes
	AverageUpload   int64  `json:"averageUpload"`   // Average upload per connection
	AverageDownload int64  `json:"averageDownload"` // Average download per connection
}

type Snapshot struct {
	DownloadTotal int64                  `json:"downloadTotal"`
	UploadTotal   int64                  `json:"uploadTotal"`
	Connections   []tracker              `json:"connections"`
	ByRoute       map[string]*RouteStats `json:"byRoute"` // Statistics grouped by route type
}
