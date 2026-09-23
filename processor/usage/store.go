package usage

import (
	"fmt"
	"time"

	"gorm.io/driver/sqlite" // cgo/mattn 驱动，go.mod 已有（processor/auth 同款）
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"aliang.one/nursorgate/common/cache"
)

// UsageWatermark 记录每个会话 JSONL 已解析到的字节偏移。重启后从断点续读，
// 防止全量重扫造成的数量级重复计数。
type UsageWatermark struct {
	FilePath  string    `gorm:"column:file_path;primaryKey"`
	Offset    int64     `gorm:"column:offset"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (UsageWatermark) TableName() string { return "usage_watermarks" }

// UsageBucket 是（本地小时 × 模型）聚合桶，累计快照语义：整行覆盖式
// 上报，服务端按 (device_id, hour_start, model) upsert，重复推送无副作用。
type UsageBucket struct {
	ID                  int64  `gorm:"primaryKey;autoIncrement"`
	HourStart           int64  `gorm:"column:hour_start;uniqueIndex:idx_usage_hour_model"`
	Model               string `gorm:"column:model;size:128;uniqueIndex:idx_usage_hour_model"`
	Requests            int64  `gorm:"column:requests"`
	InputTokens         int64  `gorm:"column:input_tokens"`
	OutputTokens        int64  `gorm:"column:output_tokens"`
	CacheReadTokens     int64  `gorm:"column:cache_read_tokens"`
	CacheCreationTokens int64  `gorm:"column:cache_creation_tokens"`
	ActiveSessions      int    `gorm:"column:active_sessions"`
	// SessionSet 是该小时内出现过的 sessionId 去重集（JSON 数组），随桶
	// 持久化，保证重启后同会话不重复计数。大小受"该小时活跃会话数"约束。
	SessionSet string `gorm:"column:session_set;type:text"`
	FirstSeen  int64  `gorm:"column:first_seen"`
	LastSeen   int64  `gorm:"column:last_seen"`
	Dirty      bool   `gorm:"column:dirty"`
}

func (UsageBucket) TableName() string { return "usage_buckets" }

// Store 是用量模块的本地 sqlite 存储（统一库 aliang.data，独立连接，
// 与 processor/auth 的连接并存——写频极低，秒级以下）。
type Store struct {
	db *gorm.DB
}

// OpenStoreAt 在指定路径建/开库（测试注入用）。
func OpenStoreAt(dbPath string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("usage: open store: %w", err)
	}
	if err := db.AutoMigrate(&UsageWatermark{}, &UsageBucket{}); err != nil {
		return nil, fmt.Errorf("usage: migrate store: %w", err)
	}
	return &Store{db: db}, nil
}

// OpenDefaultStore 打开生产路径的统一数据库。
func OpenDefaultStore() (*Store, error) {
	path, err := cache.GetUnifiedDataDBPath()
	if err != nil {
		return nil, fmt.Errorf("usage: resolve unified db path: %w", err)
	}
	return OpenStoreAt(path)
}

func (s *Store) GetWatermark(path string) (int64, error) {
	var wm UsageWatermark
	err := s.db.Where("file_path = ?", path).First(&wm).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("usage: get watermark: %w", err)
	}
	return wm.Offset, nil
}

func (s *Store) SetWatermark(path string, offset int64) error {
	return s.db.Save(&UsageWatermark{FilePath: path, Offset: offset, UpdatedAt: time.Now()}).Error
}

func (s *Store) DeleteWatermarks(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.db.Where("file_path IN ?", paths).Delete(&UsageWatermark{}).Error
}

// Bucket 取（或创建）聚合桶。返回的桶可修改后 SaveBucket 持久化。
func (s *Store) Bucket(hourStart int64, model string) (*UsageBucket, error) {
	var b UsageBucket
	err := s.db.Where("hour_start = ? AND model = ?", hourStart, model).First(&b).Error
	if err == nil {
		return &b, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("usage: load bucket: %w", err)
	}
	b = UsageBucket{HourStart: hourStart, Model: model}
	if err := s.db.Create(&b).Error; err != nil {
		return nil, fmt.Errorf("usage: create bucket: %w", err)
	}
	return &b, nil
}

// SaveBucket 持久化桶并标记为脏：凡有累计数据落库即待上报，
// 由 reporter 上报成功后 ClearDirty。
func (s *Store) SaveBucket(b *UsageBucket) error {
	b.Dirty = true
	return s.db.Save(b).Error
}

func (s *Store) DirtyBuckets() ([]UsageBucket, error) {
	var out []UsageBucket
	err := s.db.Where("dirty = ?", true).Order("hour_start ASC").Find(&out).Error
	return out, err
}

func (s *Store) AllBuckets() ([]UsageBucket, error) {
	var out []UsageBucket
	err := s.db.Order("hour_start ASC").Find(&out).Error
	return out, err
}

func (s *Store) ClearDirty(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Model(&UsageBucket{}).Where("id IN ?", ids).Update("dirty", false).Error
}

func (s *Store) allWatermarkPaths() ([]string, error) {
	var rows []UsageWatermark
	err := s.db.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.FilePath)
	}
	return out, nil
}
