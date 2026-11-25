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

// BuiltinEndpointID 是内置 aliang 隧道行的固定主键(spec §5.3),整库唯一,
// 不可删除、base_url 恒空(走默认隧道链路,凭据用登录态)。跨包引用走本常量,
// 避免字符串字面量在 services/inbound 各处漂移。
const BuiltinEndpointID = "builtin-aliang"

// autoGroup 是本期唯一的组键(spec §5.2):组策略表预留多组,但本期恒 auto。
const autoGroup = "auto"

// ErrBuiltinImmutable 标识对内置行的变更被拒(删除/摘 builtin);service 层
// 据此映射 409。文案从常量派生,避免字面量与 BuiltinEndpointID 漂移。
var ErrBuiltinImmutable = errors.New("llm_endpoint: 内置 endpoint " + BuiltinEndpointID + " 不可变更")

// ErrEndpointIDExists 标识 Create 撞主键(预查重命中或 sqlite UNIQUE 兜底)。
// 哨兵化是为了 handler 能 errors.Is 判定映射 409,而不是匹配错误文案。
var ErrEndpointIDExists = errors.New("llm_endpoint.id 已存在")

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
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// SeedBuiltin 以 INSERT OR IGNORE 语义幂等写入内置行:已存在的行(用户改过
// 协议开关等)永不被覆盖,每次启动重复调用也无害。
func (s *EndpointStore) SeedBuiltin() error {
	row := &config.LLMEndpoint{
		ID:     BuiltinEndpointID,
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

// validateBuiltinInvariants 钉死内置行不变量(spec §5.1: builtin=1 仅
// id='builtin-aliang')。没有这条,Create 可用任意 id 插队 builtin 语义,
// Update 可把用户行翻成 builtin 白嫖平台 relay 凭据;反方向(id 相同却
// builtin=false)也一并堵死——否则 Delete 按 flag 判定就形同虚设。
// 注意:这条必须在 store 层做,config.Validate 拦不住「空 base_url + 假
// builtin id」的组合(它不知道保留 id 是什么,config 也不能反向 import storage)。
func validateBuiltinInvariants(e *config.LLMEndpoint) error {
	if e.Builtin {
		if e.ID != BuiltinEndpointID {
			return fmt.Errorf("llm_endpoint.builtin 行必须是 %s, got %s", BuiltinEndpointID, e.ID)
		}
		if !e.AutoGroup {
			return fmt.Errorf("llm_endpoint.builtin 行 auto_group 不可关(恒 1)")
		}
		if e.Priority != 0 {
			return fmt.Errorf("llm_endpoint.builtin 行 priority 恒 0, got %d", e.Priority)
		}
		return nil
	}
	if e.ID == BuiltinEndpointID {
		return fmt.Errorf("llm_endpoint: %s 是保留 id,必须保持 builtin=true", BuiltinEndpointID)
	}
	return nil
}

// Create 新增端点。id 必填且唯一(预查重是为了给上层一个可映射 409 的清晰
// 文案,而不是透传 sqlite 主键冲突)。
//
// 默认值纪律:
//   - priority:非 builtin 且未给(0)时归一为 50(spec §5.1 默认值);builtin
//     由 SeedBuiltin 直插恒 0,永远第一,不受归一影响。
//   - models:空串归一为 "[]"([..] = 不限制模型),避免 NULL/空串语义漂移。
//   - enabled:由调用方显式给值——bool 零值会被原样落 0,store 层刻意不做
//     默认(gorm default:1 会造成「无法显式建禁用行」的陷阱);REST handler
//     (Task 4)对新建默认 true。
func (s *EndpointStore) Create(e *config.LLMEndpoint) error {
	if e == nil {
		return errors.New("llm_endpoint: endpoint 不能为空")
	}
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("llm_endpoint.id 必填")
	}
	// 归一在 Validate 之前:校验看到的是归一后的形态。
	if !e.Builtin && e.Priority == 0 {
		e.Priority = 50
	}
	if strings.TrimSpace(e.Models) == "" {
		e.Models = "[]"
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := validateBuiltinInvariants(e); err != nil {
		return err
	}
	// Count-then-Create 非原子:并发窗口兜底走主键约束(sqlite 会报 UNIQUE
	// 冲突),不会静默覆盖;这里提前拦只是为了可映射的 409 文案。
	var n int64
	if err := s.db.Model(&config.LLMEndpoint{}).Where("id = ?", e.ID).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s", ErrEndpointIDExists, e.ID)
	}
	if err := s.db.Create(e).Error; err != nil {
		// sqlite 主键冲突的兜底路径(并发窗口):归一成哨兵,handler 同样 409。
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: %s", ErrEndpointIDExists, e.ID)
		}
		return err
	}
	return nil
}

