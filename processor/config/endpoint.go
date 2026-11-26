package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// LLMEndpoint 是 56432 多 endpoint 聚合路由的端点注册表行(spec 2026-11-25 §5.1)。
// 与 agentcore_llm(端点连接形态)分开:本表是「有哪些可用的 LLM 入口」的路由池,
// 56432 在两者之间做选择;字段语义见设计文档。
//
// 时间戳用 unix 秒 int64:字段名沿用 CreatedAt/UpdatedAt 让 gorm 自动填充
// (int 字段填 unix 秒,与 spec 口径一致),store 层不再手动赋值。
// ⚠️ 这两个字段的 tag 不能带 default:0——gorm 对「带 default 且值为零」的字段
// 走「填 DB default 值」分支,autoCreateTime 的填充分支永远走不到,落库恒 0
// (2026-11-25 评审实证),所以只留 not null。
type LLMEndpoint struct {
	ID                string `gorm:"primaryKey;type:varchar(64)" json:"id"`
	Name              string `gorm:"type:varchar(255);not null" json:"name"`
	BaseURL           string `gorm:"type:text" json:"base_url"`
	APIKey            string `gorm:"type:text" json:"-"`
	ProtocolAnthropic bool   `gorm:"not null;default:0" json:"protocol_anthropic"`
	// ProtocolOpenAI 列名显式钉成 protocol_openai:gorm 会把 ProtocolOpenAI
	// 蛇形化成 protocol_open_ai,与 spec §5.1 的列名对不上。
	ProtocolOpenAI bool `gorm:"column:protocol_openai;not null;default:0" json:"protocol_openai"`
	AutoGroup      bool `gorm:"not null;default:0" json:"auto_group"`
	// Priority 越小越优先(对齐 sub2api 语义)。内置行恒 0,永远先被选中。
	Priority       int `gorm:"not null;default:0" json:"priority"`
	MaxConcurrency int `gorm:"not null;default:0" json:"max_concurrency"`
	// Models 是 JSON array 字符串(如 ["glm-4.6"]),[] = 不限制模型。列默认
	// '[]':避免 NULL/空串与「不限制」三种形态漂移(store.Create 还会把空串
	// 归一成 "[]")。
	Models  string `gorm:"type:text;default:'[]'" json:"models"`
	Enabled bool   `gorm:"not null;default:0" json:"enabled"`
	Builtin bool   `gorm:"not null;default:0" json:"builtin"`
	// HealthStatus 内部取值 ok/disabled/cooldown;health 字段由服务层(Task 2)
	// 维护,前端只读展示。
	HealthStatus string `gorm:"type:varchar(32);not null;default:'ok'" json:"health_status"`
	// 以下三个是运行态失败计数,不进任何 JSON 面板(展示走 last_error)。
	ConsecutiveFailures int   `gorm:"not null;default:0" json:"-"`
	CooldownUntil       int64 `gorm:"not null;default:0" json:"-"`
	RateLimitedUntil    int64 `gorm:"not null;default:0" json:"-"`
	// LastError 是最近一次失败摘要,展示用,不承载错误链。
	LastError string `gorm:"type:text" json:"last_error"`

	CreatedAt int64 `gorm:"not null" json:"created_at"`
	UpdatedAt int64 `gorm:"not null" json:"updated_at"`
}

func (LLMEndpoint) TableName() string { return "llm_endpoints" }

// Validate 校验端点行完整性。口径与 agentcore_llm 的校验(types.go 同包上文)
// 对齐:URL 必须含 scheme+host、只放行 http/https、非回环必须带 api_key
// (回环可以靠平台 relay 注入凭据);差异点是内置行 base_url 恒空(走默认隧道)。
func (e *LLMEndpoint) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("llm_endpoint.name 必填")
	}
	if !e.ProtocolAnthropic && !e.ProtocolOpenAI {
		return fmt.Errorf("llm_endpoint.protocol 至少勾选 anthropic/openai 之一")
	}
	if strings.TrimSpace(e.BaseURL) != "" {
		u, err := url.Parse(strings.TrimSpace(e.BaseURL))
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("llm_endpoint.base_url 必须是合法 URL(含 scheme 与 host), got %q", e.BaseURL)
		}
		// 与 agentcore_llm 同口径:有意放行远端 http://,家庭内网网关属合理形态。
		if !isLoopbackHostName(u.Hostname()) && strings.TrimSpace(e.APIKey) == "" {
			return fmt.Errorf("llm_endpoint.api_key 必填: 非回环地址 %s 无平台 relay 注入凭据", strings.TrimSpace(e.BaseURL))
		}
	}
	if e.Priority < 0 || e.MaxConcurrency < 0 {
		return fmt.Errorf("llm_endpoint.priority/max_concurrency 必须 >= 0")
	}
	// models 必须是 JSON 字符串数组:Task 2 的 Select/ModelsUnion 要反序列化
	// 它,落库非法 JSON 会在路由期静默炸。反序列化到 []string 一并卡住
	// 「合法 JSON 但不是数组/元素不是字符串」的形态,单一检查覆盖全部形状。
	if strings.TrimSpace(e.Models) != "" {
		var modelIDs []string
		if err := json.Unmarshal([]byte(e.Models), &modelIDs); err != nil {
			return fmt.Errorf("llm_endpoint.models 必须是 JSON 字符串数组, got %q", e.Models)
		}
	}
	if e.Builtin && e.BaseURL != "" {
		return fmt.Errorf("llm_endpoint.builtin 行的 base_url 必须为空(走内置隧道)")
	}
	return nil
}

// EndpointGroupPolicy 是 auto 组的组级摘除策略(spec §5.2)。本期只有一个组,
// 单行即可;Group 字段预留多组扩展,恒 "auto"。
type EndpointGroupPolicy struct {
	// Group 是表主键,列名刻意用 "group"(SQLite 保留字,查询侧必须引号)。
	Group                string `gorm:"column:group;primaryKey;type:varchar(64)" json:"group"`
	FailureDisable       bool   `gorm:"not null;default:0" json:"failure_disable"`
	FailureThreshold     int    `gorm:"not null;default:0" json:"failure_threshold"`
	CooldownSec          int    `gorm:"not null;default:0" json:"cooldown_sec"`
	RateLimitCooldownSec int    `gorm:"not null;default:0" json:"rate_limit_cooldown_sec"`
}

func (EndpointGroupPolicy) TableName() string { return "endpoint_group_policy" }
