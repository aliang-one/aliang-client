package storage

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"aliang.one/nursorgate/processor/config"
)

// builtinEndpointID 是内置 aliang 隧道行的固定主键(spec §5.3),整库唯一,
// 不可删除、base_url 恒空(走默认隧道链路,凭据用登录态)。
const builtinEndpointID = "builtin-aliang"

// autoGroup 是本期唯一的组键(spec §5.2):组策略表预留多组,但本期恒 auto。
const autoGroup = "auto"

// ErrBuiltinImmutable 标识对内置行的删除被拒;service 层据此映射 409。
var ErrBuiltinImmutable = errors.New("llm_endpoint: 内置 endpoint builtin-aliang 不可删除")

// EndpointStore 是 llm_endpoints / endpoint_group_policy 两张表的数据层。
// 只管落库与约束,不做路由选择(Task 2 service 层的职责)。
type EndpointStore struct {
	db *gorm.DB
	// ownsDB 标记句柄归属:测试构造自持 :memory: 句柄要关,生产构造复用
	// auth 共享句柄绝不能关(其他子系统还在用)。
	ownsDB bool
}

// NewEndpointStoreFromDB 是生产构造:复用 processor/auth 的共享句柄(同一
// aliang.data 单库),不自开文件、不关句柄;建表由 user_info.go 的 AutoMigrate
// 统一负责(经 SharedAuthSessionDB 触发单例时已带出这两张新表)。
func NewEndpointStoreFromDB(db *gorm.DB) (*EndpointStore, error) {
	if db == nil {
		return nil, errors.New("endpoint store: db 句柄为空")
	}
	s := &EndpointStore{db: db}
	if err := s.SeedBuiltin(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewEndpointStoreForTest 是测试构造::memory: 独立句柄,自建表自 seed,
// 完全不碰真实数据目录。
func NewEndpointStoreForTest() (*EndpointStore, error) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// :memory: 必须钉死单连接:SQLite 的内存库跟着连接走,连接池拉起第二个
	// 连接拿到的就是另一个空库,已建的表会"凭空消失"。池是懒连接,先限
	// 再跑迁移才安全。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := db.AutoMigrate(&config.LLMEndpoint{}, &config.EndpointGroupPolicy{}); err != nil {
		return nil, err
	}
	s := &EndpointStore{db: db, ownsDB: true}
	if err := s.SeedBuiltin(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close 只在测试构造(自持 :memory: 句柄)时真正关连接;生产构造复用的是
// auth 共享句柄,关掉会把整个 auth 持久化一起打挂,必须放行。
func (s *EndpointStore) Close() error {
	if !s.ownsDB {
		return nil
	}
	if sqlDB, err := s.db.DB(); err == nil {
		return sqlDB.Close()
	}
	return nil
}

// SeedBuiltin 以 INSERT OR IGNORE 语义幂等写入内置行:已存在的行(用户改过
// 协议开关等)永不被覆盖,每次启动重复调用也无害。
func (s *EndpointStore) SeedBuiltin() error {
	row := &config.LLMEndpoint{
		ID:     builtinEndpointID,
		Name:   "Aliang 隧道（内置）",
		APIKey: "", // 内置行走登录态,不需要 BYO key
		// 两个协议开关都开:56432 承载全部 AI 流量,OpenAI 请求也必须能
		// 落到内置链路(spec §5.3)。
		ProtocolAnthropic: true,
		ProtocolOpenAI:    true,
		AutoGroup:         true,
		Priority:          0, // 内置行恒最优先
		Enabled:           true,
		Builtin:           true,
		HealthStatus:      "ok",
	}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

// List 返回全部端点,稳定排序:priority 升序是路由视角,同优先级按 id 兜底,
// 避免 UI 列表顺序抖动。
func (s *EndpointStore) List() ([]*config.LLMEndpoint, error) {
	var es []*config.LLMEndpoint
	if err := s.db.Order("priority ASC, id ASC").Find(&es).Error; err != nil {
		return nil, err
	}
	return es, nil
}

// Get 按 id 取单行;不存在时报错并保留 gorm.ErrRecordNotFound 供上层判 404。
func (s *EndpointStore) Get(id string) (*config.LLMEndpoint, error) {
	var e config.LLMEndpoint
	if err := s.db.First(&e, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("endpoint %s 不存在: %w", id, gorm.ErrRecordNotFound)
		}
		return nil, err
	}
	return &e, nil
}

// Create 新增端点。id 必填且唯一(预查重是为了给上层一个可映射 409 的清晰
// 文案,而不是透传 sqlite 主键冲突)。
func (s *EndpointStore) Create(e *config.LLMEndpoint) error {
	if e == nil {
		return errors.New("llm_endpoint: endpoint 不能为空")
	}
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("llm_endpoint.id 必填")
	}
	if err := e.Validate(); err != nil {
		return err
	}
	var n int64
	if err := s.db.Model(&config.LLMEndpoint{}).Where("id = ?", e.ID).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("llm_endpoint.id 已存在: %s", e.ID)
	}
	return s.db.Create(e).Error
}

