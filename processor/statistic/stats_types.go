package statistic

import "time"

// TrafficStats 流量统计数据快照
type TrafficStats struct {
	Timestamp         int64  `json:"timestamp"`          // Unix时间戳(秒)
	ActiveConnections int32  `json:"active_connections"` // 当前活跃连接数
	UploadBytes       uint64 `json:"upload_bytes"`       // 上传流量(字节)
	DownloadBytes     uint64 `json:"download_bytes"`     // 下载流量(字节)
}

// StatsSnapshot API返回的统计数据快照响应
type StatsSnapshot struct {
	Timescale         string         `json:"timescale"`          // 时间尺度: "1s", "5s", "15s"
	ActiveConnections int32          `json:"active_connections"` // 当前活跃连接数
	Stats             []TrafficStats `json:"stats"`              // 统计数据列表(最多300条)
}

// CurrentStats 当前实时流量信息
type CurrentStats struct {
	Timestamp         int64  `json:"timestamp"`          // Unix时间戳(秒)
	ActiveConnections int32  `json:"active_connections"` // 活跃连接数
	UploadBytes       uint64 `json:"upload_bytes"`       // 总上传流量(字节)
	DownloadBytes     uint64 `json:"download_bytes"`     // 总下载流量(字节)
	UploadRate        uint64 `json:"upload_rate"`        // 上传速率(字节/秒)
	DownloadRate      uint64 `json:"download_rate"`      // 下载速率(字节/秒)
}

// Timescale 时间尺度类型
type Timescale string

const (
	Timescale1s  Timescale = "1s"
	Timescale5s  Timescale = "5s"
	Timescale15s Timescale = "15s"
)

// IsValid 检查时间尺度是否有效
func (t Timescale) IsValid() bool {
	switch t {
	case Timescale1s, Timescale5s, Timescale15s:
		return true
	default:
		return false
	}
}

// NewTrafficStats 创建新的流量统计快照
func NewTrafficStats(activeConns int32, uploadBytes, downloadBytes uint64) *TrafficStats {
	return &TrafficStats{
		Timestamp:         time.Now().Unix(),
		ActiveConnections: activeConns,
		UploadBytes:       uploadBytes,
		DownloadBytes:     downloadBytes,
	}
}
