package models

import "time"

// QuickSetupCombo 是一个配置组合（套餐）的存储行：变量值 + 文件模板。
// VariablesJSON/FilesJSON 以 JSON 文本列持久化（Store 层负责序列化，全仓无 serializer:json 先例）；
// Variables/Files 是运行时反序列化视图（gorm:"-" 不落库），由 Store 读写时双向转换。
type QuickSetupCombo struct {
	ID        int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Software  string `json:"software" gorm:"type:varchar(64);not null;uniqueIndex:idx_combo_sw_name"`
	Name      string `json:"name" gorm:"type:varchar(128);not null;uniqueIndex:idx_combo_sw_name"`
	IsDefault bool   `json:"is_default" gorm:"not null;default:false"`
	// VariablesJSON/FilesJSON 为持久化 JSON 文本列。
	VariablesJSON string `json:"-" gorm:"type:text;not null"`
	FilesJSON     string `json:"-" gorm:"type:text;not null"`
	// AppliedJSON/AppliedAt 是「上次应用快照」（v3.1，combo 级）：apply 全部成功后
	// 由 service 写入本次实际落盘的每文件 [{code,content}] 与 RFC3339 时间戳。
	// default:'' 兼容存量表 AutoMigrate（SQLite 加 NOT NULL 列必须有默认值）。
	AppliedJSON string `json:"-" gorm:"type:text;not null;default:''"`
	AppliedAt   string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// Variables/Files/Applied 为运行时字段，不映射数据库列。
	Variables map[string]string     `json:"variables" gorm:"-"`
	Files     []QuickSetupComboFile `json:"files" gorm:"-"`
	Applied   []QuickSetupComboFile `json:"applied" gorm:"-"`
	CreatedAt time.Time             `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt time.Time             `json:"updated_at" gorm:"autoUpdateTime"`
}

func (QuickSetupCombo) TableName() string {
	return "quick_setup_combos"
}

// QuickSetupComboFile 是组合内的一个文件模板，Code 对应 software 声明的 file code。
type QuickSetupComboFile struct {
	Code    string `json:"code"`
	Content string `json:"content"` // 含 {{base_url}}/{{api_key}}/{{model}} 占位符
}

// QuickSetupComboView 是组合的 API 视图（Variables/Files/Applied 已反序列化）。
type QuickSetupComboView struct {
	ID        int64                 `json:"id"`
	Software  string                `json:"software"`
	Name      string                `json:"name"`
	IsDefault bool                  `json:"is_default"`
	Variables map[string]string     `json:"variables"`
	Files     []QuickSetupComboFile `json:"files"`
	// Applied/AppliedAt 是「上次应用快照」（v3.1）：从未应用过 → Applied 空 slice、
	// AppliedAt 空串。
	Applied   []QuickSetupComboFile `json:"applied"`
	AppliedAt string                `json:"applied_at"`
}
