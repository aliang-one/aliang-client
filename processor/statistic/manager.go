package statistic

import (
	"sync"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
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

// NewTestManager 构造带全部计数器与空关闭历史的 Manager。导出供 handlers
// 等跨包测试注入独立实例（不带轮询协程,不写全局）。
func NewTestManager() *Manager {
	return &Manager{
		uploadTemp:    atomic.NewInt64(0),
		downloadTemp:  atomic.NewInt64(0),
		uploadBlip:    atomic.NewInt64(0),
		downloadBlip:  atomic.NewInt64(0),
		uploadTotal:   atomic.NewInt64(0),
		downloadTotal: atomic.NewInt64(0),
	}
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

	// closedMu/closedRecent 是已关闭代理连接的环形历史（状态页连接记录
	// 消费）：tracker Close 时把终态快照推入,写互斥 + 定长截断。只在
	// shouldTrackTraffic 的连接上发生(直连不入池也不入史,口径与活跃表
	// 一致),记录为无锁快照值,读侧零竞争。
	closedMu     sync.Mutex
	closedRecent []ClosedConnection
}

// closedHistoryCapacity 关闭历史的环形上限（条）。
const closedHistoryCapacity = 300

func (m *Manager) Join(c tracker) {
	m.connections.Store(c.ID(), c)
}

// recordClosed 把一条已关闭连接的终态快照推入环形历史（定长 300,超出
// 淘汰最旧）。由 tracker Close 路径调用,写少读多,单锁无竞争热点。
func (m *Manager) recordClosed(record ClosedConnection) {
	m.closedMu.Lock()
	m.closedRecent = append(m.closedRecent, record)
	if len(m.closedRecent) > closedHistoryCapacity {
		m.closedRecent = m.closedRecent[len(m.closedRecent)-closedHistoryCapacity:]
	}
	m.closedMu.Unlock()
}

// ClosedConnections 返回已关闭连接历史(新→旧,定长截断)。返回内部切片
// 的副本,调用方可自由处理。
func (m *Manager) ClosedConnections() []ClosedConnection {
	m.closedMu.Lock()
	defer m.closedMu.Unlock()
	records := make([]ClosedConnection, len(m.closedRecent))
	// 逆序拷贝:最新的在前,状态页直接按此顺序渲染。
	for i, record := range m.closedRecent {
		records[len(m.closedRecent)-1-i] = record
	}
	return records
}

// ActiveConnections 返回当前活跃代理连接明细(路由与累计流量口径与
// Snapshot.ByRoute 一致:RouteDirect 不入列表)。
func (m *Manager) ActiveConnections() []ActiveConnection {
	var records []ActiveConnection
	m.connections.Range(func(_, value any) bool {
		t, ok := value.(tracker)
		if !ok {
			return true
		}
		switch conn := t.(type) {
		case *tcpTracker:
			if conn == nil || conn.Metadata == nil || conn.Metadata.Route == "" || conn.Metadata.Route == "RouteDirect" {
				return true
			}
			records = append(records, newActiveConnection(conn.trackerInfo, conn.Metadata))
		case *udpTracker:
			if conn == nil || conn.Metadata == nil || conn.Metadata.Route == "" || conn.Metadata.Route == "RouteDirect" {
				return true
			}
			records = append(records, newActiveConnection(conn.trackerInfo, conn.Metadata))
		}
		return true
	})
	return records
}

func (m *Manager) Leave(c tracker) {
	m.connections.Delete(c.ID())
}

// CloseTrackedConnectionsByMetadata closes all tracked connections whose metadata
// matches the provided predicate. It returns the number of close attempts.
func (m *Manager) CloseTrackedConnectionsByMetadata(match func(*M.Metadata) bool) int {
	if m == nil || match == nil {
		return 0
	}

	targets := make([]tracker, 0)
	m.connections.Range(func(_, value any) bool {
		t, ok := value.(tracker)
		if !ok {
			return true
		}
		if metadata, ok := trackedConnectionMetadata(t); ok && match(metadata) {
			targets = append(targets, t)
		}
		return true
	})

	closed := 0
	for _, target := range targets {
		if target == nil {
			continue
		}
		_ = target.Close()
		closed++
	}
	return closed
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

func trackedConnectionMetadata(t tracker) (*M.Metadata, bool) {
	switch conn := t.(type) {
	case *tcpTracker:
		if conn != nil && conn.Metadata != nil {
			return conn.Metadata, true
		}
	case *udpTracker:
		if conn != nil && conn.Metadata != nil {
			return conn.Metadata, true
		}
	}
	return nil, false
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

// ActiveConnection 是一条活跃代理连接的明细快照（状态页连接记录表消费）。
// 字段为拷贝出的普通值,不含任何锁或 net.Conn 引用,可安全序列化。
type ActiveConnection struct {
	ID             string `json:"id"`             // 连接 UUID
	Network        string `json:"network"`        // tcp / udp
	Host           string `json:"host"`           // 目标主机名(无 SNI/DNS 绑定时为空)
	Destination    string `json:"destination"`    // 目标 addr:port
	Source         string `json:"source"`         // 来源 addr:port
	Route          string `json:"route"`          // RouteToALiang / RouteToSocks
	UploadTotal    int64  `json:"uploadTotal"`    // 累计上行字节
	DownloadTotal  int64  `json:"downloadTotal"`  // 累计下行字节
	StartUnix      int64  `json:"startUnix"`      // 连接开始时间(Unix 秒)
	DurationMillis int64  `json:"durationMillis"` // 已持续毫秒
}

// ClosedConnection 是一条已关闭连接的终态记录(环形历史,定长 300)。
type ClosedConnection struct {
	ActiveConnection
	EndUnix int64 `json:"endUnix"` // 连接关闭时间(Unix 秒)
}

// newActiveConnection 从 trackerInfo + metadata 拷贝出明细快照。
func newActiveConnection(info *trackerInfo, metadata *M.Metadata) ActiveConnection {
	network := "tcp"
	if metadata.Network == M.UDP {
		network = "udp"
	}
	return ActiveConnection{
		ID:             info.UUID.String(),
		Network:        network,
		Host:           metadata.HostName,
		Destination:    metadata.DestinationAddress(),
		Source:         metadata.SourceAddress(),
		Route:          metadata.Route,
		UploadTotal:    info.UploadTotal.Load(),
		DownloadTotal:  info.DownloadTotal.Load(),
		StartUnix:      info.Start.Unix(),
		DurationMillis: time.Since(info.Start).Milliseconds(),
	}
}