// Update 整行保存。预查存在性是刻意的:gorm Save 对未命中的主键会静默转
// INSERT(upsert),会把「改一条不存在的行」变成凭空造行,管理面必须报不存在。
func (s *EndpointStore) Update(e *config.LLMEndpoint) error {
	if e == nil {
		return errors.New("llm_endpoint: endpoint 不能为空")
	}
	if err := e.Validate(); err != nil {
		return err
	}
	var n int64
	if err := s.db.Model(&config.LLMEndpoint{}).Where("id = ?", e.ID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("endpoint %s 不存在: %w", e.ID, gorm.ErrRecordNotFound)
	}
	return s.db.Save(e).Error
}

// Delete 删除端点;内置行恒拒(spec §5.1: builtin=1 的行 DELETE 返回 409)。
// 对不存在的 id 幂等成功,由上层决定要不要先查。
func (s *EndpointStore) Delete(id string) error {
	if id == builtinEndpointID {
		return ErrBuiltinImmutable
	}
	return s.db.Delete(&config.LLMEndpoint{}, "id = ?", id).Error
}

// defaultGroupPolicy 是 spec §5.2 的默认策略:失败摘除开、连续 3 次摘除、
// 冷却 300s、429 冷却 60s。
func defaultGroupPolicy() *config.EndpointGroupPolicy {
	return &config.EndpointGroupPolicy{
		Group:                autoGroup,
		FailureDisable:       true,
		FailureThreshold:     3,
		CooldownSec:          300,
		RateLimitCooldownSec: 60,
	}
}

// GetPolicy 取组策略;无行时返回默认值(不落库——读路径不做写,首次 Set
// 才产生行)。
func (s *EndpointStore) GetPolicy() (*config.EndpointGroupPolicy, error) {
	var p config.EndpointGroupPolicy
	// 结构体条件走 gorm 的列名引号管道:group 是 SQLite 保留字,裸写进
	// WHERE 字符串会直接语法错误。
	if err := s.db.First(&p, &config.EndpointGroupPolicy{Group: autoGroup}).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return defaultGroupPolicy(), nil
		}
		return nil, err
	}
	return &p, nil
}

// SetPolicy 整行保存组策略,无行时 upsert 成行。组键恒 auto(本期只有一组,
// 防调用方写错组名悄悄长出第二行)。
func (s *EndpointStore) SetPolicy(p *config.EndpointGroupPolicy) error {
	if p == nil {
		return errors.New("endpoint_policy: 策略不能为空")
	}
	if p.FailureThreshold < 0 || p.CooldownSec < 0 || p.RateLimitCooldownSec < 0 {
		return fmt.Errorf("endpoint_policy 阈值/冷却时长必须 >= 0")
	}
	p.Group = autoGroup
	return s.db.Save(p).Error
}