// endpointConfigColumns 是 Update(REST/配置面)允许写穿的列白名单。健康列
// (health_status / consecutive_failures / cooldown_until / rate_limited_until /
// last_error)刻意不在内:健康态的唯一写入口是 UpdateHealth,否则 Task 4 的
// GET-merge-PUT 全行回写会用陈旧快照盖掉服务面刚写的健康态(评审 Important 3
// 指出的 clobber)。新增健康列只进 UpdateHealth;新增配置列必须同步加进本表。
var endpointConfigColumns = []string{
	"name", "base_url", "api_key", "protocol_anthropic", "protocol_openai",
	"auto_group", "priority", "max_concurrency", "models", "enabled", "builtin",
}

// Update 按白名单整行保存配置列。健康列不在写集里——调用方改了
// HealthStatus 等字段也不会落库(健康写入走 UpdateHealth);created_at 同样
// 不动(由建行时固定)。预查存在性是刻意的:gorm Save 对未命中的主键会静默
// 转 INSERT(upsert),会把「改一条不存在的行」变成凭空造行,管理面必须报
// 不存在。显式 Select + struct 让零值配置列(enabled=false 等)也照写,
// 与 Save 的全字段语义在白名单内等价。
func (s *EndpointStore) Update(e *config.LLMEndpoint) error {
	if e == nil {
		return errors.New("llm_endpoint: endpoint 不能为空")
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := validateBuiltinInvariants(e); err != nil {
		return err
	}
	var n int64
	if err := s.db.Model(&config.LLMEndpoint{}).Where("id = ?", e.ID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("endpoint %s 不存在: %w", e.ID, gorm.ErrRecordNotFound)
	}
	return s.db.Model(e).Where("id = ?", e.ID).Select(endpointConfigColumns).Updates(e).Error
}

// HealthUpdate 是 UpdateHealth 的载荷。独立 struct 而非六个位置参数:其中两个
// int64(CooldownUntil/RateLimitedUntil)相邻且语义相近,位置传参在调用点极易
// 转置(2026-11-25 Task 1 质量评审指出),具名字段让每个调用点自证语义。
type HealthUpdate struct {
	Status           string // ok / disabled / cooldown
	Failures         int    // 连续失败计数
	CooldownUntil    int64  // 摘除冷却截止(unix 秒;0 = 未冷却)
	RateLimitedUntil int64  // 429 冷却截止(unix 秒;0 = 未限流)
	LastErr          string // 最近一次失败摘要(展示用)
}

// UpdateHealth 是健康列的唯一写入口(窄更新缝):服务面记失败/恢复/冷却走
// 这里,只动指定的五个健康列;REST 面(Update 白名单)写不到健康列,两边
// 不再互相 clobber。
func (s *EndpointStore) UpdateHealth(id string, u HealthUpdate) error {
	return s.db.Model(&config.LLMEndpoint{}).Where("id = ?", id).Updates(map[string]interface{}{
		"health_status":        u.Status,
		"consecutive_failures": u.Failures,
		"cooldown_until":       u.CooldownUntil,
		"rate_limited_until":   u.RateLimitedUntil,
		"last_error":           u.LastErr,
	}).Error
}

// Delete 删除端点;内置行恒拒(spec §5.1: builtin=1 的行 DELETE 返回 409)。
// 按 flag 判定而非 id 字符串:即使将来某条旁路(迁移/导入)造出带 builtin
// 语义但 id 不同的行,这里也拦得住。对不存在的 id 幂等成功,由上层决定要
// 不要先查。
func (s *EndpointStore) Delete(id string) error {
	var e config.LLMEndpoint
	if err := s.db.First(&e, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if e.Builtin {
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
